package mihomo

import (
	"context"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Linux UAPI: include/uapi/linux/{sock_diag,inet_diag}.h.
const (
	sockDiagByFamily = 20
	inetDiagV6Only   = 11
	inetDiagReqSize  = 56
	inetDiagMsgSize  = 72
)

type diagSocket struct {
	local netip.AddrPort
	inode uint32
}

func sameNetNS(base string) bool {
	self, err := os.Stat("/proc/self/ns/net")
	other, otherErr := os.Stat(filepath.Join(base, "ns", "net"))
	return err == nil && otherErr == nil && os.SameFile(self, other)
}

type diagFacts struct {
	found  bool
	v6only *bool
}

func socketKey(ep Endpoint, rawInode string) (diagSocket, bool) {
	addr, err := netip.ParseAddr(ep.Address)
	inode, inodeErr := strconv.ParseUint(rawInode, 10, 32)
	if err != nil || inodeErr != nil || inode == 0 {
		return diagSocket{}, false
	}
	return diagSocket{netip.AddrPortFrom(addr, ep.Port), uint32(inode)}, true
}

// Only a complete kernel dump in the selected protocol and network namespace
// can add coverage. The result augments the caller's PID/FD-inode inventory.
func socketFacts(ctx context.Context, table string, wanted map[diagSocket]diagFacts) error {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	proto, state := byte(unix.IPPROTO_TCP), byte(10) // TCP_LISTEN
	family := byte(unix.AF_INET6)
	if table == "udp" {
		family = unix.AF_INET
	}
	if table != "tcp6" {
		proto, state = unix.IPPROTO_UDP, 7 // TCP_CLOSE: unconnected UDP
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return ErrInspection
	}
	defer unix.Close(fd)
	native := binary.NativeEndian
	req := make([]byte, unix.NLMSG_HDRLEN+inetDiagReqSize)
	if proto == unix.IPPROTO_UDP {
		// INET_DIAG_REQ_BYTECODE: AUTO tests absence of an explicit
		// port bind. Jump past the program on AUTO to reject that socket.
		req = native.AppendUint16(req, 12)
		req = native.AppendUint16(req, 1)
		req = append(req, 6, 4) // INET_DIAG_BC_AUTO
		req = native.AppendUint16(req, 8)
		req = append(req, 1, 4) // INET_DIAG_BC_JMP
		req = native.AppendUint16(req, 8)
	}
	native.PutUint32(req, uint32(len(req)))
	native.PutUint16(req[4:], sockDiagByFamily)
	native.PutUint16(req[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	native.PutUint32(req[8:], 1) // one request on a new socket
	body := req[unix.NLMSG_HDRLEN:]
	body[0], body[1] = family, proto
	native.PutUint32(body[4:], 1<<state)
	if err = unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return ErrInspection
	}
	var buf [64 << 10]byte
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for batches := 0; batches < 256; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, _, flags, from, err := unix.Recvmsg(fd, buf[:], nil, 0)
		if err == unix.EAGAIN {
			if _, err := unix.Poll(poll, 20); err == nil || err == unix.EINTR {
				continue
			}
		}
		if err == unix.EINTR {
			continue
		}
		sender, ok := from.(*unix.SockaddrNetlink)
		if err != nil || flags&unix.MSG_TRUNC != 0 || !ok || sender.Pid != 0 {
			return ErrInspection
		}
		batches++
		messages, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return ErrInspection
		}
		for _, msg := range messages {
			if msg.Header.Seq != 1 || msg.Header.Flags&unix.NLM_F_DUMP_INTR != 0 {
				return ErrInspection
			}
			switch msg.Header.Type {
			case unix.NLMSG_DONE:
				if len(msg.Data) >= 4 && native.Uint32(msg.Data) == 0 {
					return nil
				}
				return ErrInspection
			case sockDiagByFamily:
				d := msg.Data
				if len(d) < inetDiagMsgSize || d[0] != family || d[1] != state ||
					d[6] != 0 || d[7] != 0 || [16]byte(d[24:40]) != [16]byte{} {
					continue
				}
				addr := netip.AddrFrom16([16]byte(d[8:24]))
				if family == unix.AF_INET {
					addr = netip.AddrFrom4([4]byte(d[8:12]))
				}
				key := diagSocket{netip.AddrPortFrom(addr, binary.BigEndian.Uint16(d[4:])), native.Uint32(d[68:])}
				if _, ok := wanted[key]; ok {
					wanted[key] = diagFacts{true, diagIPv6Only(d[inetDiagMsgSize:])}
				}
			default:
				return ErrInspection
			}
		}
	}
	return ErrInspection
}

func diagIPv6Only(attrs []byte) *bool {
	for len(attrs) >= 4 {
		n := int(binary.NativeEndian.Uint16(attrs))
		aligned := (n + 3) &^ 3
		if n < 4 || aligned > len(attrs) {
			return nil
		}
		if binary.NativeEndian.Uint16(attrs[2:]) == inetDiagV6Only {
			if n != 5 || attrs[4] > 1 {
				return nil
			}
			only := attrs[4] == 1
			return &only
		}
		attrs = attrs[aligned:]
	}
	return nil
}
