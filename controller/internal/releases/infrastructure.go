package releases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/ports"
	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

var _ stage3.Infrastructure = (*Service)(nil)
var _ stage3.Publisher = (*Service)(nil)

// InventoryReport is accepted only from a trusted local provisioner/Agent
// inventory adapter. It is not a management DTO or a capability advertisement.
// Reports expire and are fenced by the exact node revision and DNS pin.
type InventoryReport struct {
	Interfaces            []InterfaceObservation
	NodeID                domain.ID
	NodeRevision          domain.Revision
	DNS                   control.DNSInput
	Loops                 control.LoopInventory
	Sockets               []stage3.PinnedSocketSpec
	Capabilities          []stage3.ProtocolCapability
	ObservedAt, ExpiresAt time.Time
}

func (s *Service) RecordInventory(ctx context.Context, r InventoryReport) error {
	now := s.options.Now()
	if !r.NodeID.Valid() || !r.NodeRevision.Valid() || r.ObservedAt.After(now) || r.ObservedAt.Before(now.Add(-5*time.Minute)) || !r.ExpiresAt.After(now) || r.ExpiresAt.After(r.ObservedAt.Add(10*time.Minute)) || len(r.Sockets) == 0 || len(r.Sockets) > 32 {
		return problem(domain.InvalidArgument)
	}
	b := emptyV2(r.NodeID, r.NodeRevision, 1, r.DNS)
	b.TaskID = r.NodeID
	if b.CheckEgressLoops(r.Loops) != nil {
		return problem(domain.InvalidArgument)
	}
	dns, _ := json.Marshal(r.DNS)
	loops, _ := json.Marshal(r.Loops)
	sockets := make([]dbgen.I03PutSocketParams, 0, len(r.Sockets))
	seen := map[control.Pin]bool{}
	for _, sock := range r.Sockets {
		pin, err := stage3.NewPin(sock.Pin.ID, sock.Pin.Revision, sock.Spec)
		if err != nil || pin != sock.Pin || seen[pin] || sock.PoolLast < sock.PoolFirst || sock.PoolFirst == 0 && sock.PoolLast != 0 {
			return problem(domain.InvalidArgument)
		}
		seen[pin] = true
		protocol := domain.Protocol("")
		udp := domain.NotChecked
		for _, c := range r.Capabilities {
			if c.Available && sock.Protocol == "" {
				if protocol != "" {
					return problem(domain.InvalidArgument)
				}
				protocol = c.Protocol
				udp = c.BusinessUDP
			}
		}
		// One report can contain multiple protocols; pair each socket by an explicit
		// protocol field, rather than inferring it from a port or certificate.
		if sock.Protocol != "" {
			protocol = sock.Protocol
			found := false
			for _, c := range r.Capabilities {
				if c.Protocol == protocol && c.Available {
					udp = c.BusinessUDP
					found = true
				}
			}
			if !found {
				return problem(domain.InvalidArgument)
			}
		}
		if protocol == "" {
			return problem(domain.InvalidArgument)
		}
		raw, _ := json.Marshal(sock.Spec)
		sockets = append(sockets, dbgen.I03PutSocketParams{NodeID: string(r.NodeID), ConfigID: string(pin.ID), ConfigRevision: int64(pin.Revision), ConfigDigest: pin.Digest, Spec: raw, Protocol: string(protocol), BusinessUdp: string(udp), PoolFirst: int64(sock.PoolFirst), PoolLast: int64(sock.PoolLast)})
	}
	return s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		n, err := q.GetNode(ctx, string(r.NodeID))
		if err != nil {
			return missing(err)
		}
		if n.RemovedAt.Valid || n.ConnectionMode != "direct" || n.Revision != int64(r.NodeRevision) || n.TargetDnsRevision != int64(r.DNS.Pin.Revision) {
			return problem(domain.RevisionConflict)
		}
		if err = q.I03PutDNS(ctx, dbgen.I03PutDNSParams{NodeID: n.ID, Revision: int64(r.DNS.Pin.Revision), Digest: r.DNS.Pin.Digest}); err != nil {
			return err
		}
		digest, err := q.I03DNS(ctx, dbgen.I03DNSParams{NodeID: n.ID, Revision: int64(r.DNS.Pin.Revision)})
		if err != nil {
			return err
		}
		if digest != r.DNS.Pin.Digest {
			return problem(domain.RevisionConflict)
		}
		for _, kind := range []string{"bootstrap", "target"} {
			servers := r.DNS.Bootstrap
			if kind == "target" {
				servers = r.DNS.Resolvers
			}
			for i, server := range servers {
				if err = q.I03PutDNSUpstream(ctx, dbgen.I03PutDNSUpstreamParams{NodeID: n.ID, Revision: int64(r.DNS.Pin.Revision), Kind: kind, Position: int64(i), Address: server}); err != nil {
					return err
				}
			}
		}
		old, err := q.I03Inventory(ctx, n.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && old.ObservedAt > stamp(r.ObservedAt) {
			return problem(domain.RevisionConflict)
		}
		if err = q.I03PutInventory(ctx, dbgen.I03PutInventoryParams{NodeID: n.ID, NodeRevision: n.Revision, DnsDigest: r.DNS.Pin.Digest, Dns: dns, Loops: loops, ObservedAt: stamp(r.ObservedAt), ExpiresAt: stamp(r.ExpiresAt)}); err != nil {
			return err
		}
		if err = q.I03ClearInterfaces(ctx, n.ID); err != nil {
			return err
		}
		for _, iface := range r.Interfaces {
			if iface.Name == "" || len(iface.Name) > 15 || !iface.Address.IsValid() {
				return problem(domain.InvalidArgument)
			}
			up := int64(0)
			if iface.Up {
				up = 1
			}
			if err = q.I03PutInterface(ctx, dbgen.I03PutInterfaceParams{NodeID: n.ID, Name: iface.Name, Address: iface.Address.String(), Up: up}); err != nil {
				return err
			}
		}
		if err = q.I03ClearSockets(ctx, n.ID); err != nil {
			return err
		}
		for _, sock := range sockets {
			if err = q.I03PutSocket(ctx, sock); err != nil {
				return err
			}
		}
		return nil
	})
}

