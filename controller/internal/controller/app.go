package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/nicccce/meshstrata/internal/buildinfo"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/httpx"
	"github.com/nicccce/meshstrata/internal/localstate"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/webui"
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
