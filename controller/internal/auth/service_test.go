package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
)

const testOrigin = "https://controller.example:8443"
const testPassword = "correct horse battery staple"

type harness struct {
	service *Service
	store   *storage.Store
	now     atomic.Int64
	proof   string
	dir     string
}

func newHarness(t *testing.T, max int64) *harness {
	t.Helper()
	h := &harness{dir: t.TempDir()}
	h.now.Store(1800000000)
	var err error
	h.store, err = storage.Open(context.Background(), h.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.store.Close() })
	h.proof = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	digest := sha256.Sum256([]byte(h.proof))
	h.service, err = New(NewRepository(h.store), Options{Clock: func() time.Time { return time.Unix(h.now.Load(), 0) }, AllowedOrigins: []string{testOrigin}, Bootstrap: &Bootstrap{SHA256: digest[:], ExpiresAt: time.Unix(h.now.Load()+3600, 0)}, MaxAttempts: max})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func request(handler http.Handler, method, path, body, origin string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func (h *harness) initialize(t *testing.T) {
	t.Helper()
	w := request(h.service.Handler(), "POST", "/api/v1/auth/initialize", `{"username":"admin","password":"`+testPassword+`"}`, testOrigin, nil, map[string]string{"X-Meshstrata-Bootstrap": h.proof})
	if w.Code != 204 {
		t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
	}
}
func (h *harness) login(t *testing.T, password string, prior *http.Cookie) (*http.Cookie, string) {
	t.Helper()
	w := request(h.service.Handler(), "POST", "/api/v1/auth/login", `{"username":"admin","password":"`+password+`"}`, testOrigin, prior, nil)
	if w.Code != 204 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("cookie missing")
	}
	c := cookies[0]
	if c.Name != "meshstrata_session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 43200 {
		t.Fatalf("unsafe cookie: %+v", c)
	}
	w = request(h.service.Handler(), "GET", "/api/v1/auth/session", "", "", c, nil)
	if w.Code != 200 {
		t.Fatalf("session: %d", w.Code)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &session); e != nil || session.CSRF == "" {
		t.Fatal("csrf missing")
	}
	if strings.Contains(w.Body.String(), c.Value) {
		t.Fatal("session token leaked")
	}
	return c, session.CSRF
}
func TestLifecycleGuardsAndPersistence(t *testing.T) {
	h := newHarness(t, 20)
	h.initialize(t)
	a, csrfA := h.login(t, testPassword, nil)
	b, csrfB := h.login(t, testPassword, a)
	if a.Value == b.Value {
		t.Fatal("session fixation")
	}
	w := request(h.service.Handler(), "GET", "/api/v1/auth/session", "", "", a, nil)
	if w.Code != 401 {
		t.Fatal("prior session survived login")
	}
	if e := h.store.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	h.store, e = storage.Open(context.Background(), h.dir)
	if e != nil {
		t.Fatal(e)
	}
	h.service.repo = NewRepository(h.store)
	w = request(h.service.Handler(), "GET", "/api/v1/auth/session", "", "", b, nil)
	if w.Code != 200 {
		t.Fatal("session not persistent")
	}
	called := 0
	business := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) })
	for _, tc := range []struct {
		cookie       *http.Cookie
		origin, csrf string
		want         int
	}{
		{nil, testOrigin, csrfB, 401}, {b, "", csrfB, 403}, {b, "https://evil.example", csrfB, 403}, {b, testOrigin, "", 403}, {b, testOrigin, csrfA, 403}, {b, testOrigin, csrfB, 204},
	} {
		before := called
		w = request(h.service.Write(business), "POST", "/business", "", tc.origin, tc.cookie, map[string]string{"X-CSRF-Token": tc.csrf})
		if w.Code != tc.want {
			t.Fatalf("guard: got %d want %d", w.Code, tc.want)
		}
		if tc.want != 204 && called != before {
			t.Fatal("unauthorized handler ran")
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("sensitive headers missing")
		}
	}
	// Read guards need a session but no CSRF/Origin. Hostile explicit Origin is denied.
	w = request(h.service.Read(business), "GET", "/business", "", "", b, nil)
	if w.Code != 204 {
		t.Fatal("read blocked")
	}
	w = request(h.service.Read(business), "GET", "/business", "", "https://evil.example", b, nil)
	if w.Code != 403 {
		t.Fatal("hostile read origin accepted")
	}
	other, _ := h.login(t, testPassword, nil)
	w = request(h.service.Handler(), "POST", "/api/v1/auth/password", `{"current_password":"wrong","new_password":"another strong password"}`, testOrigin, b, map[string]string{"X-CSRF-Token": csrfB})
	if w.Code != 401 {
		t.Fatal("wrong current password accepted")
	}
	w = request(h.service.Handler(), "POST", "/api/v1/auth/password", `{"current_password":"`+testPassword+`","new_password":"another strong password"}`, testOrigin, b, map[string]string{"X-CSRF-Token": csrfB})
	if w.Code != 204 {
		t.Fatalf("password: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []*http.Cookie{b, other} {
		w = request(h.service.Handler(), "GET", "/api/v1/auth/session", "", "", c, nil)
		if w.Code != 401 {
			t.Fatal("password change did not invalidate all sessions")
		}
	}
	c, csrfC := h.login(t, "another strong password", nil)
	w = request(h.service.Handler(), "POST", "/api/v1/auth/logout", "", testOrigin, c, map[string]string{"X-CSRF-Token": csrfC})
	if w.Code != 204 {
		t.Fatal("logout failed")
	}
	if w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout cookie not cleared")
	}
	w = request(h.service.Handler(), "GET", "/api/v1/auth/session", "", "", c, nil)
	if w.Code != 401 {
		t.Fatal("logout session survived")
	}
	c, _ = h.login(t, "another strong password", nil)
	h.now.Add(43200)
	w = request(h.service.Handler(), "GET", "/api/v1/auth/session", "", "", c, nil)
	if w.Code != 401 {
		t.Fatal("expiry boundary accepted")
	}
}
func TestBootstrapOriginAndRateLimit(t *testing.T) {
	h := newHarness(t, 5)
	body := `{"username":"admin","password":"` + testPassword + `"}`
	for _, tc := range []struct{ origin, proof string }{{"", h.proof}, {"https://evil.example", h.proof}, {testOrigin, "wrong"}, {testOrigin, ""}} {
		w := request(h.service.Handler(), "POST", "/api/v1/auth/initialize", body, tc.origin, nil, map[string]string{"X-Meshstrata-Bootstrap": tc.proof})
		if w.Code != 403 {
			t.Fatalf("bad bootstrap accepted: %d", w.Code)
		}
	}
	h.initialize(t)
	w := request(h.service.Handler(), "POST", "/api/v1/auth/initialize", body, testOrigin, nil, map[string]string{"X-Meshstrata-Bootstrap": h.proof})
	if w.Code != 409 {
		t.Fatalf("reinitialize: %d", w.Code)
	}
	for i := 0; i < 5; i++ {
		user := "admin"
		if i%2 == 1 {
			user = "missing"
		}
		w = request(h.service.Handler(), "POST", "/api/v1/auth/login", `{"username":"`+user+`","password":"wrong"}`, testOrigin, nil, nil)
		if w.Code != 401 || w.Body.String() != `{"error":{"code":"UNAUTHENTICATED"}}`+"\n" {
			t.Fatalf("login disclosure: %d %s", w.Code, w.Body.String())
		}
	}
	// New service/repository does not reset persisted limits.
	s, e := New(NewRepository(h.store), Options{Clock: h.service.options.Clock, AllowedOrigins: []string{testOrigin}})
	if e != nil {
		t.Fatal(e)
	}
	w = request(s.Handler(), "POST", "/api/v1/auth/login", body, testOrigin, nil, nil)
	if w.Code != 429 {
		t.Fatalf("persistent limiter: %d", w.Code)
	}
	h.now.Add(60)
	h.login(t, testPassword, nil)
	// Nil bootstrap never opens an empty-database initialization backdoor.
	s.options.Bootstrap = nil
	w = request(s.Handler(), "POST", "/api/v1/auth/initialize", body, testOrigin, nil, map[string]string{"X-Meshstrata-Bootstrap": h.proof})
	if w.Code != 403 {
		t.Fatal("nil bootstrap accepted")
	}
	h.now.Add(3600)
	w = request(h.service.Handler(), "POST", "/api/v1/auth/initialize", body, testOrigin, nil, map[string]string{"X-Meshstrata-Bootstrap": h.proof})
	if w.Code != 403 {
		t.Fatal("expired bootstrap accepted")
	}
}

