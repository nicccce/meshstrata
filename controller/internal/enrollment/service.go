// Package enrollment owns management CA and node enrollment, independently of B01.
package enrollment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nicccce/meshstrata/internal/adminauth"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type Options struct {
	Context             context.Context
	Now                 func() time.Time
	ControllerURL       string
	ServerNames         []string
	CertificateLifetime time.Duration
	// Set before serving; runs only after commit. Close every old node control channel.
	OnIdentityChange func(domain.ID)
	// Runtime control presence; never implies proxy verification.
	ControlConnected func(domain.ID) bool
	// Return true only for the Controller-local both Agent with enabled Relay.
	// Reverse enrollment is unavailable unless the composition root supplies it.
	RelayAllowed func(context.Context, domain.ID) bool
}
type Service struct {
	repo         *Repository
	cipher       secretstore.Cipher
	guard        adminauth.Guard
	options      Options
	ca           *pki.Authority
	tlsMu        sync.Mutex
	serverCert   tls.Certificate
	serverExpiry time.Time
}
type IssueRequest struct {
	ExpectedRevision domain.Revision `json:"expected_revision"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Name             string          `json:"name,omitempty"`
	Mode             string          `json:"connection_mode,omitempty"`
	Role             string          `json:"role,omitempty"`
	ControlAddress   string          `json:"control_address,omitempty"`
	ProxyAddress     string          `json:"proxy_address,omitempty"`
	RelayNodeID      domain.ID       `json:"relay_node_id,omitempty"`
}
type Materials struct {
	NodeID        domain.ID       `json:"node_id"`
	Revision      domain.Revision `json:"revision"`
	Token         string          `json:"token"`
	ExpiresAt     time.Time       `json:"expires_at"`
	ControllerURL string          `json:"controller_url"`
	CAPEM         string          `json:"ca_pem"`
	CAFingerprint string          `json:"ca_fingerprint"`
}
type Identity struct {
	NodeID         domain.ID `json:"node_id"`
	CertificatePEM string    `json:"certificate_pem"`
	CAPEM          string    `json:"ca_pem"`
}
type Status struct {
	NodeID      domain.ID  `json:"node_id"`
	State       string     `json:"state"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RelayNodeID *domain.ID `json:"relay_node_id"`
}

