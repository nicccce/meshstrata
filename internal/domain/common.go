// Package domain defines transport-independent v1 contracts shared by Controller and Agent.
// These types contain references to credentials, never credential material.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

type ID string

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}_[0-9a-f]{32}$`)

// NewID is opaque, immutable and independent of names, locale and network location.
func NewID(kind string) (ID, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	id := ID(kind + "_" + hex.EncodeToString(bytes[:]))
	if !id.Valid() {
		return "", fmt.Errorf("invalid ID kind")
	}
	return id, nil
}
func (id ID) Valid() bool { return idPattern.MatchString(string(id)) }

// Revision fits losslessly in JSON/JavaScript. Zero is only the create precondition.
type Revision int64

const MaxRevision Revision = 9007199254740991

func (r Revision) Valid() bool { return r > 0 && r <= MaxRevision }

type ErrorCode string

const (
	InvalidArgument     ErrorCode = "INVALID_ARGUMENT"
	Unauthenticated     ErrorCode = "UNAUTHENTICATED"
	Forbidden           ErrorCode = "FORBIDDEN"
	NotFound            ErrorCode = "NOT_FOUND"
	RevisionConflict    ErrorCode = "REVISION_CONFLICT"
	IdempotencyConflict ErrorCode = "IDEMPOTENCY_CONFLICT"
	PortInUse           ErrorCode = "PORT_IN_USE"
	DependencyInUse     ErrorCode = "DEPENDENCY_IN_USE"
	NotImplemented      ErrorCode = "NOT_IMPLEMENTED"
	TaskExpired         ErrorCode = "TASK_EXPIRED"
)

type FieldError struct {
	Path   string            `json:"path"` // JSON pointer, e.g. /links/0/listener_id
	Code   ErrorCode         `json:"code"`
	Params map[string]string `json:"params,omitempty"` // allowlisted, non-sensitive values only
}
type Problem struct {
	Code            ErrorCode    `json:"code"`
	Fields          []FieldError `json:"fields,omitempty"`
	CurrentRevision *Revision    `json:"current_revision,omitempty"`
}

func (p *Problem) Error() string { return string(p.Code) }

type ErrorResponse struct {
	Error Problem `json:"error"`
}
type Mutation struct {
	ExpectedRevision Revision `json:"expected_revision"`
	IdempotencyKey   string   `json:"idempotency_key"`
}
type ListQuery struct {
	Cursor string
	Limit  int
}

func (q ListQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 200 || len(q.Cursor) > 512 {
		return &Problem{Code: InvalidArgument}
	}
	return nil
}

type Page[T any] struct {
	Items      []T     `json:"items"`       // encode empty lists as [], never null
	NextCursor *string `json:"next_cursor"` // opaque stable-ID cursor, null at end
}
type Entity struct {
	ID        ID        `json:"id"`
	Name      string    `json:"name"`
	Revision  Revision  `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Connection string

const (
	Unknown Connection = "unknown"
	Online  Connection = "online"
	Offline Connection = "offline"
)

type Verification string

const (
	NotChecked Verification = "not_checked"
	Verified   Verification = "verified"
	Failed     Verification = "failed"
	Incomplete Verification = "incomplete"
)

type Protocol string

const (
	SS2022    Protocol = "ss2022"
	Trojan    Protocol = "trojan"
	VLESS     Protocol = "vless"
	Hysteria2 Protocol = "hysteria2"
)

type Node struct {
	Entity
	ConnectionMode    string       `json:"connection_mode"` // direct / reverse_tunnel
	Role              string       `json:"role"`            // node / both
	ControlAddress    string       `json:"control_address"`
	ProxyAddress      *string      `json:"proxy_address"`
	Control           Connection   `json:"control"`
	Tunnel            Connection   `json:"tunnel"`
	Verification      Verification `json:"verification"`
	TargetDNSRevision Revision     `json:"target_dns_revision"`
	LastConfirmedAt   *time.Time   `json:"last_confirmed_at"`
}
type Listener struct {
	Entity
	NodeID         ID           `json:"node_id"`
	Protocol       Protocol     `json:"protocol"`
	ListenAddress  string       `json:"listen_address"`
	ListenPort     uint16       `json:"listen_port"`
	OuterTransport string       `json:"outer_transport"` // tcp / udp / tcp_udp; Hy2 outer UDP != business UDP
	BusinessUDP    Verification `json:"business_udp"`
	CredentialRef  ID           `json:"credential_ref"`
	ConfigDigest   string       `json:"config_digest"` // includes next hop, DNS and egress revisions
}
type RevisionRef struct {
	ID       ID       `json:"id"`
	Revision Revision `json:"revision"`
}
type RouteRevision struct {
	RouteID         ID           `json:"route_id"`
	Revision        Revision     `json:"revision"`
	EntryListenerID ID           `json:"entry_listener_id"`
	NodeIDs         []ID         `json:"node_ids"` // fixed forwarding order, never auto-routed
	EgressPolicy    *RevisionRef `json:"egress_policy"`
	ConfigDigest    string       `json:"config_digest"`
}
type SubscriptionEntry struct {
	IdentityID ID `json:"identity_id"` // immutable authentication generation; entry ID stays stable
	Entity
	SubscriptionID ID         `json:"subscription_id"`
	RouteID        ID         `json:"route_id"`
	ListenerID     ID         `json:"listener_id"`
	CredentialRef  ID         `json:"credential_ref"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	Cleanup        string     `json:"cleanup"`
}

const MeasurementSuccessTTL = 96 * time.Hour
const MeasurementFailureTTL = 5 * time.Minute

type Measurement struct {
	ID          ID        `json:"id"`
	Scope       string    `json:"scope"`
	Method      string    `json:"method"`
	Kind        string    `json:"kind"` // network_rtt / browser_http / path_cost
	Mode        string    `json:"mode"` // network_rtt / browser_estimate / partial
	Transport   string    `json:"transport"`
	ValueMS     *float64  `json:"value_ms"` // unknown MUST remain null
	Samples     int       `json:"samples"`
	Environment string    `json:"environment"`
	Revision    Revision  `json:"revision"`
	StartedAt   time.Time `json:"started_at"`
	ObservedAt  time.Time `json:"observed_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}