type rollbackDB struct {
	storage.Database
	fail bool
}

var rollbackFailure = errors.New("injected rollback")

func (d *rollbackDB) Write(ctx context.Context, fn func(*dbgen.Queries) error) error {
	return d.Database.Write(ctx, func(q *dbgen.Queries) error {
		if e := fn(q); e != nil {
			return e
		}
		if d.fail {
			return rollbackFailure
		}
		return nil
	})
}
func TestInitializeRaceAndRollback(t *testing.T) {
	h := newHarness(t, 20)
	ctx := context.Background()
	db := &rollbackDB{Database: h.store, fail: true}
	repo := NewRepository(db)
	if e := repo.initialize(ctx, "admin", "hash", time.Unix(h.now.Load(), 0)); !errors.Is(e, rollbackFailure) {
		t.Fatal(e)
	}
	db.fail = false
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- repo.initialize(ctx, "admin", "hash", time.Unix(h.now.Load(), 0)) }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 7 {
		t.Fatalf("race winners=%d conflicts=%d", success, conflict)
	}
	// Consumed bootstrap remains consumed in subsequent transactions.
	if e := h.store.Write(ctx, func(q *dbgen.Queries) error { _, e := q.B01ConsumeBootstrap(ctx); return e }); e != nil {
		t.Fatal(e)
	}
}
func TestParsingAndGuardAmbiguities(t *testing.T) {
	h := newHarness(t, 20)
	h.initialize(t)
	c, token := h.login(t, testPassword, nil)
	called := false
	next := h.service.Write(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for _, which := range []string{"cookie", "origin", "csrf", "cross-site"} {
		r := httptest.NewRequest("POST", "/business", nil)
		r.AddCookie(c)
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("X-CSRF-Token", token)
		switch which {
		case "cookie":
			r.AddCookie(c)
		case "origin":
			r.Header.Add("Origin", testOrigin)
		case "csrf":
			r.Header.Add("X-CSRF-Token", token)
		case "cross-site":
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		w := httptest.NewRecorder()
		next.ServeHTTP(w, r)
		if called || w.Code < 400 {
			t.Fatalf("ambiguous %s allowed", which)
		}
	}
	for _, body := range []string{`{"username":"admin","password":"x","extra":1}`, `{} {}`, strings.Repeat("x", 5000)} {
		w := request(h.service.Handler(), "POST", "/api/v1/auth/login", body, testOrigin, nil, nil)
		if w.Code != 400 {
			t.Fatalf("malformed: %d", w.Code)
		}
	}
	h.service.kdf <- struct{}{}
	w := request(h.service.Handler(), "POST", "/api/v1/auth/login", `{}`, testOrigin, nil, nil)
	<-h.service.kdf
	if w.Code != 429 {
		t.Fatal("unbounded KDF concurrency")
	}
}
func TestPasswordVersionAndOptions(t *testing.T) {
	h := newHarness(t, 20)
	for _, o := range []Options{{}, {AllowedOrigins: []string{"http://controller.example"}}, {AllowedOrigins: []string{testOrigin}, CookieName: "unsafe"}, {AllowedOrigins: []string{testOrigin}, SessionTTL: time.Second}} {
		if _, e := New(NewRepository(h.store), o); e == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, e := New(nil, Options{}); e == nil {
		t.Fatal("nil repo")
	}
	if checkPassword("$argon2id$v=19$m=999999999,t=3,p=4$xx$xx", "password") {
		t.Fatal("unbounded parameters accepted")
	}
	if _, e := New(NewRepository(h.store), Options{AllowedOrigins: []string{testOrigin}, Random: strings.NewReader("")}); e == nil {
		t.Fatal("random failure ignored")
	}
}

func TestAuthTransactionsRollback(t *testing.T) {
	h := newHarness(t, 20)
	h.initialize(t)
	c, _ := h.login(t, testPassword, nil)
	ctx := context.Background()
	repo := h.service.repo
	a, e := repo.administrator(ctx)
	if e != nil {
		t.Fatal(e)
	}
	ss, e := repo.session(ctx, tokenHash(c.Value), time.Unix(h.now.Load(), 0))
	if e != nil {
		t.Fatal(e)
	}
	wrapper := &rollbackDB{Database: h.store, fail: true}
	failing := NewRepository(wrapper)
	newToken := tokenHash("new random session fixture")
	now := time.Unix(h.now.Load(), 0)
	if e := failing.login(ctx, a, newToken, ss.hash, now, now.Add(time.Hour)); !errors.Is(e, rollbackFailure) {
		t.Fatal(e)
	}
	if _, e := repo.session(ctx, ss.hash, now); e != nil {
		t.Fatal("failed login removed previous session")
	}
	if _, e := repo.session(ctx, newToken, now); !errors.Is(e, ErrUnauthenticated) {
		t.Fatal("failed login persisted new session")
	}
	if e := failing.logout(ctx, ss); !errors.Is(e, rollbackFailure) {
		t.Fatal(e)
	}
	if _, e := repo.session(ctx, ss.hash, now); e != nil {
		t.Fatal("failed logout removed session")
	}
	if e := failing.changePassword(ctx, ss, a, "new password hash fixture", now); !errors.Is(e, rollbackFailure) {
		t.Fatal(e)
	}
	current, e := repo.administrator(ctx)
	if e != nil || current.password != a.password || current.revision != a.revision {
		t.Fatal("failed password change committed")
	}
	if _, e := repo.session(ctx, ss.hash, now); e != nil {
		t.Fatal("failed password change removed session")
	}
	// A logout committed between guard and mutation must prevent a password update.
	if e := repo.logout(ctx, ss); e != nil {
		t.Fatal(e)
	}
	if e := repo.changePassword(ctx, ss, a, "new password hash fixture", now); !errors.Is(e, ErrUnauthenticated) {
		t.Fatal("logged out session authorized mutation")
	}
}

func TestConcurrentHTTPBootstrapAndPersistentLimiter(t *testing.T) {
	h := newHarness(t, 20)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	body := `{"username":"admin","password":"` + testPassword + `"}`
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(h.service.Handler(), "POST", "/api/v1/auth/initialize", body, testOrigin, nil, map[string]string{"X-Meshstrata-Bootstrap": h.proof}).Code
		}()
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		switch code {
		case 204:
			success++
		case 409, 429:
		default:
			t.Fatalf("bootstrap race status: %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("bootstrap HTTP winners=%d", success)
	}
	// Atomically reserve attempts under contention. Separate action avoids bootstrap history.
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- h.service.repo.attempt(context.Background(), "password", time.Unix(h.now.Load(), 0), time.Minute, 5)
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for e := range results {
		if e == nil {
			accepted++
		} else if !errors.Is(e, ErrRateLimited) {
			t.Fatal(e)
		}
	}
	if accepted != 5 {
		t.Fatalf("limiter accepted=%d", accepted)
	}
}

func TestEmptyDatabaseCannotBeClaimedWithoutBootstrap(t *testing.T) {
	h := newHarness(t, 5)
	h.service.options.Bootstrap = nil
	body := `{"username":"admin","password":"` + testPassword + `"}`
	w := request(h.service.Handler(), "POST", "/api/v1/auth/initialize", body, testOrigin, nil, map[string]string{"X-Meshstrata-Bootstrap": h.proof})
	if w.Code != 403 {
		t.Fatal("empty database claimed without local bootstrap")
	}
	w = request(h.service.Handler(), "POST", "/api/v1/auth/login", body, testOrigin, nil, nil)
	if w.Code != 401 {
		t.Fatal("login succeeded before initialization")
	}
	if _, e := h.service.repo.administrator(context.Background()); e == nil {
		t.Fatal("administrator was created")
	}
}

func TestTypedNilDependenciesFailConstruction(t *testing.T) {
	var store *storage.Store
	if _, e := New(NewRepository(store), Options{AllowedOrigins: []string{testOrigin}}); e == nil {
		t.Fatal("typed nil database accepted")
	}
	h := newHarness(t, 5)
	var reader *bytes.Reader
	if _, e := New(NewRepository(h.store), Options{AllowedOrigins: []string{testOrigin}, Random: reader}); e == nil {
		t.Fatal("typed nil random source accepted")
	}
}
