package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/platform"
)

func testID(kind string, n int) domain.ID { return domain.ID(fmt.Sprintf("%s_%032x", kind, n)) }

var node = testID("node", 1)

type testClock struct{ unix atomic.Int64 }

func (c *testClock) now() time.Time  { return time.Unix(0, c.unix.Load()).UTC() }
func (c *testClock) set(t time.Time) { c.unix.Store(t.UnixNano()) }

type fakeKernel struct {
	running            atomic.Bool
	stopCount          atomic.Int64
	verifyCount        atomic.Int64
	failed             atomic.Int64
	noRevocation       atomic.Bool
	inspectBlockedPort atomic.Uint32
	alsoBlockPort      atomic.Uint32
	reloadHook         func(context.Context, mihomo.Candidate) error
}

func (*fakeKernel) Validate(_ context.Context, _ mihomo.Candidate) (mihomo.Validated, error) {
	return mihomo.Validated{}, nil
}
func (*fakeKernel) Startup(_ context.Context, _ mihomo.Candidate) (string, []string, error) {
	return "fixed", nil, nil
}
func (k *fakeKernel) Reload(ctx context.Context, c mihomo.Candidate) (mihomo.Applied, error) {
	if k.reloadHook != nil {
		if err := k.reloadHook(ctx, c); err != nil {
			return mihomo.Applied{}, err
		}
	}
	return mihomo.Applied{Digest: c.Digest, APIAccepted: true}, nil
}
func (k *fakeKernel) Inspect(_ context.Context, c mihomo.Candidate, pid int) (mihomo.Evidence, error) {
	o := mihomo.Evidence{Digest: c.Digest, PID: pid}
	for _, e := range c.Expected {
		status := check(mihomo.Succeeded)
		if uint32(e.Port) == k.inspectBlockedPort.Load() || uint32(e.Port) == k.alsoBlockPort.Load() {
			status = mihomo.Check{Status: mihomo.Failed, Code: "OWNED_LISTENER_MISSING"}
		}
		o.Listeners = append(o.Listeners, mihomo.ListenerEvidence{Expected: e, Listening: status})
	}
	return o, nil
}
func (k *fakeKernel) Verify(ctx context.Context, c mihomo.Candidate, pid int, _ mihomo.Probe) (mihomo.Evidence, error) {
	k.verifyCount.Add(1)
	result, _ := k.Inspect(ctx, c, pid)
	result.Verified = true
	if k.failed.Load() > 0 {
		k.failed.Add(-1)
		result.Verified = false
	}
	return result, nil
}
func (k *fakeKernel) Revocation(_ context.Context, c mihomo.Candidate, _ mihomo.RevocationProbe) (mihomo.RevocationEvidence, error) {
	return mihomo.RevocationEvidence{Digest: c.Digest, CheckedAt: time.Now(), Verified: !k.noRevocation.Load()}, nil
}
func (*fakeKernel) Connections(context.Context) ([]mihomo.Connection, error) { return nil, nil }
func (*fakeKernel) CloseConnection(context.Context, mihomo.Candidate, string, string) error {
	return nil
}
func (k *fakeKernel) Start(context.Context, string, []string) (int, error) {
	k.running.Store(true)
	return 123, nil
}
func (k *fakeKernel) Status(context.Context) (platform.Status, error) {
	return platform.Status{Running: k.running.Load(), PID: 123}, nil
}
func (k *fakeKernel) Stop(context.Context) error {
	k.stopCount.Add(1)
	k.running.Store(false)
	return nil
}

type noProbe struct{}

func (noProbe) Forward(context.Context, core.Expected, string) mihomo.Check {
	return check(mihomo.Unverified)
}
func (noProbe) OldAuthenticationDenied(context.Context) mihomo.Check { return check(mihomo.Unverified) }
func (noProbe) ExistingSessionsClosed(context.Context) mihomo.Check  { return check(mihomo.Unverified) }
func (noProbe) OtherIdentityUnaffected(context.Context) mihomo.Check { return check(mihomo.Unverified) }

