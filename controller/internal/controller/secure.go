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
	"github.com/nicccce/meshstrata/controller/internal/business"
	"github.com/nicccce/meshstrata/controller/internal/clientconfig"
	channel "github.com/nicccce/meshstrata/controller/internal/control"
	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/enrollment"
	"github.com/nicccce/meshstrata/controller/internal/releases"
	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/rules"
	"github.com/nicccce/meshstrata/controller/internal/secrets"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/config"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

type secureServices struct {
	business   *business.Service
	admin      http.Handler
	node       http.Handler
	webTLS     *tls.Config
	nodeTLS    *tls.Config
	channel    *channel.Server
	releases   *releases.Service
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
	publication, e := releases.New(releases.NewRepository(store), cipher, guard, releases.Options{
		RequirePreparation: cfg.EnableV2, EgressProbes: cfg.EgressProbes,
		V2Session: func(id domain.ID) releases.V2Session {
			if !cfg.EnableV2 || ch == nil {
				return releases.V2Session{}
			}
			session, format := ch.RecoverySession(id)
			return releases.V2Session{SessionID: session, RecoveryFormat: format}
		},
		V2Deliver: func(ctx context.Context, cmd protocol.Command) error {
			if !cfg.EnableV2 || ch == nil {
				return channel.ErrUnavailable
			}
			return ch.DeliverV2(ctx, cmd)
		},
		Now: time.Now, NewID: domain.NewID, RemovalIntent: nodes.RemovalIntent,
		Deliver: func(ctx context.Context, cmd protocol.Command) error {
			if ch == nil {
				return channel.ErrUnavailable
			}
			return ch.Deliver(ctx, cmd)
		},
		Connected: func(id domain.ID) bool { return ch != nil && ch.RuntimeConnected(id) },
	})
	if e != nil {
		return nil, e
	}
	ch, e = channel.New(channel.Options{
		EnableV2: cfg.EnableV2, Observations: publication,
		Authenticator: nodes, Results: publication, Recovery: publication,
		Artifacts: publication, ArtifactResolver: publication, RequireRuntime: true,
	})
	if e != nil {
		publication.Close()
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			ch.Close()
			publication.Close()
		}
	}()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/", guard.Handler())
	mux.Handle("/api/v1/nodes", nodes.ManagementHandler())
	mux.Handle("/api/v1/nodes/", nodes.ManagementHandler())
	mux.Handle("/api/v1/tasks", publication.ManagementHandler())
	mux.Handle("/api/v1/tasks/", publication.ManagementHandler())
	var biz *business.Service
	var admin http.Handler = mux
	if cfg.EnableV2 {
		runtime := stage3.Runtime{DB: store}
		targets, err := egress.NewService(stage3.EgressOptions{Runtime: runtime, Cipher: cipher})
		if err != nil {
			return nil, err
		}
		credentials, err := secrets.NewCredentials(secrets.NewRepository(store), cipher, secrets.CredentialOptions{})
		if err != nil {
			return nil, err
		}
		paths, err := routes.NewService(stage3.RoutesOptions{Runtime: runtime, Publisher: publication, Egress: targets, Infra: publication, Secrets: credentials, Cipher: cipher})
		if err != nil {
			return nil, err
		}
		catalog, err := rules.NewService(stage3.RulesOptions{Runtime: runtime})
		if err != nil {
			return nil, err
		}
		builder, err := clientconfig.NewBuilder(stage3.ClientOptions{})
		if err != nil {
			return nil, err
		}
		subs, err := subscriptions.NewService(stage3.SubscriptionsOptions{Runtime: runtime, Guard: guard, Cipher: cipher, Routes: paths, Rules: catalog, Client: builder, Publisher: publication, PublicOrigin: cfg.PublicOrigin})
		if err != nil {
			return nil, err
		}
		biz = &business.Service{Cipher: cipher, DB: store, Guard: guard, Routes: paths, Rules: catalog, Egress: targets, Subscriptions: subs, Client: builder, Releases: publication, Channel: ch}
		businessHTTP := biz.Handler()
		for _, prefix := range []string{"routes", "rule-profiles", "domain-sets", "egress-targets", "egress-policies", "subscriptions"} {
			mux.Handle("/api/v1/"+prefix, businessHTTP)
			mux.Handle("/api/v1/"+prefix+"/", businessHTTP)
		}
		for _, action := range []string{"preparation", "observe", "drain-legacy", "switch-v2"} {
			mux.Handle("/api/v1/nodes/{id}/"+action, businessHTTP)
		}
		sensitive := subs.HTTPHandler()
		admin = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := subscriptions.SensitiveTemplate(r); ok {
				sensitive.ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		})
	}
	mux.Handle("/", base)
	success = true
	return &secureServices{business: biz, admin: admin, node: nodes.NodeHandler(ch.Handler()), webTLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, NextProtos: []string{"http/1.1"}}, nodeTLS: nodes.TLSConfig(), channel: ch, releases: publication, enrollment: nodes}, nil
}
