package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/nicccce/meshstrata/internal/buildinfo"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/httpx"
	"github.com/nicccce/meshstrata/internal/localstate"
)

type Health struct {
	Status string `json:"status"`
	buildinfo.Info
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
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpx.Endpoint(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, Health{Status: "ok", Info: buildinfo.Current("agent")})
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "NOT_FOUND")
	})
	logger.Info("Agent started", "health_socket", path)
	if err := httpx.Serve(ctx, listener, mux, logger); err != nil {
		return err
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
