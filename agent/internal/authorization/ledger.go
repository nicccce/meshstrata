package authorization

import (
	"sort"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func Clock(s control.DurableState, now time.Time) time.Time {
	if now.Before(s.ClockFloor) {
		return s.ClockFloor
	}
	return now.UTC()
}

// Merge records grant origins only during admission of a bound artifact. Cached
// snapshots are filtered with Active and cannot introduce grants.
func Merge(s *control.DurableState, b control.Bundle, key control.ArtifactKey, now time.Time) error {
	if b.RevocationEpoch < s.RevocationEpoch {
		return control.ErrAuthorization
	}
	grants, tombs := lookup(*s)
	highest := map[domain.ID]domain.Revision{}
	for _, g := range s.Grants {
		a := g.Listener.Authorization
		highest[a.EntryID] = max(highest[a.EntryID], a.Generation)
	}
	var added []control.Grant
	newEntries := map[domain.ID]bool{}
	for _, l := range b.Listeners {
		a := l.Authorization
		if prev, ok := grants[a.IdentityID]; ok {
			// RevokedAt is a restriction signal; immutable properties are
			// compared independently. The original grant remains untouched.
			l.Authorization.RevokedAt = prev.Listener.Authorization.RevokedAt
			if control.CheckGrant(prev, l) != nil {
				return control.ErrAuthorization
			}
			continue
		}
		if t, ok := tombs[a.IdentityID]; ok {
			if t.Generation != a.Generation {
				return control.ErrAuthorization
			}
			// No real local grant existed before the tombstone. Never adopt an
			// origin for it from a late configuration.
			continue
		}
		if a.Generation <= highest[a.EntryID] || newEntries[a.EntryID] {
			return control.ErrAuthorization
		}
		// A renewal reuses the stable entry but must use fresh credentials.
		for _, known := range s.Grants {
			old := known.Listener
			sameEntry := old.Authorization.EntryID == a.EntryID
			sameEndpoint := old.Port == l.Port && old.Protocol == l.Protocol
			if old.Credential.Ref == l.Credential.Ref ||
				((sameEntry || sameEndpoint) && equalSecret(old.Credential, l.Credential)) {
				return control.ErrAuthorization
			}
		}
		for _, other := range added {
			if other.Listener.Credential.Ref == l.Credential.Ref {
				return control.ErrAuthorization
			}
		}
		newEntries[a.EntryID] = true
		added = append(added, control.Grant{Origin: key, Listener: l})
	}
	for _, t := range b.Tombstones {
		if g, ok := grants[t.IdentityID]; ok && g.Listener.Authorization.Generation != t.Generation {
			return control.ErrAuthorization
		}
	}
	_, merged, epoch, err := control.FilterAuthorizations(b, s.RevocationEpoch, s.Tombstones, now)
	if err != nil {
		return err
	}
	s.ClockFloor = now
	s.Tombstones = merged
	s.RevocationEpoch = epoch
	s.Grants = append(s.Grants, added...)
	Expire(s, now)
	return nil
}

func equalSecret(a, b control.PrivateCredential) bool {
	return a.Password != "" && a.Password == b.Password || a.UUID != "" && a.UUID == b.UUID
}

func Expire(s *control.DurableState, now time.Time) {
	s.ClockFloor = now
	_, tombs := lookup(*s)
	for _, a := range entryAuthorizations(*s) {
		if _, exists := tombs[a.IdentityID]; exists || now.Before(a.ExpiresAt) {
			continue
		}
		s.Tombstones = append(s.Tombstones, control.Tombstone{
			IdentityID: a.IdentityID, Generation: a.Generation, Epoch: s.RevocationEpoch, At: a.ExpiresAt, Reason: "expired",
		})
	}
	sort.Slice(s.Tombstones, func(i, j int) bool { return s.Tombstones[i].IdentityID < s.Tombstones[j].IdentityID })
}

// Active only returns grants already present in the durable ledger. Snapshot
// input can be old and may contain a permanently tombstoned identity.
func Active(s control.DurableState, b control.Bundle, now time.Time) []control.BundleListener {
	grants, tombs := lookup(s)
	out := make([]control.BundleListener, 0, len(b.Listeners))
	for _, l := range b.Listeners {
		a := l.Authorization
		g, known := grants[a.IdentityID]
		_, revoked := tombs[a.IdentityID]
		if known && !revoked && a.RevokedAt == nil && now.Before(a.ExpiresAt) && control.CheckGrant(g, l) == nil {
			out = append(out, l)
		}
	}
	return out
}

func Due(s control.DurableState, now time.Time) bool {
	next := NextExpiry(s)
	return !next.IsZero() && !now.Before(next)
}

func NextExpiry(s control.DurableState) time.Time {
	_, tombs := lookup(s)
	var next time.Time
	for _, a := range entryAuthorizations(s) {
		if _, exists := tombs[a.IdentityID]; !exists && (next.IsZero() || a.ExpiresAt.Before(next)) {
			next = a.ExpiresAt
		}
	}
	return next
}

func lookup(s control.DurableState) (map[domain.ID]control.Grant, map[domain.ID]control.Tombstone) {
	grants := make(map[domain.ID]control.Grant, len(s.Grants))
	tombs := make(map[domain.ID]control.Tombstone, len(s.Tombstones))
	for _, g := range s.Grants {
		grants[g.Listener.Authorization.IdentityID] = g
	}
	for _, t := range s.Tombstones {
		tombs[t.IdentityID] = t
	}
	return grants, tombs
}
