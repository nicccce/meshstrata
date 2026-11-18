package releases

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type InterfaceObservation struct {
	Name    string
	Address netip.Addr
	Up      bool
}

// EgressObservation is emitted by the authenticated Agent adapter after a
// restricted numeric target probe, never arbitrary URLs, shell or scripts.
// Probe destinations are provisioned in trusted Options, independent of input.
type EgressObservation struct {
	NodeID                domain.ID
	NodeRevision          domain.Revision
	DNS, Target           control.Pin
	Interface             string
	SourceIP              netip.Addr
	Probe                 netip.AddrPort
	ObservedAt, ExpiresAt time.Time
	BusinessUDP           bool
}

func (s *Service) RecordEgressObservation(ctx context.Context, r EgressObservation) error {
	now := s.options.Now()
	if !r.NodeID.Valid() || !r.NodeRevision.Valid() || !r.DNS.Valid() || !r.Target.Valid() || !r.SourceIP.IsValid() || !r.Probe.IsValid() || r.Probe.Port() == 0 || !slices.Contains(s.options.EgressProbes, r.Probe) || r.ObservedAt.After(now) || r.ObservedAt.Before(now.Add(-time.Minute)) || !r.ExpiresAt.After(now) || r.ExpiresAt.After(r.ObservedAt.Add(2*time.Minute)) {
		return problem(domain.InvalidArgument)
	}
	snap, e := s.NodeSnapshot(ctx, r.NodeID)
	if e != nil {
		return e
	}
	if snap.Revision != r.NodeRevision || snap.DNS.Pin != r.DNS {
		return problem(domain.RevisionConflict)
	}
	var target control.EgressTarget
	e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		v, e := q.B06EgressVersion(ctx, dbgen.B06EgressVersionParams{ID: string(r.Target.ID), Revision: int64(r.Target.Revision)})
		if e != nil {
			return missing(e)
		}
		if v.Kind != "target" || v.NodeID != string(r.NodeID) || v.Digest != r.Target.Digest {
			return problem(domain.RevisionConflict)
		}
		return json.Unmarshal(v.Spec, &target)
	})
	if e != nil {
		return e
	}
	pin, e := stage3.NewPin(r.Target.ID, r.Target.Revision, target)
	if e != nil || pin != r.Target || !target.Valid() {
		return problem(domain.InvalidArgument)
	}
	if target.Type == "http" && r.BusinessUDP || target.Type == "interface" && r.Interface != target.Interface || r.Interface == "" || !slices.Contains(snap.Loops.LocalAddresses, r.SourceIP) || slices.Contains(snap.Loops.ProtectedInterfaces, r.Interface) {
		return problem(domain.InvalidArgument)
	}
	sockets := []control.SocketV2{}
	if snap.Extended != nil {
		for _, l := range snap.Extended.Listeners {
			sockets = append(sockets, l.Socket)
		}
	}
	if control.CheckEgressTargetLoops(target, snap.Loops, sockets) != nil {
		return problem(domain.DependencyInUse)
	}
	udp := int64(0)
	if r.BusinessUDP {
		udp = 1
	}
	return s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		now := s.options.Now()
		if !r.ExpiresAt.After(now) {
			return problem(domain.TaskExpired)
		}
		n, e := q.GetNode(ctx, string(r.NodeID))
		if e != nil {
			return e
		}
		inv, e := q.I03Inventory(ctx, n.ID)
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid || n.Revision != int64(r.NodeRevision) || inv.NodeRevision != n.Revision || inv.DnsDigest != r.DNS.Digest || inv.ExpiresAt <= stamp(now) {
			return problem(domain.RevisionConflict)
		}
		up, e := q.I03Interface(ctx, dbgen.I03InterfaceParams{NodeID: n.ID, Name: r.Interface, Address: r.SourceIP.String()})
		if e != nil || up != 1 {
			return problem(domain.DependencyInUse)
		}
		if e = q.I03PutEgress(ctx, dbgen.I03PutEgressParams{NodeID: n.ID, NodeRevision: n.Revision, TargetDigest: r.Target.Digest, DnsDigest: r.DNS.Digest, ExpiresAt: stamp(r.ExpiresAt), BusinessUdp: udp}); e != nil {
			return e
		}
		return q.I03PutObservation(ctx, dbgen.I03PutObservationParams{NodeID: n.ID, NodeRevision: n.Revision, TargetDigest: r.Target.Digest, DnsDigest: r.DNS.Digest, ObservedAt: stamp(r.ObservedAt), ExpiresAt: stamp(r.ExpiresAt), InterfaceName: r.Interface, SourceIp: r.SourceIP.String(), ProbeIp: r.Probe.Addr().String(), ProbePort: int64(r.Probe.Port())})
	})
}
