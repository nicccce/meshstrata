// Package mihomo describes the deliberately restricted server configuration used
// by the platform. It does not link, embed, or start a proxy kernel.
package mihomo

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const Version = "v1.19.33"
const LinuxAMD64SHA256 = "19cbe7ba1523a92ed2d0a30716ea093de57db05b7844db23de2b9c21fd7a1afa"

var ErrInvalid = errors.New("MIHOMO_INVALID_CONFIG")

type FieldError struct{ Field string }

func (e *FieldError) Error() string { return "MIHOMO_INVALID_CONFIG: " + e.Field }
func (e *FieldError) Unwrap() error { return ErrInvalid }
func invalid(field string) error    { return &FieldError{Field: field} }

type Protocol string

const (
	SS2022    Protocol = "ss2022"
	Trojan    Protocol = "trojan"
	VLESS     Protocol = "vless"
	Hysteria2 Protocol = "hysteria2"
)

// Entry is one identity on one listener. Sharing remains disabled for all four
// protocols until independent revocation AND established session isolation pass.
// Credentials are private inputs: never serialize Entry into public DTOs/logs.
type Entry struct {
	NodeID, ListenerID, IdentityID        string
	Protocol                              Protocol
	Address                               string
	Port                                  uint16
	BusinessUDP                           bool
	Cipher, Password, UUID                string
	Certificate, PrivateKey               string
	RealityPrivateKey, RealityDestination string
	ServerNames, ShortIDs                 []string
}
type Expected struct {
	NodeID      string   `json:"node_id"`
	ListenerID  string   `json:"listener_id"`
	Protocol    Protocol `json:"protocol"`
	Address     string   `json:"address"`
	Port        uint16   `json:"port"`
	TCP         bool     `json:"tcp"`
	UDP         bool     `json:"udp"`
	BusinessUDP bool     `json:"business_udp"`
}
type DNS struct {
	Enable            bool     `json:"enable"`
	Listen            string   `json:"listen"`
	IPv6              bool     `json:"ipv6"`
	EnhancedMode      string   `json:"enhanced-mode"`
	DefaultNameserver []string `json:"default-nameserver"`
	Nameserver        []string `json:"nameserver"`
}
type User struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
}
type Reality struct {
	Destination string   `json:"dest"`
	PrivateKey  string   `json:"private-key"`
	ShortIDs    []string `json:"short-id"`
	ServerNames []string `json:"server-names"`
}
type Listener struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Listen      string            `json:"listen"`
	Port        uint16            `json:"port"`
	UDP         bool              `json:"udp,omitempty"`
	Cipher      string            `json:"cipher,omitempty"`
	Password    string            `json:"password,omitempty"`
	Users       []User            `json:"users,omitempty"`
	HyUsers     map[string]string `json:"-"`
	Certificate string            `json:"certificate,omitempty"`
	PrivateKey  string            `json:"private-key,omitempty"`
	Reality     *Reality          `json:"reality-config,omitempty"`
}