func emptyV2(node domain.ID, rev, epoch domain.Revision, dns control.DNSInput) control.BundleV2 {
	return control.BundleV2{Format: 2, Kind: domain.Publish, NodeID: node, TargetRevision: rev, RevocationEpoch: epoch, DNS: dns, Listeners: []control.ListenerV2{}, Terminal: []control.TerminalV2{}, Tombstones: []control.Tombstone{}, RetiredLinks: []control.InternalRetirement{}}
}

func (s *Service) NodeSnapshot(ctx context.Context, id domain.ID) (stage3.NodeSnapshot, error) {
	var out stage3.NodeSnapshot
	var a dbgen.B04Artifact
	var n dbgen.Node
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		n, err = q.GetNode(ctx, string(id))
		if err != nil {
			return missing(err)
		}
		if n.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		inv, err := q.I03Inventory(ctx, n.ID)
		if err != nil {
			return missing(err)
		}
		if inv.NodeRevision != n.Revision || inv.ExpiresAt <= stamp(s.options.Now()) {
			return problem(domain.RevisionConflict)
		}
		if json.Unmarshal(inv.Dns, &out.DNS) != nil || json.Unmarshal(inv.Loops, &out.Loops) != nil || out.DNS.Pin.Digest != inv.DnsDigest || int64(out.DNS.Pin.Revision) != n.TargetDnsRevision {
			return problem(domain.RevisionConflict)
		}
		out.Revision = domain.Revision(n.Revision)
		revocations, e := q.I03Revocations(ctx, dbgen.I03RevocationsParams{NodeID: n.ID, Now: stamp(s.options.Now())})
		if e != nil {
			return e
		}
		for _, r := range revocations {
			at := r.ExpiresAt
			reason := "expired"
			if r.RevokedAt.Valid {
				at = r.RevokedAt.String
				reason = "revoked"
			}
			when, e := time.Parse(time.RFC3339Nano, at)
			if e != nil {
				return e
			}
			out.PendingTombstones = append(out.PendingTombstones, control.Tombstone{IdentityID: domain.ID(r.IdentityID), Generation: domain.Revision(r.Generation), Epoch: domain.Revision(n.RevocationEpoch), At: when, Reason: reason})
		}

		socks, err := q.I03Sockets(ctx, n.ID)
		if err != nil {
			return err
		}
		caps := map[string]bool{}
		for _, row := range socks {
			sock := stage3.PinnedSocketSpec{Pin: control.Pin{ID: domain.ID(row.ConfigID), Revision: domain.Revision(row.ConfigRevision), Digest: row.ConfigDigest}, Protocol: domain.Protocol(row.Protocol), PoolFirst: uint16(row.PoolFirst), PoolLast: uint16(row.PoolLast)}
			if json.Unmarshal(row.Spec, &sock.Spec) != nil {
				return problem(domain.RevisionConflict)
			}
			out.Sockets = append(out.Sockets, sock)
			if !caps[row.Protocol] {
				out.Capabilities = append(out.Capabilities, stage3.ProtocolCapability{Protocol: domain.Protocol(row.Protocol), Available: true, BusinessUDP: domain.Verification(row.BusinessUdp)})
				caps[row.Protocol] = true
			}
		}
		a, err = q.I03ConfirmedArtifact(ctx, n.ID)
		if errors.Is(err, sql.ErrNoRows) {
			pristine, e := q.I03PristineNode(ctx, n.ID)
			if e != nil {
				return e
			}
			if !pristine.Valid || !pristine.Bool {
				return problem(domain.GrantOriginUncertain)
			}
			out.BaseState = "unconfirmed_empty"
			out.Extended = ptr(emptyV2(id, out.Revision, domain.Revision(n.RevocationEpoch), out.DNS))
			return nil
		}
		if err != nil {
			return err
		}
		st, err := q.B04GetState(ctx, n.ID)
		if err != nil {
			return err
		}
		usable, e := q.I03BaseUsable(ctx, dbgen.I03BaseUsableParams{NodeID: n.ID, TargetRevision: a.TargetRevision})
		if e != nil {
			return e
		}
		if !usable {
			return problem(domain.GrantOriginUncertain)
		}
		if a.TargetRevision > n.Revision || st.Epoch != n.RevocationEpoch || st.EvidenceTaskID != "" {
			return problem(domain.GrantOriginUncertain)
		}
		out.BaseState = "confirmed"
		out.Artifact = artifactKey(a)
		out.EffectiveDigest = st.EffectiveDigest
		out.EvidenceAt, err = time.Parse(time.RFC3339Nano, st.EvidenceAt)
		return err
	})
	if err != nil {
		return stage3.NodeSnapshot{}, err
	}
	if a.ID == "" {
		return out, nil
	}
	data, err := s.openBytes(ctx, a)
	if err != nil {
		return stage3.NodeSnapshot{}, err
	}
	defer clear(data)
	switch a.Format {
	case 1:
		b, e := control.DecodeBundle(data, artifactKey(a), id)
		if e != nil {
			return stage3.NodeSnapshot{}, e
		}
		out.Legacy = &b
	case 2:
		b, e := control.DecodeBundleV2(data, artifactKey(a), id)
		if e != nil {
			return stage3.NodeSnapshot{}, e
		}
		out.Extended = &b
	default:
		return stage3.NodeSnapshot{}, problem(domain.NotImplemented)
	}
	return out, nil
}
func ptr[T any](v T) *T { return &v }

