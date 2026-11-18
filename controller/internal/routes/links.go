package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/ports"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

const planPurpose secretstore.Purpose = "route_release_plan"

func derived(kind string, op domain.ID, suffix string) domain.ID {
	h := sha256.Sum256([]byte(string(op) + ":" + kind + ":" + suffix))
	return domain.ID(kind + "_" + hex.EncodeToString(h[:16]))
}
func linkOp(op domain.ID, i int, key string) domain.ID {
	return derived("link_op", op, fmt.Sprintf("%d:%s", i, key))
}

func (s *Service) allocate(ctx context.Context, r reviewed, op domain.ID, subject control.Pin, deadline time.Time) (control.RouteTail, []stage3.LinkLease, error) {
	n := len(r.inputs)
	portsUsed := make([]uint16, n)
	for i, h := range r.draft.Hops {
		portsUsed[i] = h.Endpoint.Port
	}
	leases := make([]stage3.LinkLease, n)
	t := tailFor(r.draft, portsUsed)
	for i := n - 1; i >= 0; i-- {
		in := r.inputs[i]
		explicit := t.Hops[i].Endpoint.Port
		lo, hi := in.sock.PoolFirst, in.sock.PoolLast
		if explicit != 0 {
			lo, hi = explicit, explicit
		}
		chosen := false
		// Prefer a complete confirmed suffix over allocating a fresh port.
		// Never choose a different protocol or path when a port is in use.
		candidates := make([]uint16, 0, len(in.base.Listeners))
		if i > 0 {
			for _, l := range in.base.Listeners {
				port := l.Socket.Port
				if l.Internal == nil || port < lo || port > hi {
					continue
				}
				t.Hops[i].Endpoint.Port = port
				want := suffix(t, i)
				key, _ := want.Key()
				if l.Internal.Digest == key {
					candidates = append(candidates, port)
				}
			}
		}
		tried := map[uint16]bool{}
		try := func(port uint16) (bool, error) {
			if tried[port] {
				return false, nil
			}
			tried[port] = true
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			t.Hops[i].Endpoint.Port = port
			want := suffix(t, i)
			key, err := want.Key()
			if err != nil {
				return false, problem(domain.InvalidArgument, hopPath(i, "/endpoint"))
			}
			ignore := ""
			if i > 0 {
				for _, l := range in.base.Listeners {
					if l.Internal != nil && l.Internal.Digest == key && l.Socket.Port == port {
						var row dbgen.B06InternalLink
						e := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
							var err error
							row, err = q.B06LinkBySuffix(ctx, key)
							return err
						})
						if e != nil && !errors.Is(e, sql.ErrNoRows) {
							return false, e
						}
						if e == nil && row.State == "active" && row.ID == string(l.Internal.ID) &&
							row.Revision == int64(l.Internal.Revision) && row.ListenerID == string(l.Socket.ListenerID) &&
							row.CredentialRef == string(l.Socket.Credential.Ref) {
							if e = s.confirmReceiver(ctx, in); e != nil {
								return false, e
							}
							ignore = string(l.Socket.ListenerID)
						}
						break
					}
				}
				// The same attempt may already have reserved its port before a restart,
				// while the node snapshot still has the older confirmed bundle.
				if ignore == "" && op != "" && overlaps(in.ports, in.base, in.sock.Spec, port, "") {
					var prior dbgen.B06LinkReservationRow
					err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
						var e error
						prior, e = q.B06LinkReservation(ctx, string(linkOp(op, i, key)))
						return e
					})
					if err == nil && prior.SuffixDigest == key && prior.State == "active" && !prior.ReleasedAt.Valid {
						ignore = prior.ListenerID
					} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return false, err
					}
				}
			}
			if overlaps(in.ports, in.base, in.sock.Spec, port, ignore) {
				return false, nil
			}
			if i == 0 || op == "" {
				return true, nil
			}
			l, err := s.reserveLink(ctx, op, i, subject, want, deadline)
			if err != nil {
				if codeIs(err, domain.PortInUse) {
					return false, nil
				}
				return false, err
			}
			leases[i] = l
			if l.Link.ID != derived("link", linkOp(op, i, key), "") && ignore != string(l.ListenerID) {
				return false, problem(domain.RevisionConflict, hopPath(i, "/link"))
			}
			return true, nil
		}
		if i > 0 && op != "" {
			var previous dbgen.B06InternalLink
			e := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
				var err error
				previous, err = q.B06PublicationLinkAt(ctx, dbgen.B06PublicationLinkAtParams{PublicationID: string(op), Position: int64(i)})
				return err
			})
			if e == nil {
				var tail control.RouteTail
				if previous.State != "active" || json.Unmarshal(previous.Tail, &tail) != nil || len(tail.Hops) == 0 {
					return control.RouteTail{}, nil, problem(domain.GrantOriginUncertain, hopPath(i, "/link"))
				}
				port := tail.Hops[0].Endpoint.Port
				t.Hops[i].Endpoint.Port = port
				k, _ := suffix(t, i).Key()
				if port < lo || port > hi || k != previous.SuffixDigest {
					return control.RouteTail{}, nil, problem(domain.RevisionConflict, hopPath(i, "/link"))
				}
				lo, hi, candidates = port, port, nil
			} else if !errors.Is(e, sql.ErrNoRows) {
				return control.RouteTail{}, nil, e
			}
		}

		for _, p := range candidates {
			ok, err := try(p)
			if err != nil {
				return control.RouteTail{}, nil, err
			}
			if ok {
				chosen = true
				break
			}
		}
		for p := int(lo); !chosen && p <= int(hi); p++ {
			ok, err := try(uint16(p))
			if err != nil {
				return control.RouteTail{}, nil, err
			}
			chosen = ok
		}
		if !chosen {
			return control.RouteTail{}, nil, problem(domain.PortInUse, hopPath(i, "/endpoint/port"))
		}
	}
	return t, leases, nil
}
func suffix(t control.RouteTail, i int) control.RouteTail {
	t.Hops = append([]control.HopSpec(nil), t.Hops[i:]...)
	return t
}

