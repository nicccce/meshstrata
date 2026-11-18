// Package rulespec parses and validates typed rule data without storage or I/O.
package rulespec

import (
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"golang.org/x/net/idna"
)

const (
	MaxMembers    = 4096
	MaxSets       = 128
	MaxRules      = 8192
	MaxSetBytes   = stage3.MaxRuleSourceBytes
	matchRequired = domain.ErrorCode("MATCH_REQUIRED")
	multipleMatch = domain.ErrorCode("MULTIPLE_MATCH")
)

// CanonicalDomain accepts ASCII (including validated IDNA A-labels) or converts
// a Unicode domain with the IDNA lookup profile. These are DNS names, not URLs.
func CanonicalDomain(input string) (string, bool) {
	s := strings.Trim(input, " \t")
	if len(s) == 0 || len(s) > 1024 || !utf8.ValidString(s) ||
		strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return "", false
	}
	ascii, err := idna.Lookup.ToASCII(s)
	ascii = strings.ToLower(ascii)
	if err != nil || !control.DomainName(ascii) {
		return "", false
	}
	// Exclude legacy IPv4 spelling accepted by some consumers: decimal, octal,
	// hexadecimal and abbreviated components. Never treat it as DNS input.
	numeric := true
	for _, label := range strings.Split(ascii, ".") {
		digits, alphabet := label, "0123456789"
		if strings.HasPrefix(label, "0x") {
			digits, alphabet = label[2:], "0123456789abcdef"
		}
		if digits == "" || strings.ContainsFunc(digits, func(r rune) bool { return !strings.ContainsRune(alphabet, r) }) {
			numeric = false
			break
		}
	}
	if numeric {
		return "", false
	}
	return ascii, true
}

func NormalizeDomainSet(spec stage3.DomainSetSpec) (stage3.DomainSetSpec, bool) {
	if len(spec.Domains) == 0 || len(spec.Domains) > MaxMembers {
		return stage3.DomainSetSpec{}, false
	}
	out := stage3.DomainSetSpec{Domains: make([]string, 0, len(spec.Domains))}
	seen := make(map[string]bool, len(spec.Domains))
	size := 0
	for _, raw := range spec.Domains {
		d, ok := CanonicalDomain(raw)
		if !ok {
			return stage3.DomainSetSpec{}, false
		}
		if !seen[d] {
			size += len(d) + 1
			if size > MaxSetBytes {
				return stage3.DomainSetSpec{}, false
			}
			seen[d] = true
			out.Domains = append(out.Domains, d)
		}
	}
	return out, true
}

func ParseTarget(s string) (stage3.RuleTarget, bool) {
	id := domain.ID(s)
	switch {
	case s == "DIRECT" || s == "REJECT":
		return stage3.RuleTarget{Builtin: s}, true
	case id.Valid():
		return stage3.RuleTarget{RouteID: id}, true
	case s == stage3.AllProxies:
		return stage3.RuleTarget{Group: s}, true
	case strings.HasPrefix(s, "Exit-"):
		node, policy, custom := strings.Cut(s[len("Exit-"):], "-Egress-")
		expected, err := stage3.ExitGroupName(domain.ID(node), domain.ID(policy))
		if !(custom && policy == "") && err == nil && expected == s {
			return stage3.RuleTarget{Group: s}, true
		}
	}
	return stage3.RuleTarget{}, false
}

func TargetText(target stage3.RuleTarget) string {
	return target.Builtin + string(target.RouteID) + target.Group
}

func ruleValue(kind, value string) (string, bool) {
	switch kind {
	case "DOMAIN", "DOMAIN-SUFFIX":
		return CanonicalDomain(value)
	case "DOMAIN-KEYWORD":
		v := strings.ToLower(value)
		return v, len(v) >= 1 && len(v) <= 253 && !strings.ContainsFunc(v, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.')
		})
	case "IP-CIDR", "IP-CIDR6":
		p, err := netip.ParsePrefix(value)
		if err != nil || p.Addr().Is4In6() || p.Addr().Is4() != (kind == "IP-CIDR") {
			return "", false
		}
		return p.Masked().String(), true
	case "RULE-SET":
		return value, domain.ID(value).Valid()
	case "MATCH":
		return "", value == ""
	}
	return "", false
}

