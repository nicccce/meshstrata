package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	channel "github.com/nicccce/meshstrata/agent/internal/control"
	"github.com/nicccce/meshstrata/agent/internal/enrollment"
	"github.com/nicccce/meshstrata/internal/buildinfo"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/httpx"
	"github.com/nicccce/meshstrata/internal/localstate"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

type Health struct {
	Status string `json:"status"`
	buildinfo.Info
	ControlMode       string `json:"control_mode,omitempty"`
	BusinessExecution string `json:"business_execution"`
}

func Run(ctx context.Context, cfg config.Agent, logger *slog.Logger) error {
	if err := localstate.Prepare(cfg.StateDir); err != nil {
		return err
	}
	unlock, err := localstate.Lock(cfg.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	local, cancel := context.WithCancel(ctx)
	defer cancel()
	if cfg.Runtime == nil {
		for _, name := range []string{"runtime", runtimeModeFile} {
			if _, e := os.Lstat(filepath.Join(cfg.StateDir, name)); !errors.Is(e, os.ErrNotExist) {
				return errors.New("runtime state requires runtime mode")
			}
		}
	}
	runtimeExit := make(chan error, 1)
	var client *channel.Client
	var runtimeClient atomic.Pointer[channel.Client]
	var jobs sync.WaitGroup
	if cfg.Control != nil {
		identity, enrollmentClient, e := openEnrollment(cfg)
		if e != nil {
			return stopOnStartupFailure(cfg, e)
		}
		options := channel.Options{
			URL:    "wss" + strings.TrimSuffix(strings.TrimPrefix(cfg.Control.ControllerURL, "https"), "/") + "/control/v1",
			NodeID: domain.ID(cfg.Control.NodeID), TLSConfig: enrollmentClient.TLSConfig,
			BeforeConnect: func(ctx context.Context) error { _, e := enrollmentClient.RenewIfNeeded(ctx, 7*24*time.Hour); return e },
			OnRejected:    func() { logger.Warn("Control command rejected") },
		}
		if identity.NodeID() != options.NodeID {
			return stopOnStartupFailure(cfg, errors.New("enrolled node identity mismatch"))
		}
		if _, e = identity.Certificate(time.Now()); e != nil {
			return stopOnStartupFailure(cfg, e)
		}
		if cfg.Runtime != nil {
			engine, closeRuntime, err := composeRuntime(local, cfg, &runtimeClient, logger)
			if err != nil {
				return err
			}
			defer closeRuntime()
			options.Runtime = engine
			go func() {
				runtimeExit <- engine.Run(local)
				cancel()
			}()
			select {
			case <-engine.Ready():
				if local.Err() != nil {
					return <-runtimeExit
				}
			case err := <-runtimeExit:
				return err
			}
		} else {
			options.ControlOnly, e = channel.OpenControlOnly(cfg.StateDir, options.NodeID)
			if e != nil {
				return e
			}
		}
		client, e = channel.New(options)
		if e != nil {
			return e
		}
		runtimeClient.Store(client)
		defer func() { runtimeClient.Store(nil); cancel(); client.Close(); jobs.Wait() }()
		jobs.Add(2)
		go func() { defer jobs.Done(); _ = client.Run(local) }()
		go func() {
			defer jobs.Done()
			_ = enrollmentClient.Maintain(local, time.Hour, 7*24*time.Hour, client.Reconnect, func(error) { logger.Warn("Management certificate renewal unavailable") })
		}()
		logger.Info("Control client ready", "mode", controlMode(cfg))
	}
	// The process lock makes stale socket recovery safe. Never replace files
	// or symlinks accidentally left at the configured socket path.
	path := cfg.SocketPath()
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("health socket path exists and is not a socket: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale health socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect health socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen for Agent health: %w", err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict health socket permissions: %w", err)
	}
	execution := "disabled"
	if cfg.Runtime != nil {
		execution = "enabled"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpx.Endpoint(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, Health{Status: "ok", Info: buildinfo.Current("agent"), ControlMode: controlMode(cfg), BusinessExecution: execution})
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "NOT_FOUND")
	})
	logger.Info("Agent started", "health_socket", path)
	if err := httpx.Serve(local, listener, mux, logger); err != nil {
		return err
	}
	if cfg.Runtime != nil {
		return <-runtimeExit
	}
	logger.Info("Agent stopped")
	return nil
}

func CheckHealth(ctx context.Context, cfg config.Agent) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.SocketPath())
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/healthz", nil)
	if err != nil {
		return Health{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Health{}, fmt.Errorf("query Agent health: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("Agent health returned HTTP %d", response.StatusCode)
	}
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return Health{}, fmt.Errorf("decode Agent health: %w", err)
	}
	if health.Component != "agent" || health.Status != "ok" {
		return Health{}, errors.New("Agent returned an invalid health response")
	}
	return health, nil
}

func controlMode(cfg config.Agent) string {
	if cfg.Control != nil {
		return cfg.Control.Mode
	}
	return "disabled"
}
func openEnrollment(cfg config.Agent) (*enrollment.Identity, *enrollment.Client, error) {
	if cfg.Control == nil {
		return nil, nil, errors.New("control configuration required")
	}
	ca, e := privatefile.Read(cfg.Control.CAFile, 32<<10)
	if e != nil {
		return nil, nil, e
	}
	identity, e := enrollment.Open(filepath.Join(cfg.StateDir, "identity"))
	if e != nil {
		return nil, nil, e
	}
	client, e := enrollment.New(identity, enrollment.Options{ControllerURL: cfg.Control.ControllerURL, CAPEM: ca, CAFingerprint: cfg.Control.CAFingerprint, NodeID: domain.ID(cfg.Control.NodeID)})
	return identity, client, e
}

// Enroll is an explicit local action. Token is read only from a restricted file;
// it is never persisted in identity/config or supplied as a command argument.
func Enroll(ctx context.Context, cfg config.Agent, _ io.Reader) error {
	if cfg.Control == nil || cfg.Control.TokenFile == "" {
		return errors.New("enroll requires control.token_file")
	}
	if e := localstate.Prepare(cfg.StateDir); e != nil {
		return e
	}
	unlock, e := localstate.Lock(cfg.StateDir)
	if e != nil {
		return e
	}
	defer unlock()
	if _, e := channel.OpenControlOnly(cfg.StateDir, domain.ID(cfg.Control.NodeID)); e != nil {
		return e
	}
	_, client, e := openEnrollment(cfg)
	if e != nil {
		return e
	}
	token, e := privatefile.Read(cfg.Control.TokenFile, 128)
	if e != nil {
		return e
	}
	defer clear(token)
	return client.Enroll(ctx, strings.TrimSpace(string(token)))
}
