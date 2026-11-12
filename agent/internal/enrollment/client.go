package enrollment

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
)

var ErrEnrollment = errors.New("management enrollment failed")

type Options struct {
	ControllerURL string
	CAPEM         []byte
	CAFingerprint string
	NodeID        domain.ID
	Now           func() time.Time
}
type Client struct {
	identity *Identity
	o        Options
	trust    *tls.Config
}

func New(identity *Identity, o Options) (*Client, error) {
	if identity == nil || !o.NodeID.Valid() {
		return nil, ErrEnrollment
	}
	u, e := url.Parse(o.ControllerURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, ErrEnrollment
	}
	config, e := pki.Trust(o.CAPEM, o.CAFingerprint, u.Hostname())
	if e != nil {
		return nil, ErrEnrollment
	}
	o.CAPEM = append([]byte(nil), o.CAPEM...)
	if o.Now == nil {
		o.Now = time.Now
	}
	config.Time = o.Now
	o.ControllerURL = strings.TrimRight(o.ControllerURL, "/")
	identity.mu.Lock()
	defer identity.mu.Unlock()
	if identity.data.NodeID != "" && identity.data.NodeID != o.NodeID || len(identity.data.CAPEM) > 0 && !bytes.Equal(identity.data.CAPEM, o.CAPEM) {
		return nil, ErrEnrollment
	}
	return &Client{identity: identity, o: o, trust: config}, nil
}

// Enroll reuses the durable key/CSR after a timeout or lost committed response.
// net/http completes verified TLS (chain AND SAN) before writing the JSON token.
func (c *Client) Enroll(ctx context.Context, token string) error {
	if len(token) != 43 {
		return ErrEnrollment
	}
	return c.exchange(ctx, "/enroll/v1", token, false)
}
func (c *Client) Renew(ctx context.Context) error {
	return c.exchange(ctx, "/enroll/v1/renew", "", true)
}
func (c *Client) exchange(ctx context.Context, path, token string, renew bool) error {
	c.identity.mu.Lock()
	defer c.identity.mu.Unlock()
	config := c.trust.Clone()
	if renew {
		pair, e := pki.ClientCertificate(c.identity.data.CertificatePEM, c.identity.data.KeyDER, c.identity.data.CAPEM, c.o.NodeID, c.o.Now())
		if e != nil {
			return ErrEnrollment
		}
		config.Certificates = []tls.Certificate{pair}
	}
	reqData := map[string]string{"csr_pem": string(c.identity.data.CSRPEM)}
	if !renew {
		reqData["token"] = token
	}
	raw, e := json.Marshal(reqData)
	if e != nil {
		return ErrEnrollment
	}
	defer clear(raw)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.o.ControllerURL+path, bytes.NewReader(raw))
	if e != nil {
		return ErrEnrollment
	}
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{TLSClientConfig: config, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return ErrEnrollment
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrEnrollment
	}
	var identity struct {
		NodeID         domain.ID `json:"node_id"`
		CertificatePEM string    `json:"certificate_pem"`
		CAPEM          string    `json:"ca_pem"`
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 16<<10+1))
	if e != nil || len(b) > 16<<10 {
		return ErrEnrollment
	}
	defer clear(b)
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&identity) != nil || d.Decode(new(any)) != io.EOF || identity.NodeID != c.o.NodeID || !bytes.Equal([]byte(identity.CAPEM), c.o.CAPEM) {
		return ErrEnrollment
	}
	if _, e = pki.ClientCertificate([]byte(identity.CertificatePEM), c.identity.data.KeyDER, c.o.CAPEM, c.o.NodeID, c.o.Now()); e != nil {
		return ErrEnrollment
	}
	updated := c.identity.data
	updated.NodeID = identity.NodeID
	updated.CAPEM = append([]byte(nil), c.o.CAPEM...)
	updated.CertificatePEM = []byte(identity.CertificatePEM)
	return c.identity.save(updated)
}
func (c *Client) TLSConfig() (*tls.Config, error) {
	config := c.trust.Clone()
	pair, e := c.identity.Certificate(c.o.Now())
	if e != nil {
		return nil, e
	}
	config.Certificates = []tls.Certificate{pair}
	return config, nil
}

// RenewIfNeeded is also suitable for control.Options.BeforeConnect. An expired
// identity cannot renew: the administrator must explicitly issue a new token.
func (c *Client) RenewIfNeeded(ctx context.Context, before time.Duration) (bool, error) {
	if before <= 0 {
		return false, ErrEnrollment
	}
	if !c.identity.NeedsRenewal(c.o.Now(), before) {
		return false, nil
	}
	if e := c.Renew(ctx); e != nil {
		return false, e
	}
	return true, nil
}

// Maintain renews even while the control socket remains continuously connected.
// Temporary failures are reported as stable errors and retried on the next tick.
// The caller cancels this with the Agent lifecycle and may reconnect the control
// client after a successful renewal to immediately adopt the new certificate.
func (c *Client) Maintain(ctx context.Context, interval, before time.Duration, onRenew func(), onFailure func(error)) error {
	if interval <= 0 || before <= interval {
		return ErrEnrollment
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		changed, e := c.RenewIfNeeded(ctx, before)
		if e != nil && onFailure != nil {
			onFailure(ErrEnrollment)
		}
		if changed && onRenew != nil {
			onRenew()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