type fixture struct {
	t            *testing.T
	clock        testClock
	now          time.Time
	store        *checkpoint.Store
	storeOptions checkpoint.Options
	opts         Options
	kernel       *fakeKernel
	artifacts    sync.Map
	pending      chan struct{}
	fetchCount   atomic.Int64
}

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, now: time.Now().UTC(), kernel: &fakeKernel{}, pending: make(chan struct{}, 10)}
	f.clock.set(f.now)
	f.storeOptions = checkpoint.Options{Dir: privateTestDir(t), NodeID: node, KeyID: "local-test", Key: make([]byte, 32), Now: f.clock.now}
	f.reopen()
	dir := privateTestDir(t)
	sockDir, err := os.MkdirTemp("", "b05-unit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	res, err := authorization.NewResolver(node, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.opts = Options{NodeID: node, State: f.store, Materials: res, Kernel: f.kernel, Process: f.kernel, ConfigDir: dir,
		SocketPath: filepath.Join(sockDir, "a.sock"), APISecret: strings.Repeat("0", 32), Now: f.clock.now,
		Fetch: func(_ context.Context, key control.ArtifactKey, limit int64) ([]byte, error) {
			f.fetchCount.Add(1)
			data, ok := f.artifacts.Load(key)
			if !ok || limit != control.MaxBundleBytes {
				return nil, errors.New("test artifact missing")
			}
			return data.([]byte), nil
		},
		ProbeFactory: func(context.Context, control.Bundle, mihomo.Candidate) (mihomo.Probe, error) { return noProbe{}, nil },
		RevocationProbeFactory: func(context.Context, control.Bundle, mihomo.Candidate) (mihomo.RevocationProbe, error) {
			return noProbe{}, nil
		},
		OnPending: func() {
			select {
			case f.pending <- struct{}{}:
			default:
			}
		},
	}
	return f
}
func (f *fixture) reopen() {
	f.t.Helper()
	if f.store != nil {
		f.store.Close()
	}
	s, err := checkpoint.Open(f.storeOptions)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = s
	f.opts.State = s
	f.t.Cleanup(func() { s.Close() })
}
func launch(t *testing.T, o Options) *Runtime {
	t.Helper()
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Hello(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("premature hello", err)
	}
	out := make(chan error, 1)
	go func() { out <- r.Run(context.Background()) }()
	t.Cleanup(func() { r.Close() })
	select {
	case <-r.Ready():
		return r
	case err := <-out:
		t.Fatal("recovery failure", err)
	case <-time.After(15 * time.Second):
		t.Fatal("recovery timeout")
	}
	return nil
}
func (f *fixture) state() control.DurableState {
	f.t.Helper()
	s, err := f.store.Load(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}
func bundle(n int, epoch domain.Revision, kind domain.TaskKind, listeners ...control.BundleListener) control.Bundle {
	rule, _ := control.ComponentDigest("DIRECT")
	egress, _ := control.ComponentDigest("direct")
	dns := control.DNSPolicy{Bootstrap: []string{"127.0.0.1"}, Resolvers: []string{"127.0.0.1"}}
	dnsDigest, _ := control.ComponentDigest(dns)
	return control.Bundle{TaskID: testID("task", n), Format: 1, Kind: kind, NodeID: node, RevocationEpoch: epoch, TargetRevision: domain.Revision(n),
		Rules:     control.RuleInput{Default: "DIRECT", Pin: control.Pin{ID: testID("rule", 1), Revision: 1, Digest: rule}},
		Egress:    control.EgressInput{Mode: "direct", Pin: control.Pin{ID: testID("egress", 1), Revision: 1, Digest: egress}},
		DNS:       control.DNSInput{Bootstrap: dns.Bootstrap, Resolvers: dns.Resolvers, Pin: control.Pin{ID: testID("dns", 1), Revision: 1, Digest: dnsDigest}},
		Listeners: append([]control.BundleListener{}, listeners...), Tombstones: []control.Tombstone{}, Resources: []control.Resource{}}
}
func listener(n int, port uint16, expires time.Time) control.BundleListener {
	return control.BundleListener{ListenerID: testID("listener", n), NodeID: node, Protocol: domain.SS2022,
		Address: "127.0.0.1", Port: port, Transport: "tcp_udp", BusinessUDP: true,
		Cipher: "2022-blake3-aes-128-gcm", Credential: control.PrivateCredential{Ref: testID("credential", n), Audience: node,
			Password: "AAAAAAAAAAAAAAAAAAAAAA=="},
		Authorization: control.BundleAuthorization{EntryID: testID("entry", n), IdentityID: testID("identity", n), Generation: 1, ExpiresAt: expires},
		ServerNames:   []string{}, ShortIDs: []string{}}
}
func (f *fixture) command(b control.Bundle) control.Command {
	f.t.Helper()
	raw, key, err := control.EncodeBundle(b)
	if err != nil {
		f.t.Fatal("encode test bundle", err)
	}
	f.artifacts.Store(key, raw)
	return control.Command{TaskID: b.TaskID, NodeID: b.NodeID, Kind: b.Kind, TargetRevision: b.TargetRevision,
		TargetDigest: key.TargetDigest, RevocationEpoch: b.RevocationEpoch, Deadline: f.clock.now().Add(time.Hour),
		IdempotencyKey: string(b.TaskID), DependsOn: []domain.ID{}}
}
func execute(t *testing.T, r *Runtime, c control.Command) control.Result {
	t.Helper()
	result, err := r.Execute(context.Background(), c)
	if err != nil {
		t.Fatal("execute", err)
	}
	return result
}
func waitFor(t *testing.T, d time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition deadline exceeded")
}

func TestOfflineExpiryClockFloorReplayAndRenewal(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	l := listener(1, 12000, f.now.Add(time.Minute))
	c := f.command(bundle(1, 1, domain.Publish, l))
	res := execute(t, r, c)
	if res.Verification != domain.Verified {
		t.Fatal("initial publish", res.Verification)
	}
	receipt := control.Receipt{NodeID: node, TaskID: c.TaskID, Sequence: res.Sequence}
	if err := r.Acknowledge(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if got := execute(t, r, c); got.Sequence != res.Sequence || f.fetchCount.Load() != 1 {
		t.Fatal("command replay executed")
	}
	bad := c
	bad.TargetRevision++
	if _, err := r.Execute(context.Background(), bad); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	f.clock.set(f.now.Add(2 * time.Minute))
	select {
	case <-f.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("offline notification missing")
	}
	waitFor(t, 3*time.Second, func() bool {
		for _, o := range f.state().Outbox {
			if o.Cleanup == "confirmed" && len(o.Restrictions) == 1 {
				return true
			}
		}
		return false
	})
	s := f.state()
	if len(s.Tombstones) != 1 || len(s.Grants) != 1 || s.Grants[0].Origin.TaskID != c.TaskID || len(s.Outbox) != 2 ||
		s.Outbox[0].Sequence != res.Sequence+1 || s.Outbox[1].Sequence != res.Sequence+2 ||
		s.Outbox[1].Applied || s.Outbox[1].Verification == domain.Verified || s.Outbox[1].ConfirmedAt == nil {
		t.Fatal("invalid offline result sequence or evidence")
	}
	if s.Checkpoint.Startup.EffectiveDigest == s.Checkpoint.Startup.ArtifactDigest ||
		s.Checkpoint.Startup.ArtifactDigest != c.TargetDigest {
		t.Fatal("artifact/effective digest conflation")
	}
	r.Close()
	if !f.kernel.running.Load() {
		t.Fatal("normal runtime shutdown stopped kernel")
	}
	f.clock.set(f.now)
	f.reopen()
	r = launch(t, f.opts)
	s = f.state()
	if len(s.Tombstones) != 1 || s.ClockFloor.Before(f.now.Add(2*time.Minute)) {
		t.Fatal("restart lost clock floor or tombstone")
	}
	old := f.command(bundle(2, 1, domain.Rollback, l))
	result := execute(t, r, old)
	s = f.state()
	cfg, err := r.checkedRender(context.Background(), s.Checkpoint.Startup)
	if err != nil || len(cfg.Expected) != 0 || result.Verification == domain.Verified {
		t.Fatal("old rollback resurrected tombstone", err)
	}
	l2 := listener(2, 12000, f.now.Add(time.Hour))
	l2.Authorization.EntryID = l.Authorization.EntryID
	l2.Authorization.Generation = 2
	l2.Credential.Password = "AQEBAQEBAQEBAQEBAQEBAQ=="
	renew := execute(t, r, f.command(bundle(3, 2, domain.Publish, l2)))
	if renew.Verification != domain.Verified {
		t.Fatal("new generation rejected", renew.Issue)
	}
}

func TestLongFetchPreemptedByExpiryAndRecovery(t *testing.T) {
	f := newFixture(t)
	fetch := f.opts.Fetch
	entered := make(chan struct{})
	canceled := make(chan struct{})
	f.opts.Fetch = func(ctx context.Context, key control.ArtifactKey, limit int64) ([]byte, error) {
		if key.TargetRevision == 2 {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		return fetch(ctx, key, limit)
	}
	r := launch(t, f.opts)
	old := listener(1, 12000, f.now.Add(time.Minute))
	execute(t, r, f.command(bundle(1, 1, domain.Publish, old)))
	c := f.command(bundle(2, 1, domain.Publish, old))
	finished := make(chan error, 1)
	go func() { _, err := r.Execute(context.Background(), c); finished <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch did not enter")
	}
	f.clock.set(f.now.Add(2 * time.Minute))
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch blocked expiry")
	}
	if <-finished == nil {
		t.Fatal("canceled execution returned success")
	}
	waitFor(t, 3*time.Second, func() bool { return len(f.state().Tombstones) == 1 })
	r.Close()
	f.reopen()
	f.opts.Fetch = fetch
	r = launch(t, f.opts)
	result := execute(t, r, c)
	if result.Verification == domain.Verified {
		t.Fatal("interrupted old artifact resurrected identity")
	}
}

func TestFalseReloadSuccessAndRollbackFailure(t *testing.T) {
	for _, count := range []int64{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newFixture(t)
			r := launch(t, f.opts)
			l := listener(1, 12000, f.now.Add(time.Hour))
			c := f.command(bundle(1, 1, domain.Publish, l))
			execute(t, r, c)
			f.kernel.failed.Store(count)
			next := listener(2, 12001, f.now.Add(time.Hour))
			out := execute(t, r, f.command(bundle(2, 1, domain.Publish, next)))
			if !out.Applied || out.Verification != domain.Failed {
				t.Fatal("reload response promoted")
			}
			s := f.state()
			if count == 1 && (out.Rollback != "confirmed" || s.Checkpoint.Verified == nil || s.Checkpoint.Verified.TaskID != c.TaskID) {
				t.Fatal("rollback did not verify previous source", out.Rollback)
			}
			if count == 2 && (out.Rollback != "failed" || s.Checkpoint.Verified != nil || f.kernel.running.Load()) {
				t.Fatal("failed rollback left verified state or live process", out.Rollback)
			}
		})
	}
}

