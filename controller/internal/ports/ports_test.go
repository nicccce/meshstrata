package ports

import (
	"net/netip"
	"testing"
)

func TestFixedBindingConflicts(t *testing.T) {
	for _, tc := range []struct {
		a, b     string
		trans    string
		conflict bool
	}{
		{"0.0.0.0", "192.0.2.1", "tcp", true},
		{"0.0.0.0", "2001:db8::1", "tcp", false},
		{"::", "192.0.2.1", "tcp", true},
		{"::", "2001:db8::1", "tcp", true},
		{"::", "192.0.2.1", "udp", false},
		{"192.0.2.1", "192.0.2.2", "tcp", false},
		{"2001:db8::1", "2001:db8::2", "tcp", false},
		{"2001:db8::1", "2001:db8::1", "tcp", true},
	} {
		a := Binding{Address: netip.MustParseAddr(tc.a), Port: 25000, Transport: "tcp"}
		b := Binding{Address: netip.MustParseAddr(tc.b), Port: 25000, Transport: tc.trans}
		if a.Conflicts(b) != tc.conflict || b.Conflicts(a) != tc.conflict {
			t.Fatalf("%+v", tc)
		}
		b.Port++
		if a.Conflicts(b) {
			t.Fatal("different ports overlap")
		}
	}
}