func (s *Service) reserveLink(ctx context.Context, pub domain.ID, i int, subject control.Pin, tail control.RouteTail, deadline time.Time) (stage3.LinkLease, error) {
	none := stage3.LinkLease{}
	digest, err := tail.Key()
	if err != nil {
		return none, err
	}
	id := linkOp(pub, i, digest)
	link := derived("link", id, "")
	ref := derived("credential", id, "")
	request := stage3.LinkReservation{OperationID: id, Subject: subject, Tail: tail, ProposedLink: control.Pin{ID: link, Revision: 1, Digest: digest}, CredentialRef: ref, Deadline: deadline}
	var candidate dbgen.B06LinkCandidate
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		candidate, e = q.B06LinkCandidate(ctx, string(id))
		return e
	})
	if errors.Is(err, sql.ErrNoRows) {
		p, genErr := s.randomProxy(tail.Hops[0].Endpoint)
		if genErr != nil {
			return none, genErr
		}
		plain, _ := json.Marshal(p)
		sealed, sealErr := s.opts.Cipher.Seal(ctx, secretstore.Binding{Ref: ref, Purpose: secretstore.InternalProxy}, plain)
		clear(plain)
		if sealErr != nil {
			return none, sealErr
		}
		err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
			if e := q.B06InsertLinkCandidate(ctx, dbgen.B06InsertLinkCandidateParams{OperationID: string(id), PublicationID: string(pub), Position: int64(i), SuffixDigest: digest,
				ProposedLink: string(link), CredentialRef: string(ref), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext}); e != nil {
				return e
			}
			var e error
			candidate, e = q.B06LinkCandidate(ctx, string(id))
			return e
		})
	}
	if err != nil {
		return none, err
	}
	if candidate.ProposedLink != string(link) || candidate.CredentialRef != string(ref) || candidate.SuffixDigest != digest {
		return none, problem(domain.IdempotencyConflict, "/link")
	}
	lease, err := s.opts.Infra.ReserveLink(ctx, request)
	if err != nil {
		return none, dependency(ctx, err, hopPath(i, "/endpoint/port"))
	}
	if !lease.ListenerID.Valid() || !lease.ReservationID.Valid() || !lease.Link.Valid() || lease.Link.Digest != digest || !lease.CredentialRef.Valid() {
		return none, problem(domain.InvalidArgument, "/link/lease")
	}
	// A gateway receipt cannot substitute a different listener or resource for
	// the one actually reserved in the shared ledger.
	var row dbgen.B06LinkReservationRow
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		row, e = q.B06LinkReservation(ctx, string(id))
		return e
	})
	if err != nil {
		return none, err
	}
	reqHash, err := control.ComponentDigest(request)
	if err != nil || row.RequestDigest != reqHash {
		return none, problem(domain.GrantOriginUncertain, "/link/lease")
	}

	if row.State != "active" || row.ReleasedAt.Valid || row.SuffixDigest != digest || row.ID != string(lease.Link.ID) || row.Revision != int64(lease.Link.Revision) ||
		row.CredentialRef != string(lease.CredentialRef) || row.ListenerID != string(lease.ListenerID) || row.ReservationID != string(lease.ReservationID) {
		return none, problem(domain.GrantOriginUncertain, "/link/lease")
	}
	return lease, nil
}

