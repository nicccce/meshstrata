package domain

import "time"

type TargetDNS struct {
	NodeID    ID       `json:"node_id"`
	Revision  Revision `json:"revision"`
	Upstreams []string `json:"upstreams"`
	Bootstrap []string `json:"bootstrap"`
	Outbound  string   `json:"outbound"`
}
type EgressTarget struct {
	Entity
	NodeID        ID           `json:"node_id"`
	Type          string       `json:"type"`
	InterfaceName *string      `json:"interface_name"`
	Host          *string      `json:"host"`
	Port          *uint16      `json:"port"`
	CredentialRef *ID          `json:"credential_ref"`
	BusinessUDP   Verification `json:"business_udp"`
}
type EgressRule struct {
	Position       int       `json:"position"`
	Destination    string    `json:"destination"`
	TargetID       *ID       `json:"target_id"` // null with reject=true
	TargetRevision *Revision `json:"target_revision"`
	Reject         bool      `json:"reject"`
	NoResolve      bool      `json:"no_resolve"`
}
type EgressPolicy struct {
	Entity
	TerminalNodeID ID           `json:"terminal_node_id"`
	Rules          []EgressRule `json:"rules"`
}
type DomainSet struct {
	Entity
	Identifier  string   `json:"identifier"`
	Domains     []string `json:"domains"`
	TargetGroup string   `json:"target_group"`
}
type RuleLine struct {
	Position int      `json:"position"`
	Type     string   `json:"type"`
	Value    string   `json:"value"`
	Target   string   `json:"target"`
	Options  []string `json:"options"`
}
type RuleProfile struct {
	Entity
	Rules          []RuleLine    `json:"rules"`
	FallbackTarget string        `json:"fallback_target"`
	DomainSets     []RevisionRef `json:"domain_sets"`
}
type Subscription struct {
	Entity
	Profile      RevisionRef `json:"profile"`
	EntryIDs     []ID        `json:"entry_ids"`
	ExpiresAt    time.Time   `json:"expires_at"`
	RevokedAt    *time.Time  `json:"revoked_at"`
	TokenVersion Revision    `json:"token_version"` // never return token/hash in a regular DTO
}
type ReverseTunnel struct {
	ID                   ID         `json:"id"`
	Revision             Revision   `json:"revision"`
	RelayNodeID          ID         `json:"relay_node_id"`
	RemoteNodeID         ID         `json:"remote_node_id"`
	TargetListenerID     ID         `json:"target_listener_id"`
	MappingReservationID ID         `json:"mapping_reservation_id"`
	Transport            string     `json:"transport"` // v1 tcp_tls only
	RelayConfirmedAt     *time.Time `json:"relay_confirmed_at"`
	RemoteConfirmedAt    *time.Time `json:"remote_confirmed_at"`
}
