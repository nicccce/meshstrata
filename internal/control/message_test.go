package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

func TestMessagesAndAuthenticatedIdentity(t *testing.T) {
	node := domain.ID("node_" + strings.Repeat("1", 32))
	task := domain.ID("task_" + strings.Repeat("2", 32))
	msg := domain.ID("msg_" + strings.Repeat("3", 32))
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	result := Result{TaskID: task, NodeResult: domain.NodeResult{NodeID: node, TargetRevision: 1, TargetDigest: hash, Applied: true, Verification: domain.Verified, Rollback: "none", Cleanup: "none", ConfirmedAt: &now}, Sequence: 1}
	command := Command{TaskID: task, NodeID: node, Kind: domain.Publish, TargetRevision: 1, TargetDigest: hash, IdempotencyKey: "key", Deadline: now.Add(time.Minute), DependsOn: []domain.ID{}, RevocationEpoch: 1}
	for _, m := range []Message{
		{Version: 1, Type: "command", MessageID: msg, NodeID: node, SentAt: now, Command: &command},
		{Version: 1, Type: "result", MessageID: msg, NodeID: node, SentAt: now, Result: &result},
		{Version: 1, Type: "receipt", MessageID: msg, NodeID: node, SentAt: now, Receipt: &Receipt{TaskID: task, NodeID: node, Sequence: 1}},
		{Version: 1, Type: "hello", MessageID: msg, NodeID: node, SentAt: now, Hello: &Hello{NodeID: node, RevocationEpoch: 1, PendingResults: []Result{result}}},
	} {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(data, node); err != nil {
			t.Fatal(m.Type, err)
		}
		if _, err := Decode(data, task); err == nil {
			t.Fatal("wrong certificate identity accepted")
		}
		var fields map[string]any
		json.Unmarshal(data, &fields)
		fields["version"] = 2
		bad, _ := json.Marshal(fields)
		if _, err := Decode(bad, node); err == nil {
			t.Fatal("unknown version accepted")
		}
		fields["version"] = 1
		fields["unexpected"] = "secret"
		bad, _ = json.Marshal(fields)
		if _, err := Decode(bad, node); err == nil {
			t.Fatal("unknown field accepted")
		}
		if _, err := Decode(append(data, data...), node); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	}
	m := Message{Version: 1, Type: "result", MessageID: msg, NodeID: node, SentAt: now, Result: &result}
	result.Applied = false
	if m.Validate(node) == nil {
		t.Fatal("false applied and verified accepted")
	}
	result.Applied = true
	m.Command = &command
	if m.Validate(node) == nil {
		t.Fatal("multiple payloads accepted")
	}
	m.Command = nil
	command.Deadline = now
	m.Type = "command"
	m.Result = nil
	m.Command = &command
	if m.Validate(node) == nil {
		t.Fatal("expired envelope accepted")
	}
	if _, err := Decode([]byte(strings.Repeat("x", MaxMessageBytes+1)), node); err == nil {
		t.Fatal("oversize accepted")
	}
}
func TestAuthorizationCannotReviveAtExpiryOrRollback(t *testing.T) {
	now := time.Now()
	a := Authorization{ExpiresAt: now.Add(time.Minute)}
	if !a.Active(now) || a.Active(a.ExpiresAt) {
		t.Fatal("expiration boundary")
	}
	a.RevokedAt = &now
	if a.Active(now.Add(-time.Minute)) {
		t.Fatal("revocation lost to older snapshot time")
	}
}