// inventoryFence runs on the caller's short transaction, never by calling back
// through Database. All hashes/crypto and external preparation precede it.
func inventoryFence(ctx context.Context, q *dbgen.Queries, node domain.ID, snap stage3.NodeSnapshot, sock stage3.PinnedSocketSpec, now time.Time) error {
	n, err := q.GetNode(ctx, string(node))
	if err != nil {
		return err
	}
	inv, err := q.I03Inventory(ctx, n.ID)
	if err != nil {
		return err
	}
	if n.RemovedAt.Valid || n.ConnectionMode != "direct" || n.Revision != int64(snap.Revision) || inv.NodeRevision != n.Revision || inv.DnsDigest != snap.DNS.Pin.Digest || inv.ExpiresAt <= stamp(now) {
		return problem(domain.RevisionConflict)
	}
	rows, err := q.I03Sockets(ctx, n.ID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.ConfigID == string(sock.Pin.ID) && r.ConfigRevision == int64(sock.Pin.Revision) && r.ConfigDigest == sock.Pin.Digest && r.PoolFirst == int64(sock.PoolFirst) && r.PoolLast == int64(sock.PoolLast) {
			return nil
		}
	}
	return problem(domain.RevisionConflict)
}
func findSocket(snap stage3.NodeSnapshot, p control.Pin) (stage3.PinnedSocketSpec, error) {
	for _, s := range snap.Sockets {
		if s.Pin == p {
			return s, nil
		}
	}
	return stage3.PinnedSocketSpec{}, problem(domain.NotFound)
}

