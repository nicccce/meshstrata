package checkpoint

import (
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"reflect"
	"slices"
	"strings"
)

const maxPermanentBindings = 4096

func bindingID(l control.ListenerV2) domain.ID {
	if l.Entry != nil {
		return l.Entry.IdentityID
	}
	if l.Internal != nil {
		return l.Internal.ID
	}
	return ""
}
func validV2State(s control.DurableState, node domain.ID) error {
	if s.Format != 0 && s.Format != 2 {
		return ErrState
	}
	if s.Format != 2 && (len(s.BindingsV2) > 0 || len(s.RetiredLinks) > 0 || len(s.LinkCleanupsV2) > 0) {
		return ErrState
	}
	if len(s.BindingsV2) > maxPermanentBindings || len(s.RetiredLinks) > maxPermanentBindings || len(s.Tombstones) > maxPermanentBindings || len(s.LinkCleanupsV2) > maxPermanentBindings {
		return ErrState
	}
	seen := map[domain.ID]bool{}
	for _, g := range s.BindingsV2 {
		l := g.Listener
		id := bindingID(l)
		if !id.Valid() || seen[id] || (l.Entry == nil) == (l.Internal == nil) || g.Origin.NodeID != node || !validTarget(g.Origin.TaskID, g.Origin.TargetRevision, g.Origin.TargetDigest) || l.Socket.Credential.Audience != node || !l.Socket.ListenerID.Valid() || len(l.Tail.Hops) == 0 || l.Tail.Hops[0].NodeID != node {
			return ErrState
		}
		seen[id] = true
		if l.Entry != nil && (!l.Entry.Generation.Valid() || !l.Entry.EntryID.Valid() || l.Entry.ExpiresAt.IsZero()) {
			return ErrState
		}
		if l.Entry != nil {
			for _, t := range s.Tombstones {
				if t.IdentityID == id && t.Generation != l.Entry.Generation {
					return ErrState
				}
			}
			for _, g := range s.Grants {
				if g.Listener.Authorization.IdentityID == id {
					return ErrState
				}
			}
		}
		if l.Internal != nil && !l.Internal.Valid() {
			return ErrState
		}
		found := false
		for _, x := range s.Executions {
			if x.Command.TaskID == g.Origin.TaskID && x.Command.NodeID == node && x.Command.TargetRevision == g.Origin.TargetRevision && x.Command.TargetDigest == g.Origin.TargetDigest {
				found = true
			}
		}
		if !found {
			return ErrState
		}
	}
	seen = map[domain.ID]bool{}
	for _, r := range s.RetiredLinks {
		if !r.Link.Valid() || !r.Epoch.Valid() || r.Epoch > s.RevocationEpoch || seen[r.Link.ID] {
			return ErrState
		}
		seen[r.Link.ID] = true
	}
	seen = map[domain.ID]bool{}
	for _, c := range s.LinkCleanupsV2 {
		if seen[c.Link.ID] || !c.Link.Valid() || !validTarget(c.Link.ID, c.Link.Revision, c.EffectiveDigest) || c.ConfirmedAt.IsZero() || c.ConfirmedAt.After(s.ClockFloor) {
			return ErrState
		}
		seen[c.Link.ID] = true
		retired, bound := false, false
		for _, r := range s.RetiredLinks {
			retired = retired || r.Link == c.Link
		}
		for _, g := range s.BindingsV2 {
			bound = bound || g.Listener.Internal != nil && *g.Listener.Internal == c.Link
		}
		if !retired || !bound {
			return ErrState
		}
	}
	for _, snap := range []*control.Snapshot{s.Checkpoint.Candidate, s.Checkpoint.Applied, s.Checkpoint.Verified, s.Checkpoint.Startup, s.Checkpoint.PreviousGood} {
		if snap != nil && snap.Format == 2 && s.Format != 2 {
			return ErrState
		}
	}
	return nil
}

// Verify the full private binding against the first immutable fetched artifact.
// A structurally valid ledger is insufficient: it cannot invent credentials,
// origins, sockets, or suffixes independently of authenticated source bytes.
func (s *Store) validV2Sources(state control.DurableState) error {
	artifacts := map[control.ArtifactKey]control.BundleV2{}
	for _, g := range state.BindingsV2 {
		b, ok := artifacts[g.Origin]
		if !ok {
			name, err := artifactName(g.Origin)
			if err != nil {
				return ErrState
			}
			data, err := s.read(name, control.MaxBundleBytes)
			if err != nil {
				return ErrState
			}
			b, err = control.DecodeBundleV2(data, g.Origin, s.node)
			clear(data)
			if err != nil {
				return ErrState
			}
			artifacts[g.Origin] = b
		}
		found := false
		for _, l := range b.Listeners {
			if reflect.DeepEqual(l, g.Listener) {
				found = true
				break
			}
		}
		if !found {
			return ErrState
		}
	}
	return nil
}
func mergeV2State(old control.DurableState, next *control.DurableState) error {
	if old.Format == 2 {
		next.Format = 2
	}
	byID := map[domain.ID]control.BindingV2{}
	for _, g := range old.BindingsV2 {
		byID[bindingID(g.Listener)] = g
	}
	for _, g := range next.BindingsV2 {
		id := bindingID(g.Listener)
		if p, ok := byID[id]; ok && !reflect.DeepEqual(p, g) {
			return ErrState
		}
		byID[id] = g
	}
	next.BindingsV2 = nil
	for _, g := range byID {
		next.BindingsV2 = append(next.BindingsV2, g)
	}
	slices.SortFunc(next.BindingsV2, func(a, b control.BindingV2) int {
		return strings.Compare(string(bindingID(a.Listener)), string(bindingID(b.Listener)))
	})
	retired := map[domain.ID]control.InternalRetirement{}
	for _, list := range [][]control.InternalRetirement{old.RetiredLinks, next.RetiredLinks} {
		for _, r := range list {
			if p, ok := retired[r.Link.ID]; ok {
				if p.Link != r.Link {
					return ErrState
				}
				r.Epoch = max(r.Epoch, p.Epoch)
			}
			retired[r.Link.ID] = r
		}
	}
	next.RetiredLinks = nil
	for _, r := range retired {
		next.RetiredLinks = append(next.RetiredLinks, r)
	}
	slices.SortFunc(next.RetiredLinks, func(a, b control.InternalRetirement) int {
		return strings.Compare(string(a.Link.ID), string(b.Link.ID))
	})
	cleanups := map[domain.ID]control.LinkCleanupV2{}
	for _, list := range [][]control.LinkCleanupV2{old.LinkCleanupsV2, next.LinkCleanupsV2} {
		for _, c := range list {
			if p, ok := cleanups[c.Link.ID]; ok && p != c {
				return ErrState
			}
			cleanups[c.Link.ID] = c
		}
	}
	next.LinkCleanupsV2 = nil
	for _, c := range cleanups {
		next.LinkCleanupsV2 = append(next.LinkCleanupsV2, c)
	}
	slices.SortFunc(next.LinkCleanupsV2, func(a, b control.LinkCleanupV2) int { return strings.Compare(string(a.Link.ID), string(b.Link.ID)) })
	return nil
}
