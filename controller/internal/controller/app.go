package controller

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/webui"
	"github.com/nicccce/meshstrata/internal/buildinfo"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/httpx"
	"github.com/nicccce/meshstrata/internal/localstate"
)

func Run(ctx context.Context, cfg config.Controller, logger *slog.Logger) error {
	files, err := webui.Embedded()
	if err != nil {
		return err
	}
	// Fail before creating state if the binary has no frontend assets.
	if _, err := webui.Handler(files); err != nil {
		return err
	}
	if err := localstate.Prepare(cfg.StateDir); err != nil {
		return err
	}
	unlock, err := localstate.Lock(cfg.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	store, err := storage.Open(startup, cfg.StateDir)
	cancel()
	if err != nil {
		return err
	}
	defer store.Close()
	handler, err := Handler(store, buildinfo.Current("controller"), store.InstanceID, files)
	if err != nil {
		return err
	}
	if cfg.Secure != nil {
		return runSecure(ctx, cfg, store, handler, logger)
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for Controller HTTP: %w", err)
	}
	logger.Info("Controller started", "listen_address", listener.Addr().String())
	err = httpx.Serve(ctx, listener, handler, logger)
	if err == nil || errors.Is(err, context.Canceled) {
		logger.Info("Controller stopped")
		return nil
	}
	return err
}

func runSecure(ctx context.Context, cfg config.Controller, store *storage.Store, base http.Handler, logger *slog.Logger) error {
	services, e := composeSecure(ctx, *cfg.Secure, store, base)
	if e != nil {
		return e
	}
	defer services.channel.Close()
	web, e := net.Listen("tcp", cfg.ListenAddress)
	if e != nil {
		return fmt.Errorf("listen for HTTPS: %w", e)
	}
	defer web.Close()
	node, e := net.Listen("tcp", cfg.Secure.ControlListenAddress)
	if e != nil {
		return fmt.Errorf("listen for node TLS: %w", e)
	}
	defer node.Close()
	local, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- httpx.Serve(local, tls.NewListener(web, services.webTLS), services.admin, logger) }()
	go func() { done <- httpx.Serve(local, tls.NewListener(node, services.nodeTLS), services.node, logger) }()
	logger.Info("Controller HTTPS and node control ready", "listen_address", web.Addr().String(), "control_address", node.Addr().String(), "business_execution", "disabled")
	err := <-done
	cancel()
	services.channel.Close() // HTTP Shutdown cannot close hijacked WebSockets.
	other := <-done
	if err == nil {
		err = other
	}
	return err
}