func (s *Service) ReserveLink(ctx context.Context, in stage3.LinkReservation) (stage3.LinkLease, error) {
	if len(in.Tail.Hops) == 0 {
		return stage3.LinkLease{}, problem(domain.InvalidArgument)
	}
	digest, err := control.ComponentDigest(in)
	if err != nil {
		return stage3.LinkLease{}, err
	}
	var replay stage3.LinkLease
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		r, e := q.B06LinkReservation(ctx, string(in.OperationID))
		if e != nil {
			return e
		}
		if r.RequestDigest != digest {
			return problem(domain.IdempotencyConflict)
		}
		replay = stage3.LinkLease{ReservationID: domain.ID(r.ReservationID), ListenerID: domain.ID(r.ListenerID), CredentialRef: domain.ID(r.CredentialRef), Link: control.Pin{ID: domain.ID(r.ID), Revision: domain.Revision(r.Revision), Digest: r.SuffixDigest}}
		return nil
	})
	if err == nil {
		return replay, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return replay, err
	}
	h := in.Tail.Hops[0]
	snap, err := s.NodeSnapshot(ctx, h.NodeID)
	if err != nil {
		return stage3.LinkLease{}, err
	}
	sock, err := findSocket(snap, h.NodeConfig)
	if err != nil {
		return stage3.LinkLease{}, err
	}
	ids := make([]domain.ID, 4)
	for i, k := range []string{"reservation", "listener", "port", "port"} {
		ids[i], err = s.newID(k)
		if err != nil {
			return stage3.LinkLease{}, err
		}
	}
	prepared, err := routes.PrepareInternal(in, routes.LinkAllocation{Socket: sock, ExpectedRevision: snap.Revision, ReservationID: ids[0], ListenerID: ids[1], TCPPortID: ids[2], UDPPortID: ids[3], Now: s.options.Now()})
	if err != nil {
		return stage3.LinkLease{}, err
	}
	var out stage3.LinkLease
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		if err := inventoryFence(ctx, q, h.NodeID, snap, sock, s.options.Now()); err != nil {
			return err
		}
		var e error
		out, e = routes.ReserveInternal(ctx, q, prepared)
		return e
	})
	return out, err
}

