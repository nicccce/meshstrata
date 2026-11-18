package subscriptions

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/nicccce/meshstrata/internal/domain"
)

const managementPrefix = "/api/v1/subscriptions/"
const copySuffix = "/download-link"

func noLeak(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(domain.ErrorResponse{Error: domain.Problem{Code: domain.NotFound}})
}

// SensitiveTemplate is for the outer dispatcher/access logger. It recognizes
// malformed variants too; the handler never routes using a cleaned path.
// Run it before ServeMux and any middleware using URI/path for logs or metrics.
func SensitiveTemplate(r *http.Request) (string, bool) {
	p := r.URL.Path
	normalized := path.Clean("/" + strings.TrimLeft(p, "/"))
	if strings.HasPrefix(p, "/subscriptions/") || strings.HasPrefix(normalized, "/subscriptions/") ||
		normalized == "/subscriptions" || strings.Contains(p, configPath) {
		return configPath + "{token}", true
	}
	if (strings.HasPrefix(p, managementPrefix) || strings.HasPrefix(normalized, managementPrefix)) &&
		(strings.HasSuffix(normalized, copySuffix) || strings.Contains(p, copySuffix)) {
		return managementPrefix + "{subscription_id}" + copySuffix, true
	}
	return "", false
}

// HTTPHandler is deliberately unregistered. Dispatch sensitive requests from
// the outer HTTPS server before ServeMux (including malformed paths). Do not
// attach any generic redirect or access-log middleware to these requests.
func (s *Service) HTTPHandler() http.Handler {
	copyLink := s.o.Guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, managementPrefix)
		out, err := s.DownloadLink(r.Context(), domain.ID(strings.TrimSuffix(p, copySuffix)))
		if err != nil {
			notFound(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noLeak(w)
		p := r.URL.Path
		if r.URL.EscapedPath() != p || (r.URL.RawPath != "" && r.URL.RawPath != p) ||
			r.URL.ForceQuery || r.URL.RawQuery != "" {
			notFound(w)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(p, configPath):
			token := strings.TrimPrefix(p, configPath)
			out, err := s.DownloadFull(r.Context(), token)
			if err != nil {
				notFound(w)
				return
			}
			defer clear(out)
			w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
			_, _ = w.Write(out)
		case r.Method == http.MethodPost && strings.HasPrefix(p, managementPrefix) && strings.HasSuffix(p, copySuffix):
			id := domain.ID(strings.TrimSuffix(strings.TrimPrefix(p, managementPrefix), copySuffix))
			if !id.Valid() {
				notFound(w)
				return
			}
			copyLink.ServeHTTP(w, r)
		default:
			notFound(w)
		}
	})
}