// Hy2 uses a mapping for users; the remaining protocols use a sequence.
func (l Listener) MarshalJSON() ([]byte, error) {
	type plain Listener
	b, err := json.Marshal(plain(l))
	if err != nil {
		return nil, err
	}
	if l.Type != "hysteria2" {
		return b, nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["users"] = l.HyUsers
	return json.Marshal(m)
}
func (l *Listener) UnmarshalJSON(b []byte) error {
	type plain Listener
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return ErrInvalid
	}
	users := m["users"]
	delete(m, "users")
	rest, _ := json.Marshal(m)
	d := json.NewDecoder(strings.NewReader(string(rest)))
	d.DisallowUnknownFields()
	if d.Decode((*plain)(l)) != nil {
		return ErrInvalid
	}
	if len(users) > 0 {
		if l.Type == "hysteria2" {
			if json.Unmarshal(users, &l.HyUsers) != nil {
				return ErrInvalid
			}
		} else {
			if json.Unmarshal(users, &l.Users) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

type Toggle struct {
	Enable bool `json:"enable"`
}

// Config is a restricted JSON subset accepted as YAML by the fixed kernel.
// The routing compiler is B04/B05 work; Rules must be explicit and only the
// built-in DIRECT and REJECT targets are currently supported here.
type Config struct {
	Port          int             `json:"port"`
	SocksPort     int             `json:"socks-port"`
	MixedPort     int             `json:"mixed-port"`
	RedirPort     int             `json:"redir-port"`
	TProxyPort    int             `json:"tproxy-port"`
	AllowLAN      bool            `json:"allow-lan"`
	Mode          string          `json:"mode"`
	LogLevel      string          `json:"log-level"`
	Tun           Toggle          `json:"tun"`
	IPTables      Toggle          `json:"iptables"`
	DNS           DNS             `json:"dns"`
	Controller    string          `json:"external-controller"`
	ControllerTLS string          `json:"external-controller-tls"`
	Socket        string          `json:"external-controller-unix"`
	Secret        string          `json:"secret"`
	Profile       map[string]bool `json:"profile"`
	Listeners     []Listener      `json:"listeners"`
	Rules         []string        `json:"rules"`
}

func Generate(entries []Entry, dns DNS, socket string, rules []string) (Config, []Expected, error) {
	c := Config{Mode: "rule", LogLevel: "silent", DNS: dns, Socket: socket, Rules: rules, Profile: map[string]bool{"store-selected": false, "store-fake-ip": false}, Listeners: []Listener{}}
	exp := make([]Expected, 0, len(entries))
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, e := range entries {
		if !safeID(e.NodeID) || !safeID(e.ListenerID) || !safeID(e.IdentityID) || names[e.ListenerID] || ids[e.IdentityID] {
			return Config{}, nil, invalid("/entries/id")
		}
		names[e.ListenerID] = true
		ids[e.IdentityID] = true
		l, err := BuildListener(e)
		if err != nil {
			return Config{}, nil, err
		}
		c.Listeners = append(c.Listeners, l)
		exp = append(exp, Expected{e.NodeID, e.ListenerID, e.Protocol, e.Address, e.Port, e.Protocol != Hysteria2, e.Protocol == Hysteria2 || (e.Protocol == SS2022 && e.BusinessUDP), e.BusinessUDP})
	}
	return c, exp, nil
}
func safeID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func BuildListener(e Entry) (Listener, error) {
	if _, err := netip.ParseAddr(e.Address); err != nil || e.Port == 0 {
		return Listener{}, invalid("/entries/address")
	}
	l := Listener{Name: e.ListenerID, Listen: e.Address, Port: e.Port}
	switch e.Protocol {
	case SS2022:
		size := 32
		if e.Cipher == "2022-blake3-aes-128-gcm" {
			size = 16
		} else if e.Cipher != "2022-blake3-aes-256-gcm" && e.Cipher != "2022-blake3-chacha20-poly1305" {
			return l, invalid("/entries/cipher")
		}
		b, err := base64.StdEncoding.DecodeString(e.Password)
		if err != nil || len(b) != size {
			return l, invalid("/entries/password")
		}
		l.Type = "shadowsocks"
		l.Cipher = e.Cipher
		l.Password = e.Password
		l.UDP = e.BusinessUDP
	case Trojan:
		if len(e.Password) < 16 || e.Certificate == "" || e.PrivateKey == "" {
			return l, invalid("/entries/tls")
		}
		l.Type = "trojan"
		l.Users = []User{{Username: e.IdentityID, Password: e.Password}}
		l.Certificate = e.Certificate
		l.PrivateKey = e.PrivateKey
	case VLESS:
		uuid := strings.ReplaceAll(e.UUID, "-", "")
		b, err := hex.DecodeString(uuid)
		if err != nil || len(b) != 16 || len(e.UUID) != 36 || e.UUID[8] != '-' || e.UUID[13] != '-' || e.UUID[18] != '-' || e.UUID[23] != '-' {
			return l, invalid("/entries/uuid")
		}
		k, err := base64.RawURLEncoding.DecodeString(e.RealityPrivateKey)
		if err != nil {
			return l, invalid("/entries/reality")
		}
		if _, err = ecdh.X25519().NewPrivateKey(k); err != nil {
			return l, invalid("/entries/reality")
		}
		host, p, err := net.SplitHostPort(e.RealityDestination)
		n, pe := strconv.Atoi(p)
		if err != nil || host == "" || pe != nil || n < 1 || n > 65535 || len(e.ServerNames) == 0 || len(e.ShortIDs) == 0 {
			return l, invalid("/entries/reality")
		}
		for _, id := range e.ShortIDs {
			b, err := hex.DecodeString(id)
			if err != nil || len(b) == 0 || len(b) > 8 {
				return l, invalid("/entries/short_id")
			}
		}
		for _, s := range e.ServerNames {
			if s == "" || strings.ContainsAny(s, " /\r\n") {
				return l, invalid("/entries/server_names")
			}
		}
		l.Type = "vless"
		l.Users = []User{{Username: e.IdentityID, UUID: e.UUID, Flow: "xtls-rprx-vision"}}
		l.Reality = &Reality{e.RealityDestination, e.RealityPrivateKey, e.ShortIDs, e.ServerNames}
	case Hysteria2:
		if len(e.Password) < 16 || e.Certificate == "" || e.PrivateKey == "" {
			return l, invalid("/entries/tls")
		}
		l.Type = "hysteria2"
		l.HyUsers = map[string]string{e.IdentityID: e.Password}
		l.Certificate = e.Certificate
		l.PrivateKey = e.PrivateKey
	default:
		return l, invalid("/entries/protocol")
	}
	return l, nil
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
