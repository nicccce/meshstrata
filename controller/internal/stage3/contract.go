// Package stage3 freezes the Controller-only, in-process contracts for B06–B08.
// Public HTTP DTOs require explicit projections. The Agent uses internal/control.
package stage3

import (
	"context"
	"io"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/internal/adminauth"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

const MaxRuleSourceBytes = 128 << 10
const MaxConfigBytes = 1 << 20
const DownloadTokenBytes = 32

// Runtime is embedded in each Options. DB is the single Controller database.
// Nil clock, IDs and entropy default to time.Now, domain.NewID and crypto/rand.Reader.
type Runtime struct {
	DB     storage.Database
	Now    func() time.Time
	NewID  func(kind string) (domain.ID, error)
	Random io.Reader
}

type PublishedRoute struct {
	Pin             control.Pin
	Name            string
	Tail            control.RouteTail
	Verification    domain.Verification
	EgressIsDefault bool // only the frozen node-default policy, never a custom policy
	// No subscription credential, port allocation or identity on a route view.
}

type RouteDraft struct {
	ID              domain.ID // empty only on create
	Name            string
	Hops            []HopDraft
	Egress          control.Pin
	EgressIsDefault bool
	BusinessUDP     bool
}
type HopDraft struct {
	NodeID     domain.ID
	NodeConfig control.Pin
	DNS        control.Pin
	Endpoint   control.ProxyEndpoint // port 0 requests bounded pool allocation
	DialIP     string
}
type DraftReview struct {
	Draft    control.Pin
	Nodes    []domain.ID // fixed order
	Blocking []Issue
	Warnings []Issue
}
type Issue struct {
	Path   string           // JSON pointer
	Line   int              // 1-based source line; 0 when not applicable
	Column int              // 1-based UTF-8 byte column
	Code   domain.ErrorCode // no raw input or secret
}
type EgressDraft struct {
	ID   domain.ID
	Name string
	Spec control.EgressSpec
}

type EntryRoutes interface {
	PublishedRoute(context.Context, control.Pin) (PublishedRoute, error)
	ReserveIngress(context.Context, EntryReservation) (IngressLease, error)
	ApplyEntry(context.Context, EntryChange) (domain.TaskAccepted, error)
}

type Routes interface {
	EntryRoutes
	SaveDraft(context.Context, domain.Mutation, RouteDraft) (control.Pin, error)
	ValidateDraft(context.Context, control.Pin) (DraftReview, error)
	PublishDraft(context.Context, domain.Mutation, control.Pin, time.Time) (domain.TaskAccepted, error)
	ReconcilePublication(context.Context, domain.ID) (ReleaseState, error) // persisted task ID
}

// EgressTargetInput is the authorized write input only. Auth replaces credentials;
// nil means retain the specified owned ref (or remain anonymous if empty).
type EgressTargetInput struct {
	ID   domain.ID
	Name string
	Spec control.EgressTarget
	Auth *ProxyAuth
}
type ProxyAuth struct{ Username, Password string }

type EgressOptions struct {
	Runtime
	Cipher secretstore.Cipher
}
type Egress interface {
	SaveTarget(context.Context, domain.Mutation, EgressTargetInput) (control.Pin, error)
	SavePolicy(context.Context, domain.Mutation, EgressDraft) (control.Pin, error)
	EgressReader
}
type EgressReader interface {
	Policy(context.Context, control.Pin) (control.EgressSpec, error)
}

// EntryReservation is a durable B08 intent, after sealing a fresh proxy_entry
// credential, before writing any FK that requires the listener. OperationID is
// immutable across retries; Deadline controls compensation, not authorization.
type EntryReservation struct {
	OperationID    domain.ID
	SubscriptionID domain.ID
	Route          control.Pin
	Authorization  control.BundleAuthorization
	CredentialRef  domain.ID
	Deadline       time.Time
}
type IngressLease struct {
	ReservationID domain.ID
	ListenerID    domain.ID
	Route         control.Pin
	Endpoint      control.ProxyEndpoint // actual client-facing port, public only
}
type EntryChange struct {
	Kind     domain.TaskKind // publish / revoke / cleanup
	Mutation domain.Mutation
	Subject  control.Pin // B08 reviewed subscription revision
	Entry    EntryReservation
	Lease    IngressLease
	Epoch    domain.Revision
	// Revoke/cleanup use the real previously applied identity, never a fresh one.
}

// PublishPlan is B06's trusted compiler output to the B04 gateway. The task ID
// is filled by B04; each template is the COMPLETE intended node config.
type PublishPlan struct {
	Kind             domain.TaskKind
	Subject          control.Pin
	Deadline         time.Time
	Nodes            []NodePlan
	EntryOperationID domain.ID // set only for B08 intent; checked before commit/arm
}
type NodePlan struct {
	NodeID           domain.ID
	ExpectedRevision domain.Revision
	Template         control.BundleV2
	DependsOn        []domain.ID
}
type ReleaseState struct {
	TaskID           domain.ID
	Subject          control.Pin
	EntryOperationID domain.ID
	State            string              // queued, running, published, failed, cancelled, timed_out, rolled_back, rollback_pending, rollback_failed
	Nodes            []domain.NodeResult // independent apply, verify, rollback and cleanup evidence
}
type Publisher interface {
	SubmitV2(context.Context, domain.Mutation, PublishPlan) (domain.TaskAccepted, error)
	ReleaseState(context.Context, domain.ID) (ReleaseState, error)
}

// Infrastructure is implemented by I03 on the shared B04 allocation/state ledger.
// No lock or transaction crosses these calls. The gateway atomically reserves
// the ingress port, materializes the matching legacy listener FK and holds an
// expiring intent until CAS-committed by B08 or safely compensated.
type Infrastructure interface {
	ReserveIngress(context.Context, EntryReservation, PublishedRoute) (IngressLease, error)
	ReserveLink(context.Context, LinkReservation) (LinkLease, error)
	NodeSnapshot(context.Context, domain.ID) (NodeSnapshot, error)
}
type LinkReservation struct {
	OperationID domain.ID
	Subject     control.Pin
	Tail        control.RouteTail // proposed concrete port, chosen from the node snapshot
	// Candidate identity/credential are used only when allocating a new link.
	ProposedLink  control.Pin
	CredentialRef domain.ID
	Deadline      time.Time
}
type LinkLease struct {
	ReservationID domain.ID
	Link          control.Pin
	ListenerID    domain.ID
	CredentialRef domain.ID
}
type PinnedSocketSpec struct {
	Pin       control.Pin
	Spec      control.SocketSpec
	PoolFirst uint16
	PoolLast  uint16
}
type NodeSnapshot struct {
	Revision     domain.Revision
	DNS          control.DNSInput
	Capabilities []ProtocolCapability
	Sockets      []PinnedSocketSpec
	Loops        control.LoopInventory
	// Complete desired state, including grants for unrelated publications.
	// Exactly one base format selected; I03 converts explicitly, never relabels.
	Legacy   *control.Bundle
	Extended *control.BundleV2
}
type ProtocolCapability struct {
	Available   bool
	Protocol    domain.Protocol
	BusinessUDP domain.Verification
}
type RoutesOptions struct {
	Runtime
	Publisher Publisher
	Egress    EgressReader
	Infra     Infrastructure
	Secrets   SecretReader
	Cipher    secretstore.Cipher
}

// ProxySecret is the strict plaintext envelope used in proxy_entry/internal_proxy
// Cipher records. References and recipients are supplied by trusted callers.
type ProxySecret struct {
	Password string `json:"password"`
	UUID     string `json:"uuid"`
}

type SecretReader interface {
	Open(context.Context, secretstore.Binding) ([]byte, error)
}

type RuleTarget struct {
	Builtin string // DIRECT or REJECT, only on explicit selection
	RouteID domain.ID
	Group   string // All-Proxies or Exit-* name; discriminated with the other fields
}
type RuleLine struct {
	Kind      string
	Value     string
	NoResolve bool
	Target    RuleTarget
	DomainSet *control.Pin // present only for RULE-SET
	Line      int
	Column    int
}
type DomainSetSpec struct {
	Domains []string // canonical ASCII domains; order pinned, deduplicated
}
type DomainSetSnapshot struct {
	Pin  control.Pin
	Spec DomainSetSpec
}
type ProfileInput struct {
	ID     domain.ID
	Name   string
	Source string
	Sets   []control.Pin
}
type ProfileSpec struct {
	Rules []RuleLine // order preserved, exactly one MATCH as last element
	Sets  []DomainSetSnapshot
}
type ProfileSnapshot struct {
	Pin  control.Pin
	Spec ProfileSpec
}
type Rules interface {
	SaveDomainSet(context.Context, domain.Mutation, domain.ID, string, DomainSetSpec) (control.Pin, error)
	SaveProfile(context.Context, domain.Mutation, ProfileInput) (control.Pin, []Issue, error)
	Profile(context.Context, control.Pin) (ProfileSnapshot, error)
}

// EntryView is safe metadata and carries the exact route/egress revision.
// Credentials are deliberately absent even as plaintext placeholders.
type EntryView struct {
	ID         domain.ID
	IdentityID domain.ID
	Generation domain.Revision
	Route      PublishedRoute
	Name       string
	Endpoint   control.ProxyEndpoint
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	Sync       string // pending, confirmed, failed
	Cleanup    string // none, pending, confirmed
}
type GroupChoice struct {
	Name            string
	SelectedEntryID domain.ID
}
type ClientDNS struct {
	Bootstrap    []string // numeric IPs
	ProxyServers []string // literal IP or HTTPS-to-literal; bootstrap only
	Business     []string // literal HTTPS endpoints; bound to rule targets by builder
}
type ClientPreferences struct {
	Mode           string // tun (default), system_proxy
	IPv6           bool
	BusinessUDP    bool
	AllRoutesGroup bool
	GroupByExit    bool
	Selections     []GroupChoice
	DNS            ClientDNS
}
type ClientSnapshot struct {
	SubscriptionID domain.ID
	Revision       domain.Revision
	Profile        ProfileSnapshot
	Entries        []EntryView // requested order, active/confirmed by B08 for download
	Preferences    ClientPreferences
}
type EntrySecret struct {
	IdentityID domain.ID
	Credential control.PrivateCredential // audience = subscription ID, proxy_entry only
}
type GenerationInput struct {
	Snapshot ClientSnapshot
	Secrets  []EntrySecret // ordered one-to-one; no internal or management credentials
}
type FullConfig []byte    // sensitive, complete Mihomo config
type ProxyProvider []byte // sensitive, proxies only
type ClientBuilder interface {
	Preview(ClientSnapshot) (Preview, error)
	BuildFull(GenerationInput) (FullConfig, error)
	BuildProvider(GenerationInput) (ProxyProvider, error)
}
type Preview struct {
	Groups []GroupChoice
	Issues []Issue
	// No YAML containing usable credentials. A preview has no download authority.
}
type RulesOptions struct{ Runtime }
type ClientOptions struct{} // pure, no DB/clock/HTTP

type SubscriptionDraft struct {
	ID          domain.ID
	Name        string
	ExpiresAt   time.Time
	Profile     control.Pin
	Routes      []control.Pin
	Preferences ClientPreferences
}
type SubscriptionView struct {
	Entity    domain.Entity
	ExpiresAt time.Time
	RevokedAt *time.Time
	Profile   control.Pin
	Entries   []EntryView
	Sync      string
}
type SubscriptionDownloadLink struct {
	DownloadURL string `json:"download_url"`
} // only the guarded copy response
type Subscriptions interface {
	Create(context.Context, domain.Mutation, SubscriptionDraft) (SubscriptionView, error)
	UpdateConfig(context.Context, domain.Mutation, SubscriptionDraft) (SubscriptionView, error)
	Renew(context.Context, domain.Mutation, domain.ID, time.Time) (SubscriptionView, error)
	Delete(context.Context, domain.Mutation, domain.ID) (SubscriptionView, error)
	ResetDownloadToken(context.Context, domain.Mutation, domain.ID) (SubscriptionView, error)
	Get(context.Context, domain.ID) (SubscriptionView, error)
	Reconcile(context.Context, domain.ID) error // durable intent ID; explicit worker resume
	DownloadLink(context.Context, domain.ID) (SubscriptionDownloadLink, error)
	DownloadFull(context.Context, string) (FullConfig, error)
	DownloadProvider(context.Context, string) (ProxyProvider, error)
}
type SubscriptionsOptions struct {
	Runtime
	Guard  adminauth.Guard
	Cipher secretstore.Cipher
	Routes EntryRoutes
	Rules  interface {
		Profile(context.Context, control.Pin) (ProfileSnapshot, error)
	}
	Client    ClientBuilder
	Publisher interface {
		ReleaseState(context.Context, domain.ID) (ReleaseState, error)
	}
	PublicOrigin string // validated HTTPS origin from trusted config, never request Host
}
