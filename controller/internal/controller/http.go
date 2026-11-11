package controller

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/nicccce/meshstrata/internal/buildinfo"
	"github.com/nicccce/meshstrata/internal/httpx"
	"github.com/nicccce/meshstrata/controller/internal/webui"
)

type Readiness interface {
	Ready(context.Context) error
}

func Handler(store Readiness, info buildinfo.Info, instanceID string, files fs.FS) (http.Handler, error) {
	ui, err := webui.Handler(files)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpx.Endpoint(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok", "component": "controller"})
	}))
	mux.HandleFunc("/readyz", httpx.Endpoint(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ready(ctx); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "STORAGE_NOT_READY")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}))
	mux.HandleFunc("/api/v1/system", httpx.Endpoint(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, struct {
			buildinfo.Info
			InstanceID string `json:"instance_id"`
		}{info, instanceID})
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			httpx.Error(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		ui.ServeHTTP(w, r)
	})
	return mux, nil
}
