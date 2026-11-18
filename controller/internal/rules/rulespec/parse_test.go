package rulespec

import (
	"strings"
	"testing"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestRuleSourcePositionsAndOrder(t *testing.T) {
	source := "# comment\r\nrules:\r\n  - \"MATCH,All-Proxies\"\r\n  - 'DOMAIN,例子.测试,All-Proxies'\r\n  - IP-CIDR,192.0.2.22/24,REJECT,no-resolve\r\n  - IP-CIDR6,2001:db8::3/64,REJECT,no-resolve"
	got, issues := ParseSource(source)
	if len(issues) != 0 || len(got) != 4 {
		t.Fatalf("got %d rules and %+v", len(got), issues)
	}
	want := []string{"xn--fsqu00a.xn--0zwm56d", "192.0.2.0/24", "2001:db8::/64", ""}
	lines := []int{4, 5, 6, 3}
	cols := []int{6, 5, 5, 6}
	for i, r := range got {
		if r.Value != want[i] || r.Line != lines[i] || r.Column != cols[i] || r.NoResolve != (i == 1 || i == 2) {
			t.Fatalf("rule %d mismatch: %+v", i, r)
		}
	}
	for _, tc := range []struct {
		name, source string
		line, col    int
		code         domain.ErrorCode
	}{
		{"utf8 bytes", "rules:\n  - DOMAIN,例子.cn,SECRET_BAD_TARGET\n  - MATCH,REJECT", 2, 22, domain.InvalidArgument},
		{"unsupported option", "IP-CIDR,192.0.2.0/24,REJECT,other\nMATCH,REJECT", 1, 29, domain.NotImplemented},
		{"unsupported kind", "PROCESS-NAME,app,REJECT\nMATCH,REJECT", 1, 1, domain.NotImplemented},
		{"yaml key", "rules:\n  - MATCH,REJECT\nexternal-controller: evil", 3, 1, domain.InvalidArgument},
		{"yaml directive", "rules:\n  - !!str MATCH,REJECT", 2, 5, domain.InvalidArgument},
		{"yaml alias", "rules:\n  - *alias", 2, 5, domain.InvalidArgument},
		{"yaml flow", "rules: [MATCH,REJECT]", 1, 1, domain.InvalidArgument},
		{"no implicit fallback", "DOMAIN,example.com,REJECT", 1, 1, matchRequired},
		{"malformed UTF-8", "# ok\n  # \xff\nMATCH,REJECT", 2, 5, domain.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, issues := ParseSource(tc.source)
			if len(got) != 0 || len(issues) != 1 || issues[0] != issue(tc.line, tc.col, tc.code) {
				t.Fatalf("expected precise issue, got %+v", issues)
			}
		})
	}
	_, multiple := ParseSource("MATCH,REJECT\nDOMAIN,example.com,REJECT\n MATCH,All-Proxies")
	if len(multiple) != 2 || multiple[0] != issue(1, 1, multipleMatch) || multiple[1] != issue(3, 2, multipleMatch) {
		t.Fatalf("expected each MATCH to be marked: %+v", multiple)
	}
	exact := "MATCH,REJECT\n#" + strings.Repeat("x", stage3.MaxRuleSourceBytes-len("MATCH,REJECT\n#"))
	if got, issues := ParseSource(exact); len(got) != 1 || len(issues) != 0 {
		t.Fatal("exact limit rejected")
	}
	if got, issues := ParseSource(exact + "x"); got != nil || len(issues) != 1 || issues[0].Line != 2 {
		t.Fatal("oversize source accepted")
	}
}

func TestDomainSyntax(t *testing.T) {
	for _, d := range []string{"127.0.0.1", "0177.1", "0x7f000001", "::1", "*.example.com", "https://example.com", "x,y", "a b", "a..com", "b.test.", "xn--.net", ""} {
		if _, ok := CanonicalDomain(d); ok {
			t.Errorf("accepted %q", d)
		}
	}
	for raw, want := range map[string]string{" WWW.Example.Com ": "www.example.com", "例子.测试": "xn--fsqu00a.xn--0zwm56d", "XN--FSQU00A.cn": "xn--fsqu00a.cn"} {
		if got, ok := CanonicalDomain(raw); !ok || got != want {
			t.Errorf("canonical domain %q: %q (%t)", raw, got, ok)
		}
	}
}
