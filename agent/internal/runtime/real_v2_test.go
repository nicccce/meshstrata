package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

func realV2Node(t *testing.T, binary string, b control.BundleV2) (*fixture, *Runtime, *Probes, *Supervisor, *mihomo.Adapter) {
	return realV2NodeConfigured(t, binary, b, nil)
}

func realV2NodeConfigured(t *testing.T, binary string, b control.BundleV2, configure func(*fixture)) (*fixture, *Runtime, *Probes, *Supervisor, *mihomo.Adapter) {
	t.Helper()
	f := newNodeFixture(t, b.NodeID)
	v2Options(f)
	if configure != nil {
		configure(f)
	}
	socketDir, err := os.MkdirTemp("", "i03-v2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	f.opts.SocketPath = filepath.Join(socketDir, "api.sock")
	adapter, err := mihomo.New(mihomo.Options{EnableV2: true, Binary: binary, BinarySHA256: core.LinuxAMD64SHA256, WorkDir: f.opts.ConfigDir, StateDir: f.opts.ConfigDir, ConfigDir: f.opts.ConfigDir, SocketPath: f.opts.SocketPath, Secret: f.opts.APISecret, Timeout: 9 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	process, err := NewProcess(ProcessOptions{Binary: binary, WorkDir: f.opts.ConfigDir, StateDir: f.opts.ConfigDir, ConfigDir: f.opts.ConfigDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := process.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	probes, err := NewProbes(ProbeOptions{Binary: binary, Dir: privateTestDir(t), Materials: f.opts.Materials, Timeout: 3500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Kernel, f.opts.Process = adapter, process
	f.opts.ProbeFactory, f.opts.RevocationProbeFactory = probes.ForwardFactory, probes.RevocationFactory
	f.opts.ProbeFactoryV2 = func(ctx context.Context, b control.BundleV2, c mihomo.Candidate) (mihomo.Probe, error) {
		p, err := probes.ForwardFactoryV2(ctx, b, c)
		return measuredProbe{p: p, t: t}, err
	}
	f.opts.OnApplyFailure = func(err error) { t.Log("apply failure:", err) }
	return f, launch(t, f.opts), probes, process, adapter
}

func localChain(t *testing.T) [3]control.BundleV2 {
	t.Helper()
	chain := controltest.Chain()
	ports := []uint16{freeEndpoint(t, domain.SS2022), freeEndpoint(t, domain.SS2022), freeEndpoint(t, domain.SS2022)}
	for i := range chain {
		l := &chain[i].Listeners[0]
		l.Socket.Port = ports[i]
		for j := range l.Tail.Hops {
			l.Tail.Hops[j].Endpoint.Port = ports[i+j]
		}
		if l.Internal != nil {
			l.Internal.Digest, _ = l.Tail.Key()
		}
	}
	for i := 0; i < 2; i++ {
		chain[i].Listeners[0].Forward.Next.Link = *chain[i+1].Listeners[0].Internal
	}
	for _, b := range chain {
		if b.Validate() != nil {
			t.Fatal("invalid local chain")
		}
	}
	return chain
}

func holdV2(t *testing.T, ctx context.Context, p *Probes, node domain.ID, l control.ListenerV2, echo string) net.Conn {
	t.Helper()
	for range 3 {
		proxy, err := p.proxy(ctx, transportV2(node, l), false)
		if err != nil {
			t.Fatal(err)
		}
		client, err := p.startClient(ctx, proxy)
		if err != nil {
			continue
		}
		conn, err := client.connect(ctx)
		if err == nil {
			rep, _, err := socksRequest(conn, 1, echo)
			if err == nil && rep == 0 && roundTrip(conn) {
				t.Cleanup(client.stop)
				t.Cleanup(func() { conn.Close() })
				return conn
			}
			conn.Close()
		}
		client.stop()
	}
	t.Fatal("three-hop held connection did not forward")
	return nil
}

func TestRealV2ThreeNodesRestartAndOfflineExpiry(t *testing.T) {
	binary := os.Getenv("I03_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("explicit locked-kernel verification required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	chain := localChain(t)
	var fixtures [3]*fixture
	var runtimes [3]*Runtime
	var probes [3]*Probes
	var processes [3]*Supervisor
	var adapters [3]*mihomo.Adapter
	// Receiver-first execution uses three actual independent kernel processes.
	for i := 2; i >= 0; i-- {
		fixtures[i], runtimes[i], probes[i], processes[i], adapters[i] = realV2Node(t, binary, chain[i])
		if i == 0 {
			chain[i].Listeners[0].Entry.ExpiresAt = fixtures[i].now.Add(time.Hour)
			second := chain[i].Listeners[0]
			authorization := *second.Entry
			authorization.EntryID, authorization.IdentityID, authorization.ExpiresAt = controltest.ID("entrytwo"), controltest.ID("identitytwo"), fixtures[i].now.Add(2*time.Hour)
			second.Entry = &authorization
			second.Socket.ListenerID, second.Socket.Port = controltest.ID("listenertwo"), freeEndpoint(t, domain.SS2022)
			second.Socket.Credential.Ref, second.Socket.Credential.Password = controltest.ID("credentialtwo"), randomPassword(t)
			chain[i].Listeners = append(chain[i].Listeners, second)
		}
		out, err := runtimes[i].Execute(ctx, fixtures[i].v2Command(chain[i]))
		if err != nil || out.Verification != domain.Verified {
			t.Fatalf("node %d real execution: %+v %v", i, out, err)
		}
	}
	relayStatus, err := processes[1].Status(ctx)
	if err != nil || !relayStatus.Running {
		t.Fatal(err)
	}
	runtimes[1].Close()
	fixtures[1].reopen()
	fixtures[1].opts.State = fixtures[1].store
	runtimes[1] = launch(t, fixtures[1].opts)
	if status, err := processes[1].Status(ctx); err != nil || status != relayStatus {
		t.Fatal("agent recovery replaced healthy relay process", err)
	}
	echo, closeEcho, err := echoTarget(ctx, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer closeEcho()
	oldConn := holdV2(t, ctx, probes[0], chain[0].NodeID, chain[0].Listeners[0], echo)
	retainedConn := holdV2(t, ctx, probes[0], chain[0].NodeID, chain[0].Listeners[1], echo)
	ingressStatus, err := processes[0].Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixtures[0].clock.set(chain[0].Listeners[0].Entry.ExpiresAt)
	waitFor(t, 12*time.Second, func() bool {
		for _, out := range fixtures[0].state().Outbox {
			if len(out.Restrictions) > 0 && out.Cleanup == "confirmed" {
				return true
			}
		}
		return false
	})
	if roundTrip(oldConn) {
		t.Fatal("expired three-hop connection survived")
	}
	if !roundTrip(retainedConn) {
		t.Fatal("retained three-hop connection was interrupted")
	}
	if status, err := processes[0].Status(ctx); err != nil || status != ingressStatus {
		t.Fatal("expiry restarted ingress kernel", err)
	}
	// Fresh traffic cannot silently fall back to direct when the middle hop dies.
	if err := processes[1].Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := probes[0].observe(ctx, transportV2(chain[0].NodeID, chain[0].Listeners[1]), echo, "tcp", false); got == forwarded {
		t.Fatal("route bypassed stopped relay")
	}
	t.Log("three real kernels: receiver-first publish, PID-preserving recovery, precise expiry with a retained connection, and no direct fallback")
}