func TestHelloPagesByCountAndWireBytes(t *testing.T) {
	for _, padding := range []int{0, 20000} {
		t.Run(fmt.Sprint(padding), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			s := f.state()
			for i := 1; i <= 121; i++ {
				c := f.command(bundle(i, 1, domain.Publish))
				s.Executions = append(s.Executions, control.Execution{Command: c})
				result := baseResult(c)
				result.Issue = &domain.Problem{Code: domain.InvalidArgument, Fields: []domain.FieldError{{Path: strings.Repeat("x", padding)}}}
				if _, err := enqueue(&s, result, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Commit(ctx, s.Revision, s); err != nil {
				t.Fatal(err)
			}
			r := launch(t, f.opts)
			total := 0
			for {
				h, err := r.Hello(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(h.PendingResults) == 0 {
					break
				}
				if total == 0 && ((padding == 0 && len(h.PendingResults) != 100) || (padding > 0 && len(h.PendingResults) >= 100)) {
					t.Fatal("page did not observe respective limit")
				}
				m := control.Message{Version: 1, Type: "hello", MessageID: r.messageID, NodeID: node, SentAt: f.now, Hello: &h}
				encoded, err := json.Marshal(m)
				if m.Validate(node) != nil || err != nil || len(encoded) > control.MaxMessageBytes {
					t.Fatal("invalid wire page")
				}
				for _, result := range h.PendingResults {
					total++
					if err := r.Acknowledge(ctx, control.Receipt{NodeID: node, TaskID: result.TaskID, Sequence: result.Sequence}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if total != 121 {
				t.Fatal("result lost", total)
			}
		})
	}
}

func TestRestrictionPendingSurvivesReceiptAndFailedProbe(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	l := listener(1, 12000, f.now.Add(time.Minute))
	c := f.command(bundle(1, 1, domain.Publish, l))
	execute(t, r, c)
	f.kernel.noRevocation.Store(true)
	f.clock.set(f.now.Add(2 * time.Minute))
	ctx := context.Background()
	waitFor(t, 4*time.Second, func() bool {
		s := f.state()
		return len(s.Outbox) == 2 && s.Outbox[1].Cleanup == "pending" && !f.kernel.running.Load()
	})
	for _, out := range f.state().Outbox {
		if err := r.Acknowledge(ctx, control.Receipt{TaskID: out.TaskID, NodeID: out.NodeID, Sequence: out.Sequence}); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	f.reopen()
	f.kernel.noRevocation.Store(false)
	r = launch(t, f.opts)
	s := f.state()
	if len(s.Outbox) != 1 || s.Outbox[0].Sequence != 3 || s.Outbox[0].Cleanup != "confirmed" ||
		len(s.Tombstones) != 1 || s.Executions[0].LastResult.Sequence != 1 {
		t.Fatal("acked restriction lost pending cleanup across restart")
	}
}

func TestRestartDoesNotPromoteCandidate(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	l := listener(1, 12000, f.now.Add(time.Hour))
	first := f.command(bundle(1, 1, domain.Publish, l))
	execute(t, r, first)
	other := listener(2, 12001, f.now.Add(time.Hour))
	b := bundle(2, 1, domain.Publish, other)
	second := f.command(b)
	ctx := context.Background()
	key := keyFor(second)
	data, _ := f.artifacts.Load(key)
	if err := f.store.SaveArtifact(ctx, key, data.([]byte)); err != nil {
		t.Fatal(err)
	}
	next, err := r.render(ctx, b, b.Listeners)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	s := f.state()
	s.Executions = append(s.Executions, control.Execution{Command: second})
	s.Checkpoint.Candidate = &control.Snapshot{TaskID: b.TaskID, Revision: b.TargetRevision,
		ArtifactDigest: key.TargetDigest, EffectiveDigest: next.Digest, Epoch: b.RevocationEpoch}
	if err := f.store.Commit(ctx, s.Revision, s); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	r = launch(t, f.opts)
	s = f.state()
	if s.Checkpoint.Verified.TaskID != first.TaskID || s.Checkpoint.Applied.TaskID != first.TaskID {
		t.Fatal("interrupted candidate was promoted to verified")
	}
	if out := execute(t, r, second); out.Verification != domain.Verified || f.fetchCount.Load() != 1 {
		t.Fatal("command recovery did not reuse durable artifact")
	}
}

func TestClockAdvanceDuringShutdownBeforeReady(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	l := listener(1, 12000, f.now.Add(time.Minute))
	c := f.command(bundle(1, 1, domain.Publish, l))
	execute(t, r, c)
	r.Close()
	f.clock.set(f.now.Add(2 * time.Minute))
	f.reopen()
	r = launch(t, f.opts)
	s := f.state()
	active, err := r.checkedRender(context.Background(), s.Checkpoint.Startup)
	if err != nil || len(active.Expected) != 0 || len(s.Tombstones) != 1 ||
		len(s.Outbox) != 3 || s.Outbox[2].Cleanup != "confirmed" {
		t.Fatal("startup approved expired listeners", err)
	}
}

func TestRestrictionsBatchedByOriginalTask(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	listeners := make([]control.BundleListener, 40)
	for i := range listeners {
		listeners[i] = listener(i+1, uint16(12000+i), f.now.Add(time.Minute))
	}
	first := f.command(bundle(1, 1, domain.Publish, listeners...))
	execute(t, r, first)
	l := listener(41, 12040, f.now.Add(time.Minute))
	second := f.command(bundle(2, 1, domain.Publish, append(listeners, l)...))
	execute(t, r, second)
	f.clock.set(f.now.Add(2 * time.Minute))
	waitFor(t, 4*time.Second, func() bool { return len(f.state().Outbox) == 8 })
	seen := map[domain.ID]map[string]int{first.TaskID: {}, second.TaskID: {}}
	for _, o := range f.state().Outbox {
		if len(o.Restrictions) > 32 || (len(o.Restrictions) > 0 && o.Applied) {
			t.Fatal("invalid restriction event")
		}
		seen[o.TaskID][o.Cleanup] += len(o.Restrictions)
	}
	if seen[first.TaskID]["confirmed"] != 40 || seen[first.TaskID]["pending"] != 40 ||
		seen[second.TaskID]["confirmed"] != 1 || seen[second.TaskID]["pending"] != 1 {
		t.Fatal("restrictions did not use original task")
	}
}

func TestGrantMutationAndStaleEpochRejected(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	l := listener(1, 12000, f.now.Add(time.Hour))
	c := f.command(bundle(1, 2, domain.Publish, l))
	execute(t, r, c)
	for i, mut := range []func(*control.BundleListener){
		func(l *control.BundleListener) { l.Authorization.ExpiresAt = l.Authorization.ExpiresAt.Add(time.Hour) },
		func(l *control.BundleListener) { l.Credential.Password = "AQEBAQEBAQEBAQEBAQEBAQ==" },
		func(l *control.BundleListener) { l.ListenerID = testID("listener", 2) },
	} {
		changed := l
		mut(&changed)
		out := execute(t, r, f.command(bundle(2+i, 2, domain.Publish, changed)))
		if out.Verification != domain.Failed || out.Issue == nil || out.Issue.Code != domain.RevisionConflict {
			t.Fatal("grant mutation allowed")
		}
	}
	stale := f.command(bundle(5, 1, domain.Rollback, l))
	if _, err := r.Execute(context.Background(), stale); err == nil {
		t.Fatal("stale epoch accepted")
	}
	s := f.state()
	if s.Checkpoint.Verified.TaskID != c.TaskID || len(s.Grants) != 1 || s.Grants[0].Origin.TaskID != c.TaskID {
		t.Fatal("rejected command changed effective grant")
	}
}

func TestListenerFailureRestoresVerifiedStartup(t *testing.T) {
	for _, oldUnavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("old_unavailable=%v", oldUnavailable), func(t *testing.T) {
			f := newFixture(t)
			r := launch(t, f.opts)
			old := listener(1, 12000, f.now.Add(time.Hour))
			b1, b2 := bundle(1, 1, domain.Publish, old), bundle(2, 1, domain.Publish, old)
			if execute(t, r, f.command(b1)).Verification != domain.Verified ||
				execute(t, r, f.command(b2)).Verification != domain.Verified {
				t.Fatal("fixture has no confirmed startup")
			}
			prior := f.state().Checkpoint
			next := listener(2, 12001, f.now.Add(time.Hour))
			f.kernel.inspectBlockedPort.Store(uint32(next.Port))
			if oldUnavailable {
				f.kernel.alsoBlockPort.Store(uint32(old.Port))
			}
			out := execute(t, r, f.command(bundle(3, 1, domain.Publish, old, next)))
			cp := f.state().Checkpoint
			if !out.Applied || out.Verification != domain.Failed || out.EffectiveDigest != "" ||
				!reflect.DeepEqual(cp.Startup, prior.Startup) || !reflect.DeepEqual(cp.PreviousGood, prior.PreviousGood) {
				t.Fatal("failed candidate replaced verified history")
			}
			if oldUnavailable {
				if out.Rollback != "failed" || out.ConfirmedAt != nil || f.kernel.running.Load() ||
					cp.Verified != nil || cp.Applied != nil || cp.Candidate != nil {
					t.Fatal("unrestorable listener was confirmed or left active")
				}
			} else if out.Rollback != "confirmed" || out.ConfirmedAt == nil || !f.kernel.running.Load() ||
				!reflect.DeepEqual(cp.Verified, prior.Startup) || !reflect.DeepEqual(cp.Applied, prior.Startup) ||
				!reflect.DeepEqual(cp.Candidate, prior.Startup) {
				t.Fatal("verified startup was not restored")
			}
		})
	}
}

func TestFailedCandidateRestoresConfirmedEmptyStartup(t *testing.T) {
	f := newFixture(t)
	r := launch(t, f.opts)
	old := listener(1, 12000, f.now.Add(time.Minute))
	c := f.command(bundle(1, 1, domain.Publish, old))
	if execute(t, r, c).Verification != domain.Verified {
		t.Fatal("fixture has no confirmed startup")
	}
	f.clock.set(f.now.Add(time.Minute))
	waitFor(t, 3*time.Second, func() bool {
		s := f.state()
		return s.Checkpoint.Verified != nil && len(s.Outbox) == 3 && s.Outbox[2].Cleanup == "confirmed"
	})
	empty := f.state().Checkpoint.Startup
	cfg, err := r.checkedRender(context.Background(), empty)
	if err != nil || len(cfg.Expected) != 0 {
		t.Fatal("fixture has no confirmed empty startup", err)
	}
	f.kernel.failed.Store(1)
	next := listener(2, 12001, f.clock.now().Add(time.Hour))
	out := execute(t, r, f.command(bundle(2, 1, domain.Publish, next)))
	cp := f.state().Checkpoint
	if out.Verification != domain.Failed || out.Rollback != "confirmed" || out.ConfirmedAt == nil ||
		!reflect.DeepEqual(cp.Verified, empty) || !reflect.DeepEqual(cp.Applied, empty) ||
		!f.kernel.running.Load() || len(f.state().Tombstones) != 1 {
		t.Fatalf("confirmed empty startup did not survive the failed candidate: %s", out.Rollback)
	}
}

func TestExpiryPreemptsReloadWithDurableExplicitRevocation(t *testing.T) {
	f := newFixture(t)
	reload, canceled := make(chan struct{}), make(chan struct{})
	f.kernel.reloadHook = func(ctx context.Context, c mihomo.Candidate) error {
		// Only the first reload, the explicit revocation, is held.
		select {
		case <-reload:
			return nil
		default:
			close(reload)
		}
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}
	r := launch(t, f.opts)
	expiring := listener(1, 12000, f.now.Add(time.Minute))
	revoked := listener(2, 12001, f.now.Add(time.Hour))
	retained := listener(3, 12002, f.now.Add(2*time.Hour))
	first := f.command(bundle(1, 1, domain.Publish, expiring, revoked, retained))
	if execute(t, r, first).Verification != domain.Verified {
		t.Fatal("fixture failed")
	}
	b := bundle(2, 2, domain.Revoke, expiring, retained)
	b.Tombstones = append(b.Tombstones, control.Tombstone{IdentityID: revoked.Authorization.IdentityID,
		Generation: 1, Epoch: 2, At: f.now, Reason: "revoked"})
	next := f.command(b)
	results := make(chan control.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := r.Execute(context.Background(), next)
		results <- res
		errs <- err
	}()
	select {
	case <-reload:
	case <-time.After(3 * time.Second):
		t.Fatal("candidate did not reach reload")
	}
	initial := f.state()
	if len(initial.Tombstones) != 1 || len(initial.Outbox) != 2 || initial.Outbox[1].Cleanup != "pending" ||
		initial.Outbox[1].ConfirmedAt != nil || initial.Checkpoint.Candidate.TaskID != next.TaskID ||
		initial.Checkpoint.Applied.TaskID != first.TaskID {
		t.Fatal("revocation was not durable before the reload")
	}
	f.clock.set(f.now.Add(time.Minute))
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("reload blocked expiry")
	}
	select {
	case out := <-results:
		if err := <-errs; err != nil || out.Rollback != "pending" || out.Cleanup != "pending" || out.ConfirmedAt != nil {
			t.Fatalf("interrupted command claimed confirmation: %s/%s, %v", out.Rollback, out.Cleanup, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("candidate did not exit")
	}
	waitFor(t, 3*time.Second, func() bool {
		s := f.state()
		return len(s.Tombstones) == 2 && s.Checkpoint.Verified != nil
	})
	s := f.state()
	newRender, err := r.checkedRender(context.Background(), s.Checkpoint.Verified)
	if err != nil || len(newRender.bundle.Listeners) != 1 || !reflect.DeepEqual(newRender.bundle.Listeners[0], retained) {
		t.Fatal("wrong identity removed or retained runtime changed", err)
	}
	history, err := f.store.RestrictionResults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []control.BundleListener{expiring, revoked} {
		event := history[l.Authorization.IdentityID]
		if event.TaskID != first.TaskID || event.Cleanup != "confirmed" || event.ConfirmedAt == nil {
			t.Fatal("missing confirmation for original grant")
		}
	}
	if _, restricted := history[retained.Authorization.IdentityID]; restricted {
		t.Fatal("untouched grant received restriction evidence")
	}
}
