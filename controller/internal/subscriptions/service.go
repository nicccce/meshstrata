// Package subscriptions owns the persistent subscription, ingress authorization
// and download capability. Its stage3 views are private Go contracts.
package subscriptions

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const (
	adminScope                     = "single_administrator"
	timeLayout                     = "2006-01-02T15:04:05.000000000Z"
	releaseWindow                  = 15 * time.Minute
	unavailable   domain.ErrorCode = "SUBSCRIPTION_UNAVAILABLE"
)

type Service struct {
	o stage3.SubscriptionsOptions
	// Runtime may provide a reader or an ID generator without synchronization.
	randomMu sync.Mutex
	idMu     sync.Mutex
}

var _ stage3.Subscriptions = (*Service)(nil)

func NewService(o stage3.SubscriptionsOptions) (*Service, error) {
	for _, d := range []any{o.DB, o.Guard, o.Cipher, o.Routes, o.Rules, o.Client, o.Publisher} {
		if nilValue(d) {
			return nil, invalid("")
		}
	}
	if !httpsURL(o.PublicOrigin, false) {
		return nil, invalid("/public_origin")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = domain.NewID
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	if nilValue(o.Random) {
		return nil, invalid("")
	}
	return &Service{o: o}, nil
}

func nilValue(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

func httpsURL(raw string, numeric bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "#\\%") {
		return false
	}
	host := u.Hostname()
	if !numeric && u.Path != "" || numeric && (len(raw) > 512 || strings.ContainsAny(u.Path, ", \t\r\n") || strings.ContainsFunc(u.Path, unicode.IsControl)) {
		return false
	}
	if !ipValid(host) && (numeric || !control.DomainName(host)) {
		return false
	}
	// Parse rejects malformed numeric ports; also disallow empty or out-of-range ports.
	if u.Port() != "" {
		p, err := strconv.ParseUint(u.Port(), 10, 16)
		return err == nil && p > 0
	}
	return !strings.HasSuffix(u.Host, ":")
}

func ipValid(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Zone() == "" && !a.Is4In6() && !a.IsUnspecified() && !a.IsMulticast() && a.String() == s
}

func problem(c domain.ErrorCode) error { return &domain.Problem{Code: c} }
func invalid(path string) error {
	p := &domain.Problem{Code: domain.InvalidArgument}
	if path != "" {
		p.Fields = []domain.FieldError{{Path: path, Code: domain.InvalidArgument}}
	}
	return p
}
func conflict(r int64) error {
	rev := domain.Revision(r)
	return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &rev}
}

func safe(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return problem(domain.NotFound)
	}
	// Never forward dependency-provided fields, messages or wrapped errors.
	var p *domain.Problem
	if errors.As(err, &p) {
		switch p.Code {
		case domain.InvalidArgument, domain.NotFound, domain.RevisionConflict, domain.IdempotencyConflict,
			domain.PortInUse, domain.DependencyInUse, domain.GrantOriginUncertain, domain.TaskExpired, domain.NotImplemented:
			return problem(p.Code)
		}
	}
	return problem(unavailable)
}

func localError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p, ok := err.(*domain.Problem); ok {
		return p // constructed by this package, contains only allowlisted fields
	}
	return safe(ctx, err)
}

func stamp(t time.Time) string          { return t.UTC().Format(timeLayout) }
func parse(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }
func validTime(t time.Time) bool        { return !t.IsZero() && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999 }

func eligible(row dbgen.Subscription, now time.Time) bool {
	exp, err := parse(row.ExpiresAt)
	return err == nil && exp.After(now) && !row.RevokedAt.Valid
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func revision(r int64) sql.NullInt64   { return sql.NullInt64{Int64: r, Valid: r > 0} }
func stringTime(t sql.NullString) (*time.Time, error) {
	if !t.Valid {
		return nil, nil
	}
	p, err := parse(t.String)
	return &p, err
}

func (s *Service) id(kind string) (domain.ID, error) {
	s.idMu.Lock()
	id, err := s.o.NewID(kind)
	s.idMu.Unlock()
	if err != nil || !id.Valid() {
		return "", problem(unavailable)
	}
	return id, nil
}

// Tick is independent of the business transaction so expiry still advances
// during failed/denied requests. Each following transaction re-reads the floor.
func (s *Service) tick(ctx context.Context) error {
	t := s.o.Now().UTC()
	if !validTime(t) {
		return problem(unavailable)
	}
	return s.o.DB.Write(ctx, func(q *dbgen.Queries) error { return q.B08AdvanceClock(ctx, stamp(t)) })
}
func dbNow(ctx context.Context, q *dbgen.Queries) (time.Time, error) {
	t, err := q.B08Clock(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return parse(t)
}

func checkMutation(m domain.Mutation, create bool) error {
	if create && m.ExpectedRevision != 0 || !create && (!m.ExpectedRevision.Valid() || m.ExpectedRevision >= domain.MaxRevision) {
		return invalid("/expected_revision")
	}
	if len(m.IdempotencyKey) < 1 || len(m.IdempotencyKey) > 128 || strings.IndexFunc(m.IdempotencyKey, func(r rune) bool {
		return r < '!' || r > '~'
	}) != -1 {
		return invalid("/idempotency_key")
	}
	return nil
}

type request struct{ op, key, digest string }

func makeRequest(op string, m domain.Mutation, payload any) (request, error) {
	d, err := control.ComponentDigest(struct {
		Expected domain.Revision
		Input    any
	}{m.ExpectedRevision, payload})
	return request{op, m.IdempotencyKey, d}, err
}

func (r request) replay(ctx context.Context, q *dbgen.Queries) (v stage3.SubscriptionView, ok bool, err error) {
	old, err := q.B08Request(ctx, dbgen.B08RequestParams{Scope: adminScope, Operation: r.op, IdempotencyKey: r.key})
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if r.digest != old.Digest {
		return v, false, problem(domain.IdempotencyConflict)
	}
	err = json.Unmarshal([]byte(old.ResultJson), &v)
	return v, err == nil, err
}

func (s *Service) replay(ctx context.Context, r request) (v stage3.SubscriptionView, ok bool, err error) {
	err = s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		v, ok, e = r.replay(ctx, q)
		return e
	})
	return v, ok, localError(ctx, err)
}

