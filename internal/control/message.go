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
	Sequence int64 `json:"sequence"` // monotonic per (task,node), durable before send
}
type Receipt struct {
	TaskID   domain.ID `json:"task_id"`
	NodeID   domain.ID `json:"node_id"`
	Sequence int64     `json:"sequence"` // Controller persisted result; Agent may retire outbox item
}
type Hello struct {
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

// Checkpoint records candidate/applied/verified/startup separately. B05 must persist it
// atomically with the authorization tombstones before acknowledging a command.
type Checkpoint struct {
	NodeID             domain.ID
	TaskID             domain.ID
	TargetRevision     domain.Revision
	CandidateDigest    string
	AppliedDigest      string
	VerifiedDigest     string
	StartupDigest      string
	PreviousGoodDigest string
	RevocationEpoch    domain.Revision
	ResultSequence     int64
}
type Authorization struct {
	IdentityID domain.ID // tombstones key this identity, never erase them on entry renewal
	EntryID    domain.ID
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

func (a Authorization) Active(now time.Time) bool {
	return a.RevokedAt == nil && now.Before(a.ExpiresAt)
}

// LocalState is an Agent-only boundary. It never uploads local Mihomo API secrets.
// Commit must merge monotonic tombstones and persist checkpoint + result outbox in one operation.
type LocalState interface {
	Load(context.Context) (Checkpoint, []Authorization, []Result, error)
	Commit(context.Context, Checkpoint, []Authorization, Result) error
	Acknowledge(context.Context, Receipt) error
}
