package controller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/auth"
	channel "github.com/nicccce/meshstrata/controller/internal/control"
	"github.com/nicccce/meshstrata/controller/internal/enrollment"
	"github.com/nicccce/meshstrata/controller/internal/secrets"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/internal/config"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

type secureServices struct {
	admin      http.Handler
	node       http.Handler
	webTLS     *tls.Config
	nodeTLS    *tls.Config
	channel    *channel.Server
	enrollment *enrollment.Service
}
type bootstrapMaterial struct {
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
}

func composeSecure(ctx context.Context, cfg config.ControllerSecure, store *storage.Store, base http.Handler) (*secureServices, error) {
	// Validate every dependency before any listener is exposed.
	certPEM, e := privatefile.Read(cfg.TLSCertFile, 1<<20)
	if e != nil {
		return nil, e
	}
	keyPEM, e := privatefile.Read(cfg.TLSKeyFile, 1<<20)
	if e != nil {
		return nil, e
	}
	defer clear(keyPEM)
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		return nil, errors.New("web TLS identity unavailable")
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return nil, errors.New("web TLS certificate invalid or expired")
	}
	for _, origin := range cfg.AllowedOrigins {
		u, e := url.Parse(origin)
		if e != nil || leaf.VerifyHostname(u.Hostname()) != nil {
			return nil, errors.New("web TLS certificate does not cover allowed origin")
		}
	}
	cipher, e := secrets.New(secrets.Options{CurrentKeyID: cfg.CurrentKeyID, KeyFiles: cfg.KeyFiles})
	if e != nil {
		return nil, e
	}
	var bootstrap *auth.Bootstrap
	if cfg.BootstrapFile != "" {
		raw, e := privatefile.Read(cfg.BootstrapFile, 4096)
		if e != nil {
			return nil, e
		}
		defer clear(raw)
		var b bootstrapMaterial
		if json.Unmarshal(raw, &b) != nil {
			return nil, errors.New("bootstrap material unavailable")
		}
		hash, e := hex.DecodeString(b.SHA256)
		if e != nil || len(hash) != sha256.Size {
			return nil, errors.New("bootstrap material unavailable")
		}
		// An expired proof cannot initialize; ordinary restarts remain available.
		if time.Now().Before(b.ExpiresAt) {
			bootstrap = &auth.Bootstrap{SHA256: hash, ExpiresAt: b.ExpiresAt}
		}
	}
	guard, e := auth.New(auth.NewRepository(store), auth.Options{AllowedOrigins: cfg.AllowedOrigins, Bootstrap: bootstrap})
	if e != nil {
		return nil, e
	}
	var ch *channel.Server
	nodes, e := enrollment.New(enrollment.NewRepository(store), cipher, guard, enrollment.Options{
		Context: ctx, ControllerURL: cfg.ControllerURL, ServerNames: cfg.ServerNames,
		OnIdentityChange: func(id domain.ID) {
			if ch != nil {
				ch.Disconnect(id)
			}
		},
		ControlConnected: func(id domain.ID) bool { return ch != nil && ch.Connected(id) },
		// Reverse requires B12/B17 trusted local Relay capability; role alone is insufficient.
	})
	if e != nil {
		return nil, e
	}
	ch, e = channel.New(channel.Options{Authenticator: nodes, Recovery: pristineRecovery{}})
	if e != nil {
		return nil, e
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/", guard.Handler())
	mux.Handle("/api/v1/nodes", nodes.ManagementHandler())
	mux.Handle("/api/v1/nodes/", nodes.ManagementHandler())
	mux.Handle("/", base)
	return &secureServices{admin: mux, node: nodes.NodeHandler(ch.Handler()), webTLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, NextProtos: []string{"http/1.1"}}, nodeTLS: nodes.TLSConfig(), channel: ch, enrollment: nodes}, nil
}

// Stage 1 has no business recovery/result consumer. Reject pre-existing business
// state instead of treating it as an empty node or acknowledging a fake result.
type pristineRecovery struct{}

func (pristineRecovery) Hello(_ context.Context, id domain.ID, h protocol.Hello) error {
	if h.NodeID != id || h.AppliedRevision != 0 || h.AppliedDigest != "" || h.RevocationEpoch != 1 || len(h.PendingResults) != 0 {
		return channel.ErrUnavailable
	}
	return nil
}
