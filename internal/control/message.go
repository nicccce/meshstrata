// Package control defines the v1 mTLS WebSocket protocol, not a network listener.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

const Version = 1
const MaxMessageBytes = 1 << 20

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Command struct {
	TaskID          domain.ID       `json:"task_id"`
	NodeID          domain.ID       `json:"node_id"`
	Kind            domain.TaskKind `json:"kind"`
	TargetRevision  domain.Revision `json:"target_revision"`
	TargetDigest    string          `json:"target_digest"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Deadline        time.Time       `json:"deadline"`
	DependsOn       []domain.ID     `json:"depends_on"` // prerequisite node IDs within the task
	RevocationEpoch domain.Revision `json:"revocation_epoch"`
}
type Result struct {
	TaskID domain.ID `json:"task_id"`
	domain.NodeResult
	EffectiveDigest string        `json:"effective_digest,omitempty"` // verified activation bytes
	Sequence        int64         `json:"sequence"`                   // monotonic per (task,node), durable before send
	Restrictions    []Restriction `json:"restrictions,omitempty"`     // local evidence, tied to original grant task
}
type Receipt struct {
	TaskID   domain.ID `json:"task_id"`
	NodeID   domain.ID `json:"node_id"`
	Sequence int64     `json:"sequence"` // Controller persisted result; Agent may retire outbox item
}
type Hello struct {
	Runtime         bool            `json:"runtime,omitempty"` // recovered execution capability
	NodeID          domain.ID       `json:"node_id"`
	AppliedRevision domain.Revision `json:"applied_revision"` // zero before first apply
	AppliedDigest   string          `json:"applied_digest"`   // empty only at revision zero
	RevocationEpoch domain.Revision `json:"revocation_epoch"`
	PendingResults  []Result        `json:"pending_results"` // bounded unacknowledged outbox, not telemetry
}

// Exactly one payload matches Type. NodeID must equal the identity authenticated by mTLS.
type Message struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	MessageID domain.ID `json:"message_id"`
	NodeID    domain.ID `json:"node_id"`
	SentAt    time.Time `json:"sent_at"`
	Command   *Command  `json:"command,omitempty"`
	Result    *Result   `json:"result,omitempty"`
	Receipt   *Receipt  `json:"receipt,omitempty"`
	Hello     *Hello    `json:"hello,omitempty"`
}

func Decode(data []byte, authenticatedNode domain.ID) (Message, error) {
	var m Message
	if len(data) > MaxMessageBytes {
		return m, fmt.Errorf("control message too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid control message")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, fmt.Errorf("multiple control messages")
	}
	return m, m.Validate(authenticatedNode)
}
func validTarget(rev domain.Revision, digest string) bool {
	return rev.Valid() && digestPattern.MatchString(digest)
}
func (r Result) validate(node domain.ID) error {
	if !r.TaskID.Valid() || r.NodeID != node || !validTarget(r.TargetRevision, r.TargetDigest) || r.Sequence < 1 || r.Sequence > int64(domain.MaxRevision) {
		return fmt.Errorf("invalid task result identity or target")
	}
	if r.EffectiveDigest != "" && (r.Verification != domain.Verified || !digestPattern.MatchString(r.EffectiveDigest)) {
		return fmt.Errorf("effective digest requires verified application")
	}
	if len(r.Restrictions) > 32 {
		return ErrAuthorization
	}
	seen := map[domain.ID]bool{}
	for _, evidence := range r.Restrictions {
		if evidence.validate() != nil || seen[evidence.IdentityID] {
			return ErrAuthorization
		}
		seen[evidence.IdentityID] = true
	}
	// Local restriction evidence is not a successful re-application of an expired grant.
	if len(r.Restrictions) > 0 && (r.Applied || r.Verification == domain.Verified) {
		return ErrAuthorization
	}
	switch r.Verification {
	case domain.NotChecked, domain.Verified, domain.Failed, domain.Incomplete:
	default:
		return fmt.Errorf("invalid verification")
	}
	switch r.Rollback {
	case "none", "pending", "confirmed", "failed":
	default:
		return fmt.Errorf("invalid rollback state")
	}
	switch r.Cleanup {
	case "none", "pending", "confirmed":
	default:
		return fmt.Errorf("invalid cleanup state")
	}
	if r.Verification == domain.Verified && (!r.Applied || r.ConfirmedAt == nil || r.ConfirmedAt.IsZero() || r.Issue != nil) {
		return fmt.Errorf("verification requires confirmed application")
	}
	if (r.Rollback == "confirmed" || r.Cleanup == "confirmed") && (r.ConfirmedAt == nil || r.ConfirmedAt.IsZero()) {
		return fmt.Errorf("confirmation time required")
	}
	return nil
}
func (m Message) Validate(authenticatedNode domain.ID) error {
	if m.Version != Version || !m.MessageID.Valid() || !m.NodeID.Valid() || m.NodeID != authenticatedNode || m.SentAt.IsZero() {
		return fmt.Errorf("invalid control envelope or authenticated identity")
	}
	count := 0
	for _, present := range []bool{m.Command != nil, m.Result != nil, m.Receipt != nil, m.Hello != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("exactly one control payload required")
	}
	switch m.Type {
	case "command":
		c := m.Command
		if c == nil || c.DependsOn == nil || !c.TaskID.Valid() || c.NodeID != m.NodeID || !c.Kind.Valid() || !validTarget(c.TargetRevision, c.TargetDigest) || len(c.IdempotencyKey) < 1 || len(c.IdempotencyKey) > 128 || !c.Deadline.After(m.SentAt) || !c.RevocationEpoch.Valid() {
			return fmt.Errorf("invalid command")
		}
		seen := map[domain.ID]bool{}
		for _, id := range c.DependsOn {
			if !id.Valid() || id == c.NodeID || seen[id] {
				return fmt.Errorf("invalid task dependency")
			}
			seen[id] = true
		}
	case "result":
		if m.Result == nil {
			return fmt.Errorf("result payload required")
		}
		return m.Result.validate(m.NodeID)
	case "receipt":
		if m.Receipt == nil || !m.Receipt.TaskID.Valid() || m.Receipt.NodeID != m.NodeID || m.Receipt.Sequence < 1 || m.Receipt.Sequence > int64(domain.MaxRevision) {
			return fmt.Errorf("invalid receipt")
		}
	case "hello":
		h := m.Hello
		if h == nil || h.PendingResults == nil || h.NodeID != m.NodeID || !h.RevocationEpoch.Valid() || len(h.PendingResults) > 100 || !(validTarget(h.AppliedRevision, h.AppliedDigest) || (h.AppliedRevision == 0 && h.AppliedDigest == "")) {
			return fmt.Errorf("invalid reconnect hello")
		}
		for _, r := range h.PendingResults {
			if err := r.validate(m.NodeID); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported control message type")
	}
	return nil
}

// Checkpoint tracks artifact AND effective rendered digests at each phase.
// Candidate survives validation; Applied means reload/start was attempted; Verified
// requires fresh B03 evidence; Startup is the durable restart pointer; PreviousGood
// is a rollback input which must be filtered and verified again, never a bypass.
type Checkpoint struct {
	NodeID       domain.ID
	Candidate    *Snapshot
	Applied      *Snapshot
	Verified     *Snapshot
	Startup      *Snapshot
	PreviousGood *Snapshot
}

// Grant keeps the first real task that authorized an immutable identity. Local
// expiry results use this Origin, even after newer publishes replace the checkpoint.
// Listener is private; never log it. Same-identity fields are immutable (CheckGrant).
type Grant struct {
	Origin   ArtifactKey
	Listener BundleListener
}

// Execution retains command/idempotency identity and the sequence high-water mark
// even after all receipts drain Outbox. LastResult permits durable command replay;
// it must not be discarded or reset by checkpoint rollback or acknowledgement.
// Sequence is monotonic per (Command.TaskID, Command.NodeID); overflow fails closed.
type Execution struct {
	Command    Command
	Sequence   int64
	LastResult *Result
}

// DurableState is Agent-local, independent of Controller SQL and master keys.
// Revision is local CAS; zero only before the first commit. ClockFloor and all
// identity grants/tombstones survive rollback; tombstones are never garbage-collected
// on the basis of a Controller snapshot. Outbox can exceed 100; Hello pages it.
type DurableState struct {
	Revision        domain.Revision
	Checkpoint      Checkpoint
	RevocationEpoch domain.Revision
	ClockFloor      time.Time
	Grants          []Grant
	Tombstones      []Tombstone
	Outbox          []Result
	Executions      []Execution
}

// LocalState transactions can persist intermediate phases without inventing a
// Result. Commit must merge durable grants/tombstones/clock floor monotonically,
// CAS expectedRevision, and atomically persist checkpoint + outbox. Acknowledge
// deletes only the exact (task,node,sequence) receipt, including duplicate receipts.
// B05 implements file/database crash durability and fsync; this is only a boundary.
type LocalState interface {
	Load(context.Context) (DurableState, error)
	Commit(context.Context, domain.Revision, DurableState) error
	Acknowledge(context.Context, Receipt) error
}
