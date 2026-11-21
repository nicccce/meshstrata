package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicccce/meshstrata/controller/internal/rules"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type testGuard struct{}

func (testGuard) Read(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Test-Session") != "yes" {
			w.WriteHeader(401)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (g testGuard) Write(next http.Handler) http.Handler {
	return g.Read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "https://admin.test" || r.Header.Get("X-CSRF-Token") != "test" {
			w.WriteHeader(403)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
func fixture(t *testing.T) (*Service, http.Handler) {
	t.Helper()
	db, e := storage.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	rs, e := rules.NewService(stage3.RulesOptions{Runtime: stage3.Runtime{DB: db}})
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{DB: db, Guard: testGuard{}, Rules: rs}
	return s, s.Handler()
}
func request(t *testing.T, h http.Handler, method, path string, body any, status int) map[string]json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://admin.test")
	r.Header.Set("X-CSRF-Token", "test")
	r.Header.Set("Test-Session", "yes")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	var out map[string]json.RawMessage
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestCatalogCASReplayReferencesArchiveAndPagination(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	input := map[string]any{"expected_revision": 0, "idempotency_key": "set-create", "name": "中文 名称 !", "domains": []string{"example.org"}}
	out := request(t, h, "POST", "/api/v1/domain-sets", input, 202)
	var pin control.Pin
	json.Unmarshal(out["pin"], &pin)
	replay := request(t, h, "POST", "/api/v1/domain-sets", input, 202)
	if !bytes.Equal(replay["pin"], out["pin"]) {
		t.Fatal("create replay changed identity")
	}
	input["name"] = "changed"
	request(t, h, "POST", "/api/v1/domain-sets", input, 409)
	profile := map[string]any{"expected_revision": 0, "idempotency_key": "profile-create", "name": "规则", "source": "RULE-SET," + string(pin.ID) + ",DIRECT\nMATCH,All-Proxies", "sets": []control.Pin{pin}}
	p := request(t, h, "POST", "/api/v1/rule-profiles", profile, 202)
	var pp control.Pin
	json.Unmarshal(p["pin"], &pp)
	request(t, h, "DELETE", "/api/v1/domain-sets/"+string(pin.ID), domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "archive-used"}, 409)
	refs := request(t, h, "GET", "/api/v1/domain-sets/"+string(pin.ID)+"/references?limit=1", nil, 200)
	if !bytes.Contains(refs["items"], []byte(pp.ID)) {
		t.Fatal("reference omitted")
	}
	request(t, h, "DELETE", "/api/v1/rule-profiles/"+string(pp.ID), domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "archive-profile"}, 202)
	request(t, h, "DELETE", "/api/v1/domain-sets/"+string(pin.ID), domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "archive-set"}, 202)
	// Bypass the HTTP precheck: actual catalog transactions must still reject a
	// concurrent installation of a new reference to an archived object.
	_, _, e := s.Rules.SaveProfile(ctx, domain.Mutation{ExpectedRevision: 0, IdempotencyKey: "race-after-archive"}, stage3.ProfileInput{Name: "race", Source: profile["source"].(string), Sets: []control.Pin{pin}})
	if e == nil {
		t.Fatal("archived dependency installed")
	}
	if _, e = s.Rules.DomainSet(ctx, pin); e != nil {
		t.Fatal("historical pin lost", e)
	}
	for i := 0; i < 205; i++ {
		_, e := s.Rules.SaveDomainSet(ctx, domain.Mutation{IdempotencyKey: fmt.Sprint("page-", i)}, "", fmt.Sprint("set ", i), stage3.DomainSetSpec{Domains: []string{"example.com"}})
		if e != nil {
			t.Fatal(e)
		}
	}
	cursor := ""
	seen := map[domain.ID]bool{}
	pages := 0
	for {
		path := "/api/v1/domain-sets?limit=37"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		v := request(t, h, "GET", path, nil, 200)
		var items []Catalog
		json.Unmarshal(v["items"], &items)
		for _, item := range items {
			if seen[item.Pin.ID] {
				t.Fatal("duplicate page item")
			}
			seen[item.Pin.ID] = true
		}
		pages++
		if string(v["next_cursor"]) == "null" {
			break
		}
		json.Unmarshal(v["next_cursor"], &cursor)
		if pages > 10 {
			t.Fatal("cursor did not advance")
		}
	}
	if len(seen) != 205 || pages != 6 {
		t.Fatalf("pagination lost items: %d / %d", len(seen), pages)
	}
	request(t, h, "GET", "/api/v1/rule-profiles?cursor="+cursor, nil, 400)
	input = map[string]any{"expected_revision": 1, "idempotency_key": "stale", "name": "stale", "domains": []string{"example.net"}}
	// Current archived state is still visible as history, never mutable.
	request(t, h, "PATCH", "/api/v1/domain-sets/"+string(pin.ID), input, 404)
}
func TestGuardAndStrictBodies(t *testing.T) {
	_, h := fixture(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/routes"}, {"POST", "/api/v1/routes"}, {"GET", "/api/v1/subscriptions"}, {"POST", "/api/v1/subscriptions"}, {"DELETE", "/api/v1/domain-sets/set_00000000000000000000000000000001"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != 401 {
			t.Fatalf("unguarded %s %s", tc.method, tc.path)
		}
	}
	for _, raw := range []string{`{"expected_revision":0,"idempotency_key":"x","name":"x","domains":[],"name":"y"}`, `{"expected_revision":0,"idempotency_key":"x","name":"x","domains":[],"unexpected":true}`, `{} {}`, `[]`} {
		r := httptest.NewRequest("POST", "/api/v1/domain-sets", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Test-Session", "yes")
		r.Header.Set("Origin", "https://admin.test")
		r.Header.Set("X-CSRF-Token", "test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("body accepted: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/domain-sets", strings.NewReader(`{}`))
	r.Header.Set("Test-Session", "yes")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing Origin/CSRF accepted")
	}
	invalid := map[string]any{"expected_revision": 0, "idempotency_key": "invalid-source", "name": "bad", "source": "DOMAIN-SUFFIX,example.com,Unknown\nMATCH,DIRECT", "sets": []control.Pin{}}
	out := request(t, h, "POST", "/api/v1/rule-profiles", invalid, 400)
	if !bytes.Contains(out["issues"], []byte(`"line":1`)) {
		t.Fatal("source position missing")
	}
}

func TestInternalErrorStatusAndRedaction(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, fmt.Errorf("%w: private dependency details", &domain.Problem{Code: "INTERNAL"}))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private dependency details") {
		t.Fatal("internal failure not safely represented", w.Code, w.Body.String())
	}
}
