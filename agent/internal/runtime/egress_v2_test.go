package runtime

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
)

// The proxy fixtures use actual TCP sockets, protocol authentication and CONNECT
// tunnels. They are canaries for the locked renderer, not production egress proof.
func egressFixture(t *testing.T, kind string) (netip.AddrPort, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	calls := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				var target string
				var input io.Reader = conn
				if kind == "http" {
					reader := bufio.NewReader(conn)
					req, err := http.ReadRequest(reader)
					if err != nil || req.Method != "CONNECT" {
						return
					}
					if req.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("mesh:test-secret")) {
						conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
						return
					}
					target, input = req.Host, reader
				} else {
					var hello [2]byte
					if _, err := io.ReadFull(conn, hello[:]); err != nil || hello[0] != 5 {
						return
					}
					methods := make([]byte, int(hello[1]))
					if _, err := io.ReadFull(conn, methods); err != nil {
						return
					}
					conn.Write([]byte{5, 2})
					if _, err := io.ReadFull(conn, hello[:]); err != nil || hello[0] != 1 {
						return
					}
					user := make([]byte, int(hello[1]))
					if _, err := io.ReadFull(conn, user); err != nil {
						return
					}
					var size [1]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						return
					}
					password := make([]byte, int(size[0]))
					if _, err := io.ReadFull(conn, password); err != nil {
						return
					}
					if string(user) != "mesh" || string(password) != "test-secret" {
						conn.Write([]byte{1, 1})
						return
					}
					conn.Write([]byte{1, 0})
					var request [4]byte
					if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 5 || request[1] != 1 {
						return
					}
					address, err := readSocksAddress(conn, request[3])
					if err != nil {
						return
					}
					target = address.String()
				}
				// This fixture can only reach a local echo; it is not a general forwarder.
				destination, err := netip.ParseAddrPort(target)
				if err != nil || !destination.Addr().IsLoopback() {
					return
				}
				upstream, err := net.DialTimeout("tcp", target, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				calls.Add(1)
				if kind == "http" {
					conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				} else {
					conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 2, 0, 0})
				}
				copied := make(chan struct{})
				go func() { io.Copy(upstream, input); upstream.Close(); close(copied) }()
				io.Copy(conn, upstream)
				conn.Close()
				<-copied
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).AddrPort(), calls
}

func TestRealV2AuthenticatedEgressAndReject(t *testing.T) {
	binary := os.Getenv("I03_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("explicit locked-kernel verification required")
	}
	for _, kind := range []string{"http", "socks5"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			endpoint, calls := egressFixture(t, kind)
			b := controltest.Chain()[2]
			b.Listeners[0].Socket.Port = freeEndpoint(t, "ss2022")
			b.Listeners[0].Tail.Hops[0].Endpoint.Port = b.Listeners[0].Socket.Port
			target := control.EgressTarget{NodeID: b.NodeID, Type: kind, Host: endpoint.Addr().String(), DialIP: endpoint.Addr().String(), Port: endpoint.Port(), CredentialRef: controltest.ID("egresscredential")}
			targetPin := controltest.Pin("egresstarget", target)
			policy := control.EgressSpec{NodeID: b.NodeID, DNS: b.DNS.Pin, Targets: []control.PinnedEgressTarget{{Pin: targetPin, Spec: target}}, Rules: []control.EgressRule{{Destination: "127.0.0.1/32", Target: &targetPin, NoResolve: true}, {Destination: "MATCH"}}}
			pin := controltest.Pin("egresspolicy", policy)
			b.Terminal = []control.TerminalV2{{Pin: pin, Spec: policy, Auth: []control.EgressAuth{{Target: targetPin, Ref: target.CredentialRef, Audience: b.NodeID, Username: "mesh", Password: "test-secret"}}}}
			b.Listeners[0].Tail.Egress, b.Listeners[0].Forward.Terminal = pin, &pin
			b.Listeners[0].Internal.Digest, _ = b.Listeners[0].Tail.Key()
			f, r, probes, _, _ := realV2Node(t, binary, b)
			out, err := r.Execute(ctx, f.v2Command(b))
			if err != nil || out.Verification != "verified" || calls.Load() == 0 {
				t.Fatal("authenticated egress did not forward", out, err)
			}
			// A reachable destination outside the allowed /32 must hit final REJECT.
			rejected, err := net.Listen("tcp4", "127.0.0.2:0")
			if err != nil {
				t.Fatal(err)
			}
			defer rejected.Close()
			reached := make(chan struct{}, 1)
			go func() {
				c, err := rejected.Accept()
				if err == nil {
					reached <- struct{}{}
					c.Close()
				}
			}()
			count := calls.Load()
			if got := probes.observe(ctx, transportV2(b.NodeID, b.Listeners[0]), rejected.Addr().String(), "tcp", false); got == forwarded {
				t.Fatal("final REJECT forwarded")
			}
			select {
			case <-reached:
				t.Fatal("REJECT contacted destination")
			default:
			}
			if calls.Load() != count {
				t.Fatal("REJECT contacted selected proxy")
			}
			// Same policy Pin/ref may never acquire a different private auth binding.
			mutated := b
			mutated.TaskID, mutated.TargetRevision = controltest.ID("mutatedegress"), b.TargetRevision+1
			mutated.Terminal = append([]control.TerminalV2{}, b.Terminal...)
			mutated.Terminal[0].Auth = append([]control.EgressAuth{}, b.Terminal[0].Auth...)
			mutated.Terminal[0].Auth[0].Password = "changed-secret"
			out, err = r.Execute(ctx, f.v2Command(mutated))
			if err != nil || out.Issue == nil || out.Issue.Code != "REVISION_CONFLICT" {
				t.Fatal("policy private auth changed under same pin", out, err)
			}
		})
	}
}