func (s *Service) mutate(ctx context.Context, r request, fn func(*dbgen.Queries, time.Time) (stage3.SubscriptionView, error)) (v stage3.SubscriptionView, err error) {
	if err := s.tick(ctx); err != nil {
		return v, localError(ctx, err)
	}
	err = s.o.DB.Write(ctx, func(q *dbgen.Queries) error {
		old, found, err := r.replay(ctx, q)
		if err != nil || found {
			v = old
			return err
		}
		now, err := dbNow(ctx, q)
		if err != nil {
			return err
		}
		v, err = fn(q, now)
		if err != nil {
			return err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return q.B08RecordRequest(ctx, dbgen.B08RecordRequestParams{Scope: adminScope, Operation: r.op,
			IdempotencyKey: r.key, Digest: r.digest, ResultJson: string(b)})
	})
	if err = localError(ctx, err); err != nil {
		return stage3.SubscriptionView{}, err
	}
	return v, nil
}

func requireCAS(row dbgen.Subscription, m domain.Mutation) error {
	if row.RevokedAt.Valid {
		return problem(domain.NotFound)
	}
	if row.Revision != int64(m.ExpectedRevision) {
		return conflict(row.Revision)
	}
	return nil
}

func affected(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return problem(domain.RevisionConflict)
	}
	return nil
}

func jsonValue(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
func clone[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

func rowRoute(row dbgen.B08Intent) (r stage3.PublishedRoute, err error) {
	err = json.Unmarshal([]byte(row.RouteJson), &r)
	if err != nil {
		return r, err
	}
	digest, err := stage3.RouteDigest(r)
	want := control.Pin{ID: domain.ID(row.RouteID), Revision: domain.Revision(row.RouteRevision), Digest: row.RouteDigest}
	if err != nil || !want.Valid() || r.Pin != want || r.Pin.Digest != digest {
		return r, problem(unavailable)
	}
	return r, nil
}

func validDraft(d stage3.SubscriptionDraft) error {
	if strings.TrimSpace(d.Name) != d.Name || d.Name == "" || len(d.Name) > 200 || !utf8.ValidString(d.Name) ||
		strings.ContainsFunc(d.Name, unicode.IsControl) {
		return invalid("/name")
	}
	if !validTime(d.ExpiresAt) {
		return invalid("/expires_at")
	}
	if !d.Profile.Valid() {
		return invalid("/profile")
	}
	if len(d.Routes) < 1 || len(d.Routes) > stage3.MaxSubscriptionEntries {
		return invalid("/routes")
	}
	seen := map[domain.ID]bool{}
	for i, p := range d.Routes {
		if !p.Valid() || seen[p.ID] {
			return invalid("/routes/" + strconv.Itoa(i))
		}
		seen[p.ID] = true
	}
	return validPreferences(d.Preferences)
}

func validPreferences(p stage3.ClientPreferences) error {
	if p.Mode != "tun" && p.Mode != "system_proxy" {
		return invalid("/preferences/mode")
	}
	// Until I03 has capability evidence, only the IPv4/default-deny client is
	// accepted. A successful route digest alone is no evidence of UDP/IPv6.
	if p.IPv6 || p.BusinessUDP {
		return invalid("/preferences")
	}
	dns := p.DNS
	for _, list := range []struct {
		values []string
		path   string
		valid  func(string) bool
	}{
		{dns.Bootstrap, "/preferences/dns/bootstrap", ipValid},
		{dns.ProxyServers, "/preferences/dns/proxy_servers", func(s string) bool { return ipValid(s) || httpsURL(s, true) }},
		{dns.Business, "/preferences/dns/business", func(s string) bool { return httpsURL(s, true) }},
	} {
		seen := map[string]bool{}
		if len(list.values) == 0 || len(list.values) > 16 {
			return invalid(list.path)
		}
		for _, v := range list.values {
			if !list.valid(v) || seen[v] {
				return invalid(list.path)
			}
			seen[v] = true
		}
	}
	if len(p.Selections) > stage3.MaxSubscriptionEntries+1 {
		return invalid("/preferences/selections")
	}
	for _, v := range p.Selections {
		if !v.SelectedEntryID.Valid() || len(v.Name) > 160 || v.Name == "" || strings.ContainsFunc(v.Name, unicode.IsControl) {
			return invalid("/preferences/selections")
		}
	}
	return nil
}

func strictJSON[T any](raw []byte, v *T) bool {
	// We generated the envelope ourselves; require its exact canonical form.
	// This rejects extra keys, duplicate keys, omitted fields and trailing data.
	if err := json.Unmarshal(raw, v); err != nil {
		return false
	}
	encoded, err := json.Marshal(v)
	defer clear(encoded)
	return err == nil && bytes.Equal(raw, encoded)
}
