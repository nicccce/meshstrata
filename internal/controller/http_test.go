package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/nicccce/meshstrata/internal/buildinfo"
)

type readiness struct{ err error }

func (r *readiness) Ready(context.Context) error { return r.err }

func TestHTTPContract(t *testing.T) {
	store := &readiness{}
	files := fstest.MapFS{"index.html": {Data: []byte("<html>Meshstrata</html>")}, "assets/app.js": {Data: []byte("console.log('ready')")}, ".gitkeep": {}}
	handler, err := Handler(store, buildinfo.Current("controller"), "test-instance", files)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/healthz", 200, ""}, {"GET", "/readyz", 200, ""},
		{"GET", "/api/v1/system", 200, ""}, {"GET", "/api/missing", 404, "NOT_FOUND"},
		{"GET", "/api", 404, "NOT_FOUND"}, {"POST", "/api/v1/system", 405, "METHOD_NOT_ALLOWED"},
		{"GET", "/", 200, ""}, {"GET", "/assets/app.js", 200, ""},
		{"GET", "/assets/missing.js", 404, ""}, {"GET", "/.gitkeep", 404, ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			if tc.code != "" {
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != tc.code {
					t.Fatalf("unexpected error: %+v", body)
				}
			}
		})
	}
	store.err = errors.New("database closed")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != 503 {
		t.Fatalf("unavailable database is ready: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != 200 {
		t.Fatal("liveness depends on database availability")
	}
}

func TestMissingFrontendRefusesStartup(t *testing.T) {
	if _, err := Handler(&readiness{}, buildinfo.Current("controller"), "", fstest.MapFS{".gitkeep": {}}); err == nil {
		t.Fatal("missing frontend accepted")
	}
}
