package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicccce/meshstrata/internal/adminauth"
	"github.com/nicccce/meshstrata/internal/domain"
)

// Bootstrap is supplied ONLY from a trusted local provisioning source. SHA256
// hashes a canonical base64url token with 32 random bytes, not a human password.
// Nil disables HTTP initialization, even when the database has no administrator.
type Bootstrap struct {
	SHA256    []byte
	ExpiresAt time.Time
}
type Options struct {
	Clock          func() time.Time
	Random         io.Reader
	Bootstrap      *Bootstrap
	AllowedOrigins []string
	CookieName     string // Contract default or __Host- name; Secure/HttpOnly/Strict, Path=/, no Domain.
	SessionTTL     time.Duration
	AttemptWindow  time.Duration
	MaxAttempts    int64 // fixed server-wide per action, including successful attempts
}
type Service struct {
	repo     *Repository
	options  Options
	origins  map[string]bool
	randomMu sync.Mutex
	kdf      chan struct{}
	dummy    string
}

var _ adminauth.Guard = (*Service)(nil)

func New(repo *Repository, options Options) (*Service, error) {
	if repo == nil || nilDependency(repo.db) {
		return nil, errors.New("auth repository unavailable")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if nilDependency(options.Random) {
		return nil, errors.New("auth randomness unavailable")
	}
	if options.CookieName == "" {
		options.CookieName = "meshstrata_session"
	}
	if (options.CookieName != "meshstrata_session" && !strings.HasPrefix(options.CookieName, "__Host-")) || (&http.Cookie{Name: options.CookieName, Value: "test"}).Valid() != nil {
		return nil, errors.New("invalid auth options")
	}

	if options.SessionTTL == 0 {
		options.SessionTTL = 12 * time.Hour
	}
	if options.AttemptWindow == 0 {
		options.AttemptWindow = time.Minute
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 5
	}
	if options.SessionTTL < 5*time.Minute || options.SessionTTL > 24*time.Hour || options.AttemptWindow < 10*time.Second || options.AttemptWindow > time.Hour || options.MaxAttempts < 1 || options.MaxAttempts > 20 {
		return nil, errors.New("invalid auth options")
	}
	s := &Service{repo: repo, options: options, origins: map[string]bool{}, kdf: make(chan struct{}, 1)}
	for _, origin := range options.AllowedOrigins {
		u, e := url.Parse(origin)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != origin {
			return nil, errors.New("invalid auth origin")
		}
		s.origins[origin] = true
	}
	if len(s.origins) == 0 {
		return nil, errors.New("auth origin required")
	}
	if b := options.Bootstrap; b != nil {
		if len(b.SHA256) != sha256.Size || b.ExpiresAt.IsZero() || !b.ExpiresAt.After(options.Clock()) {
			return nil, errors.New("invalid auth bootstrap")
		}
		copied := *b
		copied.SHA256 = append([]byte(nil), b.SHA256...)
		s.options.Bootstrap = &copied
	}
	var err error
	s.dummy, err = hashPassword(s.options.Random, "dummy administrator password")
	if err != nil {
		return nil, errors.New("auth randomness unavailable")
	}
	return s, nil
}
func (s *Service) token() (string, error) {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	b := make([]byte, 32)
	defer clear(b)
	if _, e := io.ReadFull(s.options.Random, b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func tokenHash(token string) []byte { hash := sha256.Sum256([]byte(token)); return hash[:] }
func validToken(token string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(token)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == token
}
func csrf(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("meshstrata.auth.csrf.v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func oneHeader(r *http.Request, name string) string {
	values := r.Header.Values(name)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
func (s *Service) origin(r *http.Request) bool {
	return s.origins[oneHeader(r, "Origin")] && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func (s *Service) readOrigin(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Site") != "cross-site" && (len(r.Header.Values("Origin")) == 0 || s.origin(r))
}
func (s *Service) cookieToken(r *http.Request) string {
	var token string
	count := 0
	for _, c := range r.Cookies() {
		if c.Name == s.options.CookieName {
			token = c.Value
			count++
		}
	}
	if count != 1 || !validToken(token) {
		return ""
	}
	return token
}
func (s *Service) authenticate(r *http.Request) (session, error) {
	token := s.cookieToken(r)
	if token == "" {
		return session{}, ErrUnauthenticated
	}
	ss, err := s.repo.session(r.Context(), tokenHash(token), s.options.Clock())
	ss.token = token
	return ss, err
}

type sessionKey struct{}

func (s *Service) Read(next http.Handler) http.Handler  { return s.guard(next, false) }
func (s *Service) Write(next http.Handler) http.Handler { return s.guard(next, true) }
func (s *Service) guard(next http.Handler, write bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sensitive(w)
		ss, err := s.authenticate(r)
		if err != nil {
			fail(w, err)
			return
		}
		if !s.readOrigin(r) || (write && (!s.origin(r) || subtle.ConstantTimeCompare([]byte(oneHeader(r, "X-CSRF-Token")), []byte(csrf(ss.token))) != 1)) {
			fail(w, ErrForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, ss)))
	})
}
func sensitive(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func fail(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, domain.ErrorCode("INTERNAL_ERROR")
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, code = 401, domain.Unauthenticated
	case errors.Is(err, ErrForbidden):
		status, code = 403, domain.Forbidden
	case errors.Is(err, ErrInvalid):
		status, code = 400, domain.InvalidArgument
	case errors.Is(err, ErrConflict):
		status, code = 409, domain.RevisionConflict
	case errors.Is(err, ErrRateLimited):
		status, code = 429, "RATE_LIMITED"
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", "60")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(domain.ErrorResponse{Error: domain.Problem{Code: code}})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return ErrInvalid
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if e = dec.Decode(dst); e != nil {
		return ErrInvalid
	}
	if e = dec.Decode(new(any)); e != io.EOF {
		return ErrInvalid
	}
	return nil
}
func validUsername(username string) bool {
	if len(username) < 1 || len(username) > 64 {
		return false
	}
	for _, c := range username {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 1024 }
func (s *Service) acquire(w http.ResponseWriter, r *http.Request, bucket string) bool {
	if err := s.repo.attempt(r.Context(), bucket, s.options.Clock(), s.options.AttemptWindow, s.options.MaxAttempts); err != nil {
		if errors.Is(err, ErrRateLimited) {
			w.Header().Set("Retry-After", strconv.FormatInt(int64((s.options.AttemptWindow+time.Second-1)/time.Second), 10))
		}
		fail(w, err)
		return false
	}
	select {
	case s.kdf <- struct{}{}:
		return true
	default:
		fail(w, ErrRateLimited)
		return false
	}
}
func (s *Service) hash(password string) (string, error) {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	return hashPassword(s.options.Random, password)
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/initialize", s.initialize)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("GET /api/v1/auth/session", s.Read(http.HandlerFunc(s.getSession)))
	mux.Handle("POST /api/v1/auth/logout", s.Write(http.HandlerFunc(s.logout)))
	mux.Handle("POST /api/v1/auth/password", s.Write(http.HandlerFunc(s.password)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sensitive(w); mux.ServeHTTP(w, r) })
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) initialize(w http.ResponseWriter, r *http.Request) {
	if !s.origin(r) {
		fail(w, ErrForbidden)
		return
	}
	if !s.acquire(w, r, "initialize") {
		return
	}
	defer func() { <-s.kdf }()
	proof := oneHeader(r, "X-Meshstrata-Bootstrap")
	b := s.options.Bootstrap
	if b == nil || !s.options.Clock().Before(b.ExpiresAt) || !validToken(proof) || subtle.ConstantTimeCompare(tokenHash(proof), b.SHA256) != 1 {
		fail(w, ErrForbidden)
		return
	}
	var in loginInput
	if err := decode(w, r, &in); err != nil || !validUsername(in.Username) || !validPassword(in.Password) {
		fail(w, ErrInvalid)
		return
	}
	hash, err := s.hash(in.Password)
	if err == nil && !s.options.Clock().Before(b.ExpiresAt) {
		err = ErrForbidden
	}
	if err == nil {
		err = s.repo.initialize(r.Context(), in.Username, hash, s.options.Clock())
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	if !s.origin(r) {
		fail(w, ErrForbidden)
		return
	}
	if !s.acquire(w, r, "login") {
		return
	}
	defer func() { <-s.kdf }()
	var in loginInput
	if err := decode(w, r, &in); err != nil || len(in.Username) > 64 || len(in.Password) > 1024 {
		fail(w, ErrInvalid)
		return
	}
	a, err := s.repo.administrator(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, err)
		return
	}
	encoded := a.password
	if errors.Is(err, sql.ErrNoRows) {
		encoded = s.dummy
	}
	ok := checkPassword(encoded, in.Password)
	name := sha256.Sum256([]byte(in.Username))
	expected := sha256.Sum256([]byte(a.username))
	if !ok || subtle.ConstantTimeCompare(name[:], expected[:]) != 1 || errors.Is(err, sql.ErrNoRows) {
		fail(w, ErrUnauthenticated)
		return
	}
	token, err := s.token()
	now := s.options.Clock()
	expiry := now.Add(s.options.SessionTTL)
	if err == nil {
		err = s.repo.login(r.Context(), a, tokenHash(token), tokenHash(s.cookieToken(r)), now, expiry)
	}
	if err != nil {
		fail(w, err)
		return
	}
	s.setCookie(w, token, expiry, int(s.options.SessionTTL.Seconds()))
	w.WriteHeader(204)
}
func (s *Service) setCookie(w http.ResponseWriter, token string, expiry time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: s.options.CookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expiry.UTC(), MaxAge: maxAge})
}
func (s *Service) getSession(w http.ResponseWriter, r *http.Request) {
	ss := r.Context().Value(sessionKey{}).(session)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Username  string    `json:"username"`
		ExpiresAt time.Time `json:"expires_at"`
		CSRFToken string    `json:"csrf_token"`
	}{ss.username, ss.expires, csrf(ss.token)})
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	ss := r.Context().Value(sessionKey{}).(session)
	if err := s.repo.logout(r.Context(), ss); err != nil {
		fail(w, err)
		return
	}
	s.setCookie(w, "", time.Unix(1, 0), -1)
	w.WriteHeader(204)
}
func (s *Service) password(w http.ResponseWriter, r *http.Request) {
	if !s.acquire(w, r, "password") {
		return
	}
	defer func() { <-s.kdf }()
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decode(w, r, &in); err != nil || len(in.CurrentPassword) > 1024 || !validPassword(in.NewPassword) {
		fail(w, ErrInvalid)
		return
	}
	a, err := s.repo.administrator(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if !checkPassword(a.password, in.CurrentPassword) {
		fail(w, ErrUnauthenticated)
		return
	}
	hash, err := s.hash(in.NewPassword)
	if err == nil {
		err = s.repo.changePassword(r.Context(), r.Context().Value(sessionKey{}).(session), a, hash, s.options.Clock())
	}
	if err != nil {
		fail(w, err)
		return
	}
	s.setCookie(w, "", time.Unix(1, 0), -1)
	w.WriteHeader(204)
}

// Interfaces can contain nil pointers; reject those before exposing a handler.
func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	}
	return false
}