func (s *Service) randomProxy(ep control.ProxyEndpoint) (stage3.ProxySecret, error) {
	n := 32
	if ep.Cipher == "2022-blake3-aes-128-gcm" || ep.Protocol == domain.VLESS {
		n = 16
	}
	var random [32]byte
	defer clear(random[:])
	s.entropy.Lock()
	_, err := io.ReadFull(s.opts.Random, random[:n])
	s.entropy.Unlock()
	if err != nil {
		return stage3.ProxySecret{}, err
	}
	if ep.Protocol == domain.VLESS {
		random[6] = random[6]&15 | 64
		random[8] = random[8]&63 | 128
		u := hex.EncodeToString(random[:16])
		return stage3.ProxySecret{UUID: u[:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:]}, nil
	}
	return stage3.ProxySecret{Password: base64.StdEncoding.EncodeToString(random[:n])}, nil
}

// LinkAllocation is prepared by the gateway before its write transaction.
// Socket/DNS and port pool are the exact, rechecked Controller node inventory.
type LinkAllocation struct {
	Socket                                          stage3.PinnedSocketSpec
	ExpectedRevision                                domain.Revision
	ReservationID, ListenerID, TCPPortID, UDPPortID domain.ID
	Now                                             time.Time
}

// PreparedLink contains the canonical binding prepared outside the write transaction.
type PreparedLink struct {
	request     stage3.LinkReservation
	allocation  LinkAllocation
	key, digest string
	data        []byte
	pin         control.Pin
}

// PrepareInternal computes immutable hashes outside the allocation transaction.
func PrepareInternal(in stage3.LinkReservation, a LinkAllocation) (PreparedLink, error) {
	key, err := in.Tail.Key()
	if err != nil || !in.OperationID.Valid() || !in.Subject.Valid() || !in.ProposedLink.Valid() ||
		in.ProposedLink.Revision != 1 || in.ProposedLink.Digest != key || !in.CredentialRef.Valid() || !utc(in.Deadline) {
		return PreparedLink{}, problem(domain.InvalidArgument, "/link")
	}
	digest, err := control.ComponentDigest(in)
	if err != nil {
		return PreparedLink{}, err
	}
	data, err := json.Marshal(in.Tail)
	if err != nil {
		return PreparedLink{}, err
	}
	in.Tail = suffix(in.Tail, 0)
	buf, err := json.Marshal(a.Socket.Spec)
	if err != nil {
		return PreparedLink{}, err
	}
	var spec control.SocketSpec
	if err = json.Unmarshal(buf, &spec); err != nil {
		return PreparedLink{}, err
	}
	a.Socket.Spec = spec
	pin, err := stage3.NewPin(a.Socket.Pin.ID, a.Socket.Pin.Revision, spec)
	if err != nil {
		return PreparedLink{}, err
	}
	return PreparedLink{request: in, allocation: a, key: key, digest: digest, data: data, pin: pin}, nil
}