type field struct {
	text string
	col  int
}

func fields(s string, off int) []field {
	parts := make([]field, 0, 5)
	for len(parts) < 5 {
		part, rest, more := strings.Cut(s, ",")
		left := strings.TrimLeft(part, " \t")
		parts = append(parts, field{strings.TrimRight(left, " \t"), off + len(part) - len(left) + 1})
		if !more {
			break
		}
		off += len(part) + 1
		s = rest
	}
	return parts
}

func issue(line, col int, code domain.ErrorCode) stage3.Issue {
	return stage3.Issue{Path: "/source", Line: line, Column: col, Code: code}
}

// ParseSource deliberately accepts a scalar grammar, never evaluates YAML.
func ParseSource(source string) ([]stage3.RuleLine, []stage3.Issue) {
	if len(source) > stage3.MaxRuleSourceBytes {
		p := source[:stage3.MaxRuleSourceBytes]
		return nil, []stage3.Issue{issue(strings.Count(p, "\n")+1, len(p)-strings.LastIndexByte(p, '\n'), domain.InvalidArgument)}
	}
	var rules, matches []stage3.RuleLine
	var issues []stage3.Issue
	yaml, started, indent := false, false, -1
	for i, line := range strings.Split(source, "\n") {
		n := i + 1
		line = strings.TrimSuffix(line, "\r")
		badChar := false
		for pos := 0; pos < len(line); {
			r, width := utf8.DecodeRuneInString(line[pos:])
			if r == utf8.RuneError && width == 1 || unicode.IsControl(r) && r != '\t' {
				issues = append(issues, issue(n, pos+1, domain.InvalidArgument))
				badChar = true
				break
			}
			pos += width
		}
		if badChar {
			continue
		}
		s := strings.TrimLeft(line, " \t")
		off := len(line) - len(s)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if !started && off == 0 && strings.TrimRight(s, " \t") == "rules:" {
			yaml, started = true, true
			continue
		}
		started = true
		if yaml {
			if strings.Contains(line[:off], "\t") || !strings.HasPrefix(s, "- ") || indent >= 0 && indent != off {
				issues = append(issues, issue(n, off+1, domain.InvalidArgument))
				continue
			}
			indent = off
			after := strings.TrimLeft(s[2:], " \t")
			off += len(s) - len(after)
			s = after
		}
		if s != "" && (s[0] == '"' || s[0] == '\'') {
			end := strings.IndexByte(s[1:], s[0]) + 1
			if end == 0 || strings.Trim(s[end+1:], " \t") != "" {
				issues = append(issues, issue(n, off+1, domain.InvalidArgument))
				continue
			}
			s, off = s[1:end], off+1
		}
		f := fields(s, off)
		kind := f[0].text
		bad := func(col int, code domain.ErrorCode) { issues = append(issues, issue(n, col, code)) }
		min, max := 3, 3
		switch kind {
		case "MATCH":
			min, max = 2, 2
		case "IP-CIDR", "IP-CIDR6":
			max = 4
		case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "RULE-SET":
		default:
			code := domain.InvalidArgument
			if kind != "" && !strings.ContainsFunc(kind, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r == '-' || r >= '0' && r <= '9') }) {
				code = domain.NotImplemented
			}
			bad(f[0].col, code)
			continue
		}
		if len(f) < min {
			bad(off+len(s)+1, domain.InvalidArgument)
			continue
		}
		if len(f) > max {
			bad(f[max].col, domain.NotImplemented)
			continue
		}
		value := ""
		if kind != "MATCH" {
			var ok bool
			value, ok = ruleValue(kind, f[1].text)
			if !ok {
				bad(f[1].col, domain.InvalidArgument)
				continue
			}
		}
		target, ok := ParseTarget(f[min-1].text)
		if !ok {
			bad(f[min-1].col, domain.InvalidArgument)
			continue
		}
		noResolve := len(f) == 4
		if noResolve && f[3].text != "no-resolve" {
			bad(f[3].col, domain.NotImplemented)
			continue
		}
		r := stage3.RuleLine{Kind: kind, Value: value, Target: target, NoResolve: noResolve, Line: n, Column: f[0].col}
		if kind == "MATCH" {
			matches = append(matches, r)
		} else {
			rules = append(rules, r)
		}
		if len(rules)+len(matches) > MaxRules {
			return nil, []stage3.Issue{issue(n, f[0].col, domain.InvalidArgument)}
		}
	}
	if len(matches) == 0 && len(issues) == 0 {
		issues = append(issues, issue(strings.Count(source, "\n")+1, 1, matchRequired))
	}
	if len(matches) > 1 {
		for _, r := range matches {
			issues = append(issues, issue(r.Line, r.Column, multipleMatch))
		}
	}
	if len(issues) != 0 {
		return nil, issues
	}
	return append(rules, matches[0]), nil
}

