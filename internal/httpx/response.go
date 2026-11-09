package httpx

import (
	"encoding/json"
	"net/http"
)

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Error(w http.ResponseWriter, status int, code string) {
	JSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}

// Endpoint enforces a read-only endpoint with consistent JSON errors.
func Endpoint(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			Error(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return
		}
		fn(w, r)
	}
}
