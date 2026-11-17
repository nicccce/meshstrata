package stage3

import (
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const AllProxies = "All-Proxies"

// ExitGroupName is independent of labels and revisions. A builder must reject
// members whose full policies differ despite having the same stable name.
func ExitGroupName(terminal, policy domain.ID) (string, error) {
	if !terminal.Valid() || policy != "" && !policy.Valid() {
		return "", &domain.Problem{Code: domain.InvalidArgument}
	}
	name := "Exit-" + string(terminal)
	if policy != "" {
		name += "-Egress-" + string(policy)
	}
	return name, nil
}

// NewPin hashes a bounded typed payload (no plaintext). Never use user JSON
// maps, transport metadata, mutable evidence or credentials as this payload.
func NewPin(id domain.ID, revision domain.Revision, value any) (control.Pin, error) {
	digest, err := control.ComponentDigest(value)
	p := control.Pin{ID: id, Revision: revision, Digest: digest}
	if err != nil || !p.Valid() {
		return control.Pin{}, &domain.Problem{Code: domain.InvalidArgument}
	}
	return p, nil
}

// RouteDigest pins the safe published payload; observations are mutable and
// excluded. Profile and DomainSet pins use their respective Spec values.
func RouteDigest(r PublishedRoute) (string, error) {
	if _, err := r.Tail.Key(); err != nil {
		return "", invalidGeneration()
	}
	return control.ComponentDigest(struct {
		Name            string            `json:"name"`
		Tail            control.RouteTail `json:"tail"`
		EgressIsDefault bool              `json:"egress_is_default"`
	}{r.Name, r.Tail, r.EgressIsDefault})
}

const MaxSubscriptionEntries = 128

// ValidateGenerationBinding is a pure defense at B07's private boundary.
// B08 performs the current expiry, revocation and active generation CAS checks;
// this does not authorize a download and cannot replace that transaction fence.
func ValidateGenerationBinding(input GenerationInput) error {
	s := input.Snapshot
	if !s.SubscriptionID.Valid() || !s.Revision.Valid() || !s.Profile.Pin.Valid() ||
		len(s.Entries) == 0 || len(s.Entries) > MaxSubscriptionEntries || len(s.Entries) != len(input.Secrets) {
		return invalidGeneration()
	}
	ids := map[domain.ID]bool{}
	routes := map[domain.ID]bool{}
	identities := map[domain.ID]bool{}
	refs := map[domain.ID]bool{}
	for i, e := range s.Entries {
		secret := input.Secrets[i]
		hash, err := RouteDigest(e.Route)
		if !e.ID.Valid() || !e.IdentityID.Valid() || !e.Generation.Valid() || e.ExpiresAt.IsZero() ||
			e.RevokedAt != nil || e.Sync != "confirmed" || e.Cleanup != "none" ||
			ids[e.ID] || identities[e.IdentityID] || routes[e.Route.Pin.ID] || refs[secret.Credential.Ref] ||
			err != nil || !e.Route.Pin.Valid() || e.Route.Pin.Digest != hash || secret.IdentityID != e.IdentityID ||
			!control.ValidProxyCredential(e.Endpoint, secret.Credential, s.SubscriptionID) {
			return invalidGeneration()
		}
		endpoint := e.Route.Tail.Hops[0].Endpoint
		endpoint.Port = e.Endpoint.Port // subscription-scoped allocation
		if endpoint != e.Endpoint {
			return invalidGeneration()
		}
		ids[e.ID], identities[e.IdentityID], routes[e.Route.Pin.ID], refs[secret.Credential.Ref] = true, true, true, true
	}
	return nil
}
func invalidGeneration() error { return &domain.Problem{Code: domain.InvalidArgument} }