// ReserveInternal is the B06 portion of Infrastructure.ReserveLink. Call it
// inside the gateway's shared write transaction after checking the current
// node inventory. It returns a real listener and port reservation, or attaches
// this operation to exactly the existing live suffix.
func ReserveInternal(ctx context.Context, q *dbgen.Queries, prepared PreparedLink) (stage3.LinkLease, error) {
	none := stage3.LinkLease{}
	in, a, key, digest, data := prepared.request, prepared.allocation, prepared.key, prepared.digest, prepared.data
	if !in.OperationID.Valid() || len(in.Tail.Hops) < 1 {
		return none, problem(domain.InvalidArgument, "/link")
	}
	row, err := q.B06LinkReservation(ctx, string(in.OperationID))
	if err == nil {
		if row.RequestDigest != digest {
			return none, problem(domain.IdempotencyConflict, "/link")
		}
		return stage3.LinkLease{ReservationID: domain.ID(row.ReservationID), Link: control.Pin{ID: domain.ID(row.ID), Revision: domain.Revision(row.Revision), Digest: row.SuffixDigest},
			ListenerID: domain.ID(row.ListenerID), CredentialRef: domain.ID(row.CredentialRef)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return none, err
	}
	if !in.Deadline.After(a.Now) {
		return none, problem(domain.TaskExpired, "/deadline")
	}
	candidate, err := q.B06LinkCandidate(ctx, string(in.OperationID))
	if err != nil {
		return none, err
	}
	pub, err := q.B06Publication(ctx, candidate.PublicationID)
	if err != nil {
		return none, err
	}
	v, err := q.B06RouteVersion(ctx, dbgen.B06RouteVersionParams{RouteID: pub.RouteID, Revision: pub.Revision})
	if err != nil {
		return none, err
	}
	if pub.RouteID != string(in.Subject.ID) || pub.Revision != int64(in.Subject.Revision) || v.DraftDigest != in.Subject.Digest || pub.Deadline != timeText(in.Deadline) ||
		candidate.ProposedLink != string(in.ProposedLink.ID) || candidate.CredentialRef != string(in.CredentialRef) || candidate.SuffixDigest != key || pub.State != "preparing" {
		return none, problem(domain.IdempotencyConflict, "/link")
	}
	_, err = q.B06PublicationLinkAt(ctx, dbgen.B06PublicationLinkAtParams{PublicationID: candidate.PublicationID, Position: candidate.Position})
	if err == nil {
		return none, problem(domain.RevisionConflict, "/link")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return none, err
	}

	h := in.Tail.Hops[0]
	node, err := q.GetNode(ctx, string(h.NodeID))
	if err != nil {
		return none, err
	}
	if node.RemovedAt.Valid {
		return none, problem(domain.NotFound, "/link/node")
	}
	if node.ConnectionMode != "direct" {
		return none, problem(domain.NotImplemented, "/link/node")
	}
	if domain.Revision(node.Revision) != a.ExpectedRevision || domain.Revision(node.TargetDnsRevision) != h.DNS.Revision {
		return none, revisionConflict(node.Revision)
	}

	if prepared.pin != h.NodeConfig || prepared.pin != a.Socket.Pin || !socketValid(a.Socket.Spec, h.Endpoint, in.Tail.BusinessUDP) ||
		a.Socket.Spec.RequestedPort != 0 && a.Socket.Spec.RequestedPort != h.Endpoint.Port || !a.ReservationID.Valid() {
		return none, problem(domain.InvalidArgument, "/link/socket")
	}
	existing, err := q.B06LinkBySuffix(ctx, key)
	if err == nil {
		if existing.State != "active" || existing.NodeID != string(h.NodeID) || !bytes.Equal(existing.Tail, data) {
			return none, problem(domain.PortInUse, "/link")
		}
		l, err := q.B06InternalListener(ctx, existing.ListenerID)
		if err != nil {
			return none, err
		}
		if l.CleanupState != "none" || l.CredentialRef != existing.CredentialRef || l.ListenPort != int64(h.Endpoint.Port) ||
			l.ConfigDigest != key || l.ListenAddress != a.Socket.Spec.Address || l.OuterTransport != a.Socket.Spec.Transport || l.Protocol != string(h.Endpoint.Protocol) || l.DnsRevision != int64(h.DNS.Revision) {
			return none, problem(domain.GrantOriginUncertain, "/link")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return none, err
	} else {
		if !a.ListenerID.Valid() {
			return none, problem(domain.InvalidArgument, "/link/listener")
		}
		listen := a.Socket.Spec
		types := []string{listen.Transport}
		if listen.Transport == "tcp_udp" {
			types = []string{"tcp", "udp"}
		}
		for _, tr := range types {
			if _, err = ports.CheckAvailable(ctx, q, string(h.NodeID), ports.Parse(listen.Address, int64(h.Endpoint.Port), tr), ports.Owner{}); err != nil {
				return none, err
			}
		}
		at := timeText(a.Now)
		if err = q.B01InsertCredential(ctx, dbgen.B01InsertCredentialParams{ID: candidate.CredentialRef, Purpose: string(secretstore.InternalProxy), KeyID: candidate.KeyID, Ciphertext: candidate.Ciphertext, CreatedAt: at}); err != nil {
			return none, err
		}
		if err = q.B06InsertInternalListener(ctx, dbgen.B06InsertInternalListenerParams{ID: string(a.ListenerID), NodeID: string(h.NodeID), Protocol: string(h.Endpoint.Protocol), ListenAddress: listen.Address,
			ListenPort: int64(h.Endpoint.Port), OuterTransport: listen.Transport, CredentialRef: candidate.CredentialRef, ConfigDigest: key, DnsRevision: int64(h.DNS.Revision), UpdatedAt: at}); err != nil {
			return none, err
		}
		for _, tr := range types {
			id := a.TCPPortID
			if tr == "udp" {
				id = a.UDPPortID
			}
			if !id.Valid() {
				return none, problem(domain.InvalidArgument, "/link/port")
			}
			if err = q.B06InsertInternalPort(ctx, dbgen.B06InsertInternalPortParams{ID: string(id), NodeID: string(h.NodeID), ListenerID: sql.NullString{String: string(a.ListenerID), Valid: true},
				Address: listen.Address, Transport: tr, Port: int64(h.Endpoint.Port)}); err != nil {
				return none, err
			}
		}
		existing = dbgen.B06InternalLink{ID: string(in.ProposedLink.ID), Revision: 1, ListenerID: string(a.ListenerID), CredentialRef: candidate.CredentialRef}
		if err = q.B06InsertInternalLink(ctx, dbgen.B06InsertInternalLinkParams{ID: existing.ID, SuffixDigest: key, Tail: data, NodeID: string(h.NodeID), ListenerID: existing.ListenerID, CredentialRef: existing.CredentialRef}); err != nil {
			return none, err
		}
	}
	if err = q.B06InsertLinkReference(ctx, dbgen.B06InsertLinkReferenceParams{OperationID: string(in.OperationID), PublicationID: candidate.PublicationID, Position: candidate.Position, LinkID: existing.ID, ReservationID: string(a.ReservationID), RequestDigest: digest}); err != nil {
		return none, err
	}
	return stage3.LinkLease{ReservationID: a.ReservationID, Link: control.Pin{ID: domain.ID(existing.ID), Revision: 1, Digest: key}, ListenerID: domain.ID(existing.ListenerID), CredentialRef: domain.ID(existing.CredentialRef)}, nil
}

// RetireInternalReference is called only after the gateway proves this exact
// version/intent and all its possible rollback users no longer require the
// reference. It cannot release ports: a separately confirmed cleanup is needed.
func RetireInternalReference(ctx context.Context, q *dbgen.Queries, operation domain.ID, now time.Time) (bool, error) {
	row, err := q.B06LinkReservation(ctx, string(operation))
	if err != nil {
		return false, err
	}
	if row.State != "active" {
		return row.State == "retiring", nil
	}
	pub, err := q.B06ReferencePublication(ctx, string(operation))
	if err != nil {
		return false, err
	}
	switch pub.State {
	case "failed", "cancelled", "timed_out", "rolled_back", "published":
	default:
		return false, problem(domain.DependencyInUse, "/link/publication")
	}
	if pub.State == "published" {
		head, e := q.B06RouteHead(ctx, pub.RouteID)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return false, e
		}
		if e == nil && head == pub.Revision {
			return false, problem(domain.DependencyInUse, "/link/publication")
		}
	}

	at := timeText(now)
	if err = q.B06DropLinkReference(ctx, dbgen.B06DropLinkReferenceParams{At: sql.NullString{String: at, Valid: true}, OperationID: string(operation), LinkID: row.ID}); err != nil {
		return false, err
	}
	return retireIfUnreferenced(ctx, q, row.ID, row.ListenerID)
}

func retireIfUnreferenced(ctx context.Context, q *dbgen.Queries, id, listener string) (bool, error) {
	n, err := q.B06RetireInternalLink(ctx, id)
	if err != nil || n == 0 {
		return false, err
	}
	if err = q.B06SetInternalListenerPending(ctx, listener); err != nil {
		return false, err
	}
	return true, q.B06SetInternalPortsPending(ctx, sql.NullString{String: listener, Valid: true})
}

// ConfirmInternalCleanup finalizes a retiring link after the real cleanup task
// for that link confirms on the owning node; an offline node keeps the port.
func ConfirmInternalCleanup(ctx context.Context, q *dbgen.Queries, link control.Pin, task domain.ID, now time.Time) error {
	row, err := q.B06LinkByID(ctx, string(link.ID))
	if err != nil {
		return err
	}
	if !link.Valid() || !task.Valid() || row.SuffixDigest != link.Digest || row.Revision != int64(link.Revision) || row.State == "active" {
		return problem(domain.GrantOriginUncertain, "/link")
	}
	if row.State == "released" {
		if row.CleanupTaskID.String != string(task) {
			return problem(domain.GrantOriginUncertain, "/link/cleanup")
		}
		return nil
	}
	ok, err := q.B06InternalCleanupEvidence(ctx, dbgen.B06InternalCleanupEvidenceParams{TaskID: string(task), NodeID: row.NodeID, LinkID: row.ID})
	if err != nil {
		return err
	}
	if !ok {
		return problem(domain.GrantOriginUncertain, "/link/cleanup")
	}
	n, err := q.B06ReleaseInternalLink(ctx, dbgen.B06ReleaseInternalLinkParams{ID: row.ID, TaskID: sql.NullString{String: string(task), Valid: true}})
	if err != nil {
		return err
	}
	if n != 1 {
		return problem(domain.GrantOriginUncertain, "/link/cleanup")
	}
	if err = q.B06ConfirmInternalListener(ctx, row.ListenerID); err != nil {
		return err
	}
	return q.B06ReleaseInternalPorts(ctx, dbgen.B06ReleaseInternalPortsParams{At: sql.NullString{String: timeText(now), Valid: true},
		ListenerID: sql.NullString{String: row.ListenerID, Valid: true}})

}