func New(repo *Repository, cipher secretstore.Cipher, guard adminauth.Guard, options Options) (*Service, error) {
	if repo == nil || repo.db == nil || cipher == nil || guard == nil {
		return nil, errors.New("enrollment dependencies unavailable")
	}
	u, e := url.Parse(options.ControllerURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || len(options.ServerNames) == 0 {
		return nil, problem(domain.InvalidArgument)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.CertificateLifetime == 0 {
		options.CertificateLifetime = 30 * 24 * time.Hour
	}
	if options.CertificateLifetime < time.Hour || options.CertificateLifetime > 90*24*time.Hour {
		return nil, problem(domain.InvalidArgument)
	}
	options.ControllerURL = strings.TrimRight(options.ControllerURL, "/")
	s := &Service{repo: repo, cipher: cipher, guard: guard, options: options}
	row, exists, e := repo.loadCA(options.Context)
	if e != nil {
		return nil, errors.Join(pki.ErrIdentity, e)
	}
	if !exists {
		ca, key, e := pki.GenerateCA(options.Now())
		if e != nil {
			return nil, e
		}
		defer clear(key)
		id, e := domain.NewID("cred")
		if e != nil {
			return nil, pki.ErrIdentity
		}
		sealed, e := cipher.Seal(options.Context, secretstore.Binding{Ref: id, Purpose: secretstore.ManagementCA}, key)
		if e != nil {
			return nil, e
		}
		if e = repo.saveCA(options.Context, ca.PEM, id, sealed, options.Now()); e != nil {
			return nil, errors.Join(pki.ErrIdentity, e)
		}
		row, exists, e = repo.loadCA(options.Context)
		if e != nil || !exists {
			return nil, errors.Join(pki.ErrIdentity, e)
		}
	}
	raw, e := cipher.Open(options.Context, secretstore.Binding{Ref: domain.ID(row.ID), Purpose: secretstore.ManagementCA}, secretstore.Sealed{KeyID: row.KeyID, Ciphertext: row.Ciphertext})
	if e != nil {
		return nil, e
	}
	defer clear(raw)
	s.ca, e = pki.LoadCA(row.Certificate, raw, options.Now())
	if e != nil {
		return nil, e
	}
	server, e := s.refreshServer()
	if e != nil {
		return nil, e
	}
	leaf, e := x509.ParseCertificate(server.Certificate[0])
	if e != nil || leaf.VerifyHostname(u.Hostname()) != nil {
		return nil, problem(domain.InvalidArgument)
	}
	return s, nil
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func requestHash(v any) string  { b, _ := json.Marshal(v); return hashBytes(b) }
func validKey(key string) bool  { return len(key) > 0 && len(key) <= 128 }
func validName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 128 && !strings.ContainsAny(name, "\x00\r\n")
}
func (s *Service) Issue(ctx context.Context, id domain.ID, req IssueRequest) (Materials, error) {
	var out Materials
	create := id == ""
	if !validKey(req.IdempotencyKey) || create && req.ExpectedRevision != 0 || !create && (!id.Valid() || !req.ExpectedRevision.Valid()) {
		return out, problem(domain.InvalidArgument)
	}
	if create {
		if !validName(req.Name) || (req.Mode != "direct" && req.Mode != "reverse_tunnel") || (req.Role != "node" && req.Role != "both") || req.ControlAddress == "" || len(req.ControlAddress) > 512 || len(req.ProxyAddress) > 512 || strings.ContainsAny(req.ControlAddress+req.ProxyAddress, "\x00\r\n@?#") {
			return out, problem(domain.InvalidArgument)
		}
		if req.Mode == "direct" && (req.ProxyAddress == "" || req.RelayNodeID != "") || req.Mode == "reverse_tunnel" && (req.ProxyAddress != "" || !req.RelayNodeID.Valid() || req.Role != "node") {
			return out, problem(domain.InvalidArgument)
		}
	} else if req.Name != "" || req.Mode != "" || req.Role != "" || req.ControlAddress != "" || req.ProxyAddress != "" || req.RelayNodeID != "" {
		return out, problem(domain.InvalidArgument)
	}
	if create && req.Mode == "reverse_tunnel" && (s.options.RelayAllowed == nil || !s.options.RelayAllowed(ctx, req.RelayNodeID)) {
		return out, problem(domain.NotImplemented)
	}
	hash := requestHash(req)
	if create {
		var e error
		id, e = domain.NewID("node")
		if e != nil {
			return out, e
		}
	}
	bytes := make([]byte, 32)
	if _, e := rand.Read(bytes); e != nil {
		return out, e
	}
	defer clear(bytes)
	token := base64.RawURLEncoding.EncodeToString(bytes)
	ref, e := domain.NewID("cred")
	if e != nil {
		return out, e
	}
	plaintext := []byte(token)
	defer clear(plaintext)
	sealed, e := s.cipher.Seal(ctx, secretstore.Binding{Ref: ref, Purpose: secretstore.Certificate}, plaintext)
	if e != nil {
		return out, e
	}
	ref, e = s.repo.issue(ctx, id, req, preparedToken{id: ref, hash: hashBytes(plaintext), sealed: sealed, expires: s.options.Now().Add(15 * time.Minute)}, hash, s.options.Now(), create)
	if e != nil {
		return out, e
	}
	row, e := s.repo.tokenByID(ctx, ref)
	if e != nil {
		return out, e
	}
	if row.RevokedAt.Valid {
		return out, problem(domain.TaskExpired)
	}
	n, e := s.repo.Get(ctx, domain.ID(row.NodeID))
	if e != nil {
		return out, e
	}
	raw, e := s.cipher.Open(ctx, secretstore.Binding{Ref: ref, Purpose: secretstore.Certificate}, secretstore.Sealed{KeyID: row.KeyID, Ciphertext: row.Ciphertext})
	if e != nil {
		return out, e
	}
	defer clear(raw)
	expires, e := time.Parse(time.RFC3339Nano, row.ExpiresAt)
	if e != nil {
		return out, e
	}
	return Materials{NodeID: n.ID, Revision: n.Revision, Token: string(raw), ExpiresAt: expires, ControllerURL: s.options.ControllerURL, CAPEM: string(s.ca.PEM), CAFingerprint: pki.Fingerprint(s.ca.Certificate)}, nil
}
func (s *Service) Enroll(ctx context.Context, token string, csrPEM []byte) (Identity, error) {
	var out Identity
	if len(token) != 43 {
		return out, problem(domain.Unauthenticated)
	}
	csr, e := pki.ParseCSR(csrPEM)
	if e != nil {
		return out, problem(domain.InvalidArgument)
	}
	hash := hashBytes([]byte(token))
	row, e := s.repo.token(ctx, hash)
	if e != nil {
		var p *domain.Problem
		if errors.As(e, &p) && p.Code == domain.NotFound {
			return out, problem(domain.Unauthenticated)
		}
		return out, e
	}
	if row.RevokedAt.Valid {
		return out, problem(domain.Forbidden)
	}
	expires, e := time.Parse(time.RFC3339Nano, row.ExpiresAt)
	if e != nil {
		return out, e
	}
	if !row.CsrHash.Valid && !s.options.Now().Before(expires) {
		return out, problem(domain.TaskExpired)
	}
	if row.CsrHash.Valid && row.CsrHash.String != pki.CSRHash(csr) {
		return out, problem(domain.IdempotencyConflict)
	}
	cert := row.Certificate
	if !row.CsrHash.Valid {
		cert, e = s.ca.SignNode(csrPEM, domain.ID(row.NodeID), s.options.Now(), s.options.CertificateLifetime)
		if e != nil {
			return out, e
		}
	}
	var changed bool
	out, changed, e = s.repo.consume(ctx, hash, pki.CSRHash(csr), cert, s.options.Now)
	if e != nil {
		return Identity{}, e
	}
	out.CAPEM = string(s.ca.PEM)
	if changed && s.options.OnIdentityChange != nil {
		s.options.OnIdentityChange(out.NodeID)
	}
	return out, nil
}
func (s *Service) Get(ctx context.Context, id domain.ID) (domain.Node, error) {
	n, e := s.repo.Get(ctx, id)
	s.connection(&n)
	return n, e
}
func (s *Service) List(ctx context.Context, q domain.ListQuery) (domain.Page[domain.Node], error) {
	page, e := s.repo.List(ctx, q)
	for i := range page.Items {
		s.connection(&page.Items[i])
	}
	return page, e
}
func (s *Service) Rename(ctx context.Context, id domain.ID, expected domain.Revision, key, name string) (domain.Node, error) {
	if !id.Valid() || !expected.Valid() || !validKey(key) || !validName(name) {
		return domain.Node{}, problem(domain.InvalidArgument)
	}
	_, e := s.repo.mutate(ctx, id, expected, key, name, false, requestHash([]any{expected, name}), s.options.Now())
	if e != nil {
		return domain.Node{}, e
	}
	return s.Get(ctx, id)
}

// Remove revokes management identity immediately. B04 must separately schedule
// proxy/tunnel cleanup; no Applied/Verified or fictional task is produced here.
func (s *Service) Remove(ctx context.Context, id domain.ID, expected domain.Revision, key string) (domain.Revision, error) {
	if !id.Valid() || !expected.Valid() || !validKey(key) {
		return 0, problem(domain.InvalidArgument)
	}
	v, e := s.repo.mutate(ctx, id, expected, key, "", true, requestHash([]any{id, expected, "management_revoked", "cleanup_pending"}), s.options.Now())
	if e == nil && s.options.OnIdentityChange != nil {
		s.options.OnIdentityChange(id)
	}
	return v, e
}
func (s *Service) CAPEM() []byte { return append([]byte(nil), s.ca.PEM...) }
func (s *Service) refreshServer() (*tls.Certificate, error) {
	s.tlsMu.Lock()
	defer s.tlsMu.Unlock()
	if s.serverExpiry.Sub(s.options.Now()) < 6*time.Hour {
		cert, e := s.ca.ServerCertificate(s.options.ServerNames, s.options.Now())
		if e != nil {
			return nil, e
		}
		parsed, e := x509.ParseCertificate(cert.Certificate[0])
		if e != nil {
			return nil, pki.ErrIdentity
		}
		s.serverCert = cert
		s.serverExpiry = parsed.NotAfter
	}
	copy := s.serverCert
	return &copy, nil
}

// VerifyClientCertIfGiven allows only the enrollment route to omit a certificate.
// Every other route must call Authenticate. HTTP/2 is disabled for WebSocket v1.
func (s *Service) TLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(s.ca.Certificate)
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool, NextProtos: []string{"http/1.1"}, Time: s.options.Now, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.refreshServer() }, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := s.Authenticate(ctx, cs.PeerCertificates[0])
		return e
	}}
}

func (s *Service) connection(n *domain.Node) {
	if s.options.ControlConnected != nil {
		n.Control = domain.Offline
		if s.options.ControlConnected(n.ID) {
			n.Control = domain.Online
		}
	}
}