func (s *Service) ReserveIngress(ctx context.Context, in stage3.EntryReservation, route stage3.PublishedRoute) (stage3.IngressLease, error) {
	var out stage3.IngressLease
	digest, err := control.ComponentDigest(in)
	if err != nil {
		return out, err
	}
	want, err := stage3.RouteDigest(route)
	if err != nil || want != in.Route.Digest || route.Pin != in.Route || len(route.Tail.Hops) == 0 {
		return out, problem(domain.InvalidArgument)
	}
	// Exact replay precedes current inventory and deadline validation.
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		l, e := q.I03Lease(ctx, string(in.OperationID))
		if e != nil {
			return e
		}
		if l.RequestDigest != digest {
			return problem(domain.IdempotencyConflict)
		}
		row, e := q.B06Ingress(ctx, string(in.OperationID))
		if e != nil {
			return e
		}
		return json.Unmarshal(row.Lease, &out)
	})
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	h := route.Tail.Hops[0]
	snap, err := s.NodeSnapshot(ctx, h.NodeID)
	if err != nil {
		return out, err
	}
	sock, err := findSocket(snap, h.NodeConfig)
	if err != nil {
		return out, err
	}
	ids := make([]domain.ID, 4)
	for i, k := range []string{"reservation", "listener", "port", "port"} {
		ids[i], err = s.newID(k)
		if err != nil {
			return out, err
		}
	}
	// Marshal every bounded candidate outside the transaction.
	candidates := []uint16{}
	if sock.Spec.RequestedPort != 0 {
		candidates = append(candidates, sock.Spec.RequestedPort)
	}
	if sock.PoolFirst != 0 {
		for p := int(sock.PoolFirst); p <= int(sock.PoolLast); p++ {
			if uint16(p) != sock.Spec.RequestedPort {
				candidates = append(candidates, uint16(p))
			}
		}
	}
	leases := make([]stage3.IngressLease, len(candidates))
	blobs := make([][]byte, len(candidates))
	for i, p := range candidates {
		ep := h.Endpoint
		ep.Port = p
		leases[i] = stage3.IngressLease{ReservationID: ids[0], ListenerID: ids[1], Route: in.Route, Endpoint: ep}
		blobs[i], _ = json.Marshal(leases[i])
	}
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		old, e := q.I03Lease(ctx, string(in.OperationID))
		if e == nil {
			if old.RequestDigest != digest {
				return problem(domain.IdempotencyConflict)
			}
			row, e := q.B06Ingress(ctx, string(in.OperationID))
			if e != nil {
				return e
			}
			return json.Unmarshal(row.Lease, &out)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = subscriptions.CheckReservation(ctx, q, in, s.options.Now()); e != nil {
			return e
		}
		if e = inventoryFence(ctx, q, h.NodeID, snap, sock, s.options.Now()); e != nil {
			return e
		}
		origin, e := q.B06Ingress(ctx, string(in.OperationID))
		if e != nil {
			return e
		}
		if origin.RequestDigest != digest {
			return problem(domain.IdempotencyConflict)
		}
		for i, p := range candidates {
			transports := []string{sock.Spec.Transport}
			if sock.Spec.Transport == "tcp_udp" {
				transports = []string{"tcp", "udp"}
			}
			free := true
			for _, tr := range transports {
				_, e = ports.CheckAvailable(ctx, q, string(h.NodeID), ports.Parse(sock.Spec.Address, int64(p), tr), ports.Owner{})
				if e != nil {
					var prob *domain.Problem
					if errors.As(e, &prob) && prob.Code == domain.PortInUse {
						free = false
						break
					}
					return e
				}
			}
			if !free {
				continue
			}
			if e = q.B06InsertInternalListener(ctx, dbgen.B06InsertInternalListenerParams{ID: string(ids[1]), NodeID: string(h.NodeID), Protocol: string(h.Endpoint.Protocol), ListenAddress: sock.Spec.Address, ListenPort: int64(p), OuterTransport: sock.Spec.Transport, CredentialRef: string(in.CredentialRef), ConfigDigest: in.Route.Digest, DnsRevision: int64(h.DNS.Revision), UpdatedAt: stamp(s.options.Now())}); e != nil {
				return e
			}
			for j, tr := range transports {
				if e = q.B06InsertInternalPort(ctx, dbgen.B06InsertInternalPortParams{ID: string(ids[2+j]), NodeID: string(h.NodeID), ListenerID: nullable(string(ids[1])), Address: sock.Spec.Address, Port: int64(p), Transport: tr}); e != nil {
					return e
				}
			}
			if _, e = q.B06BindIngress(ctx, dbgen.B06BindIngressParams{OperationID: string(in.OperationID), Lease: blobs[i], ListenerID: nullable(string(ids[1]))}); e != nil {
				return e
			}
			if e = q.I03PutLease(ctx, dbgen.I03PutLeaseParams{OperationID: string(in.OperationID), RequestDigest: digest, ReservationID: string(ids[0]), ListenerID: string(ids[1]), ConfigID: string(sock.Pin.ID), ConfigRevision: int64(sock.Pin.Revision), ConfigDigest: sock.Pin.Digest, PreferredPort: int64(sock.Spec.RequestedPort), ActualPort: int64(p)}); e != nil {
				return e
			}
			out = leases[i]
			return nil
		}
		return problem(domain.PortInUse)
	})
	return out, err
}