func ValidateDomainSet(s stage3.DomainSetSnapshot) bool {
	canonical, ok := NormalizeDomainSet(s.Spec)
	if !ok || len(canonical.Domains) != len(s.Spec.Domains) {
		return false
	}
	for i, d := range canonical.Domains {
		if d != s.Spec.Domains[i] {
			return false
		}
	}
	p, err := stage3.NewPin(s.Pin.ID, s.Pin.Revision, s.Spec)
	return err == nil && p == s.Pin
}

// ValidateProfile checks more than the digest: all embedded sets must be
// canonical, used, unique by ID and referenced by the exact Pin in each rule.
func ValidateProfile(s stage3.ProfileSnapshot) bool {
	if len(s.Spec.Rules) == 0 || len(s.Spec.Rules) > MaxRules || len(s.Spec.Sets) > MaxSets {
		return false
	}
	sets := make(map[domain.ID]control.Pin, len(s.Spec.Sets))
	used := make(map[domain.ID]bool, len(sets))
	total := 0
	for _, set := range s.Spec.Sets {
		if _, exists := sets[set.Pin.ID]; exists || !ValidateDomainSet(set) {
			return false
		}
		sets[set.Pin.ID] = set.Pin
		for _, d := range set.Spec.Domains {
			total += len(d) + 1
		}
		if total > MaxSetBytes {
			return false
		}
	}
	locations := map[int]bool{}
	last := 0
	for i, r := range s.Spec.Rules {
		value, valueOK := ruleValue(r.Kind, r.Value)
		target, targetOK := ParseTarget(TargetText(r.Target))
		match := i == len(s.Spec.Rules)-1
		if !valueOK || value != r.Value || !targetOK || target != r.Target ||
			match != (r.Kind == "MATCH") || r.Line <= 0 || r.Line > stage3.MaxRuleSourceBytes ||
			r.Column <= 0 || r.Column > stage3.MaxRuleSourceBytes || locations[r.Line] || !match && r.Line <= last ||
			r.NoResolve && r.Kind != "IP-CIDR" && r.Kind != "IP-CIDR6" {
			return false
		}
		locations[r.Line], last = true, r.Line
		if r.Kind == "RULE-SET" {
			p, exists := sets[domain.ID(r.Value)]
			if !exists || r.DomainSet == nil || *r.DomainSet != p {
				return false
			}
			used[p.ID] = true
		} else if r.DomainSet != nil {
			return false
		}
	}
	if len(used) != len(sets) {
		return false
	}
	p, err := stage3.NewPin(s.Pin.ID, s.Pin.Revision, s.Spec)
	return err == nil && p == s.Pin
}
