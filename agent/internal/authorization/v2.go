package authorization

import (
	"reflect"
	"sort"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

// V2ID distinguishes real subscription identities from independent internal
// resources. It never synthesizes an entry, generation, or expiry for a link.
func V2ID(l control.ListenerV2) domain.ID {
	if l.Entry != nil {
		return l.Entry.IdentityID
	}
	if l.Internal != nil {
		return l.Internal.ID
	}
	return ""
}

func entryAuthorizations(s control.DurableState) []control.BundleAuthorization {
	out := make([]control.BundleAuthorization, 0, len(s.Grants)+len(s.BindingsV2))
	for _, g := range s.Grants {
		out = append(out, g.Listener.Authorization)
	}
	for _, g := range s.BindingsV2 {
		if g.Listener.Entry != nil {
			out = append(out, *g.Listener.Entry)
		}
	}
	return out
}

// MergeV2 admits only a new fetched artifact. Restoring any cached checkpoint
// uses ActiveV2 and therefore cannot introduce a binding or erase a restriction.
func MergeV2(s *control.DurableState, b control.BundleV2, key control.ArtifactKey, now time.Time) error {
	_, actual, err := control.EncodeBundleV2(b)
	if err != nil || actual != key || b.RevocationEpoch < s.RevocationEpoch || key.NodeID != b.NodeID || key.TaskID != b.TaskID || key.TargetRevision != b.TargetRevision {
		return control.ErrAuthorization
	}
	known := map[domain.ID]control.BindingV2{}
	retired := map[domain.ID]control.InternalRetirement{}
	tombs := map[domain.ID]control.Tombstone{}
	highest := map[domain.ID]domain.Revision{}
	for _, a := range entryAuthorizations(*s) {
		highest[a.EntryID] = max(highest[a.EntryID], a.Generation)
	}
	for _, g := range s.BindingsV2 {
		known[V2ID(g.Listener)] = g
	}
	for _, r := range s.RetiredLinks {
		retired[r.Link.ID] = r
	}
	for _, t := range s.Tombstones {
		tombs[t.IdentityID] = t
	}
	for _, t := range b.Tombstones {
		if old, ok := tombs[t.IdentityID]; ok {
			if old.Generation != t.Generation {
				return control.ErrAuthorization
			}
			if old.At.Before(t.At) {
				t.At = old.At
			}
			t.Epoch = max(t.Epoch, old.Epoch)
			if old.Reason == "revoked" {
				t.Reason = old.Reason
			}
		}
		if g, ok := known[t.IdentityID]; ok && (g.Listener.Entry == nil || g.Listener.Entry.Generation != t.Generation) {
			return control.ErrAuthorization
		}
		tombs[t.IdentityID] = t
	}
	for _, r := range b.RetiredLinks {
		if old, ok := retired[r.Link.ID]; ok {
			if old.Link != r.Link {
				return control.ErrAuthorization
			}
			r.Epoch = max(r.Epoch, old.Epoch)
		}
		if g, ok := known[r.Link.ID]; ok && (g.Listener.Internal == nil || *g.Listener.Internal != r.Link) {
			return control.ErrAuthorization
		}
		retired[r.Link.ID] = r
	}
	// A V1 live identity must be explicitly cleaned before switching a node's
	// routing model. Merely advertising V2 never grants a migration privilege.
	for _, g := range s.Grants {
		a := g.Listener.Authorization
		if _, restricted := tombs[a.IdentityID]; !restricted && a.RevokedAt == nil && now.Before(a.ExpiresAt) {
			return control.ErrAuthorization
		}
	}
	var added []control.BindingV2
	present := map[domain.ID]bool{}
	newEntries := map[domain.ID]bool{}
	for _, l := range b.Listeners {
		id := V2ID(l)
		present[id] = true
		if previous, ok := known[id]; ok {
			if !reflect.DeepEqual(previous.Listener, l) {
				return control.ErrAuthorization
			}
			continue
		}
		if l.Entry != nil {
			a := *l.Entry
			if t, ok := tombs[id]; ok {
				if t.Generation != a.Generation {
					return control.ErrAuthorization
				}
				continue // no real first origin can be invented after a tombstone
			}
			if a.Generation <= highest[a.EntryID] || newEntries[a.EntryID] {
				return control.ErrAuthorization
			}
			newEntries[a.EntryID] = true
			for _, old := range s.Grants {
				if old.Listener.Credential.Ref == l.Socket.Credential.Ref || equalSecret(old.Listener.Credential, l.Socket.Credential) {
					return control.ErrAuthorization
				}
			}
		} else if r, ok := retired[id]; ok {
			if *l.Internal != r.Link {
				return control.ErrAuthorization
			}
			continue
		}
		for _, old := range append(append([]control.BindingV2{}, s.BindingsV2...), added...) {
			if old.Listener.Socket.Credential.Ref == l.Socket.Credential.Ref || equalSecret(old.Listener.Socket.Credential, l.Socket.Credential) {
				return control.ErrAuthorization
			}
		}
		added = append(added, control.BindingV2{Origin: key, Listener: l})
	}
	for id, g := range known {
		if present[id] {
			continue
		}
		if g.Listener.Entry != nil {
			a := g.Listener.Entry
			if _, ok := tombs[id]; !ok && a.RevokedAt == nil && now.Before(a.ExpiresAt) {
				return control.ErrAuthorization
			}
		} else if _, ok := retired[id]; !ok {
			return control.ErrAuthorization
		}
	}
	s.Format = 2
	s.ClockFloor = now
	s.RevocationEpoch = b.RevocationEpoch
	s.BindingsV2 = append(s.BindingsV2, added...)
	s.Tombstones = nil
	for _, t := range tombs {
		s.Tombstones = append(s.Tombstones, t)
	}
	s.RetiredLinks = nil
	for _, r := range retired {
		s.RetiredLinks = append(s.RetiredLinks, r)
	}
	sort.Slice(s.RetiredLinks, func(i, j int) bool { return s.RetiredLinks[i].Link.ID < s.RetiredLinks[j].Link.ID })
	Expire(s, now)
	return nil
}

func ActiveV2(s control.DurableState, b control.BundleV2, now time.Time) []control.ListenerV2 {
	known := map[domain.ID]control.ListenerV2{}
	restricted := map[domain.ID]bool{}
	for _, g := range s.BindingsV2 {
		known[V2ID(g.Listener)] = g.Listener
	}
	for _, t := range s.Tombstones {
		restricted[t.IdentityID] = true
	}
	for _, r := range s.RetiredLinks {
		restricted[r.Link.ID] = true
	}
	out := make([]control.ListenerV2, 0, len(b.Listeners))
	for _, l := range b.Listeners {
		id := V2ID(l)
		if k, ok := known[id]; !ok || restricted[id] || !reflect.DeepEqual(k, l) {
			continue
		}
		if l.Entry != nil && (l.Entry.RevokedAt != nil || !now.Before(l.Entry.ExpiresAt)) {
			continue
		}
		out = append(out, l)
	}
	return out
}
