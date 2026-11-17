package mihomo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	core "github.com/nicccce/meshstrata/internal/mihomo"
	"golang.org/x/sys/unix"
)

func TestOwnedWildcardEvidence(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		for _, mode := range []string{"v4", "dual", "v6", "mapped"} {
			t.Run(transport+"/"+mode, func(t *testing.T) {
				a, candidate := fixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				binary, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				a.o.Binary = binary
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestOwnedSocketChild$", "--", "-d", a.o.WorkDir)
				cmd.Env = append(os.Environ(), "R02_TEST_SOCKET="+transport+"/"+mode)
				cmd.Stderr = os.Stderr
				pipe, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { cmd.Process.Kill(); cmd.Wait() }()
				reader := bufio.NewReader(pipe)
				line, err := reader.ReadString('\n')
				port, portErr := strconv.ParseUint(strings.TrimSpace(line), 10, 16)
				if err != nil || portErr != nil || port == 0 {
					details, _ := io.ReadAll(reader)
					t.Fatalf("socket child did not report a port: %q, %v: %s", line, err, details)
				}
				pid := cmd.Process.Pid
				e := core.Expected{Address: "0.0.0.0", Port: uint16(port), TCP: transport == "tcp", UDP: transport == "udp"}
				sockets, err := inspectSockets(ctx, fmt.Sprintf("/proc/%d", pid), []core.Expected{e})
				if err != nil || len(sockets) != 1 {
					t.Fatalf("owned inventory: %+v, %v", sockets, err)
				}
				actual := sockets[0]
				other := "tcp"
				if transport == other {
					other = "udp"
				}
				want := mode == "v4" || mode == "dual"
				if actual.Matches(e.Address, e.Port, transport) != want ||
					actual.Matches("127.0.0.1", e.Port, transport) ||
					actual.Matches("::ffff:127.0.0.1", e.Port, transport) ||
					actual.Matches(e.Address, e.Port, other) {
					t.Fatalf("wrong scope or transport: %+v", actual)
				}
				if mode == "dual" || mode == "v6" {
					if actual.IPv6Only == nil || *actual.IPv6Only != (mode == "v6") || !actual.Matches("0:0:0:0:0:0:0:0", e.Port, transport) {
						t.Fatalf("missing per-socket family evidence: %+v", actual)
					}
					t.Logf("kernel presented %s:%d; IPV6_V6ONLY=%v", actual.Address, actual.Port, *actual.IPv6Only)
				}
				foreign, err := inspectSockets(ctx, "/proc/self", []core.Expected{e})
				if err != nil {
					t.Fatal(err)
				}
				for _, socket := range foreign {
					if socket.Port == e.Port && socket.Transport == transport {
						t.Fatal("foreign child socket attributed to parent")
					}
				}
				if transport != "tcp" {
					return
				}
				c, expected, err := core.Generate([]core.Entry{{
					NodeID: "node_test", ListenerID: "listener_test", IdentityID: "identity_test", Protocol: core.SS2022,
					Address: e.Address, Port: e.Port, Cipher: "2022-blake3-aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA==",
				}}, core.DNS{Enable: true, EnhancedMode: "normal", Nameserver: []string{"127.0.0.1"}, DefaultNameserver: []string{"127.0.0.1"}}, a.o.SocketPath, []string{"MATCH,REJECT"})
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(candidate.Path, data, 0600); err != nil {
					t.Fatal(err)
				}
				candidate.Digest, candidate.Expected = core.Digest(data), expected
				ev, err := a.Inspect(ctx, candidate, pid)
				if err != nil || ev.ProcessStart == "" || len(ev.Listeners) != 1 ||
					(ev.Listeners[0].Listening.Status == Succeeded) != want {
					t.Fatalf("wrong listener evidence: %+v, %v", ev, err)
				}
				a.o.WorkDir = filepath.Dir(a.o.WorkDir)
				if _, err := a.Inspect(ctx, candidate, pid); err == nil {
					t.Fatal("unrelated argv passed the owner check")
				}
			})
		}
	}
}

func TestOwnedSocketChild(t *testing.T) {
	spec := os.Getenv("R02_TEST_SOCKET")
	if spec == "" {
		return
	}
	transport, mode, _ := strings.Cut(spec, "/")
	var sock io.Closer
	var addr net.Addr
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var sockErr error
		if err := raw.Control(func(fd uintptr) {
			// Restrict even wildcard fixtures to the loopback interface.
			sockErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "lo")
			if sockErr == nil && (mode == "dual" || mode == "v6") {
				only := 0
				if mode == "v6" {
					only = 1
				}
				sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, only)
			}
		}); err != nil {
			return err
		}
		return sockErr
	}}
	network, address := transport+"6", "[::]:0"
	switch mode {
	case "v4":
		network, address = transport+"4", "0.0.0.0:0"
	case "mapped":
		// The standard library converts mapped input to AF_INET. Bind
		// an AF_INET6 socket directly to exercise /proc's mapped form.
		proto := unix.SOCK_STREAM
		if transport == "udp" {
			proto = unix.SOCK_DGRAM
		}
		fd, err := unix.Socket(unix.AF_INET6, proto|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if err := unix.SetsockoptString(fd, unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "lo"); err != nil {
			t.Fatal(err)
		}
		if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0); err != nil {
			t.Fatal(err)
		}
		port := 0
		if transport == "udp" {
			port = testUDPPort(t)
		}
		if err := unix.Bind(fd, &unix.SockaddrInet6{Port: port, Addr: [16]byte{10: 255, 11: 255, 12: 127, 15: 1}}); err != nil {
			t.Fatal(err)
		}
		if transport == "tcp" {
			if err := unix.Listen(fd, 1); err != nil {
				t.Fatal(err)
			}
		}
		sa, err := unix.Getsockname(fd)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(sa.(*unix.SockaddrInet6).Port)
		<-time.After(4 * time.Second)
		return
	}
	if transport == "tcp" {
		l, err := lc.Listen(context.Background(), network, address)
		if err != nil {
			t.Fatal(err)
		}
		sock, addr = l, l.Addr()
	} else {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatal(err)
		}
		l, err := lc.ListenPacket(context.Background(), network, net.JoinHostPort(host, strconv.Itoa(testUDPPort(t))))
		if err != nil {
			t.Fatal(err)
		}
		sock, addr = l, l.LocalAddr()
		outbound, err := net.Dial("udp4", "127.0.0.1:9")
		if err != nil {
			t.Fatal(err)
		}
		defer outbound.Close() // A connected UDP socket must not join the inventory.
	}
	defer sock.Close()
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(port)
	<-time.After(4 * time.Second)
}

func testUDPPort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestUDPInventoryRejectsAutoboundEgress(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	// A send on an unbound, unconnected socket implicitly assigns a port.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	dst := &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}, Port: target.LocalAddr().(*net.UDPAddr).Port}
	if err := unix.Sendto(fd, []byte{1}, 0, dst); err != nil {
		t.Fatal(err)
	}
	local, err := unix.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(local.(*unix.SockaddrInet4).Port)
	sockets, err := inspectSockets(context.Background(), "/proc/self", []core.Expected{{Address: "0.0.0.0", Port: port, UDP: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sockets {
		if p.Matches("0.0.0.0", port, "udp") {
			t.Fatal("implicitly bound UDP egress counted as an ingress")
		}
	}
}
