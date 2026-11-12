package domain

import "time"

type TaskKind string

const (
	Publish      TaskKind = "publish"
	Rollback     TaskKind = "rollback"
	Revoke       TaskKind = "revoke"
	Cleanup      TaskKind = "cleanup"
	QueryStatus  TaskKind = "query_status"
	TestProtocol TaskKind = "test_protocol"
	Measure      TaskKind = "measure"
	CheckEgress  TaskKind = "check_egress"
	QueryUsage   TaskKind = "query_usage"
	ReadLogs     TaskKind = "read_logs"
)

func (k TaskKind) Valid() bool {
	switch k {
	case Publish, Rollback, Revoke, Cleanup, QueryStatus, TestProtocol, Measure, CheckEgress, QueryUsage, ReadLogs:
		return true
	}
	return false
}

type TaskState string

const (
	Queued     TaskState = "queued"
	Running    TaskState = "running"
	Succeeded  TaskState = "succeeded"
	TaskFailed TaskState = "failed"
	Cancelled  TaskState = "cancelled"
	TimedOut   TaskState = "timed_out"
)

type Task struct {
	Revision  Revision     `json:"revision"` // task ledger CAS, independent of subject revision
	ID        ID           `json:"id"`
	Kind      TaskKind     `json:"kind"`
	Subject   RevisionRef  `json:"subject"`
	State     TaskState    `json:"state"`
	Deadline  time.Time    `json:"deadline"`
	CreatedAt time.Time    `json:"created_at"`
	Nodes     []NodeResult `json:"nodes"`
}
type TaskAccepted struct {
	TaskID ID `json:"task_id"`
}
type NodeResult struct {
	NodeID         ID           `json:"node_id"`
	TargetRevision Revision     `json:"target_revision"`
	TargetDigest   string       `json:"target_digest"`
	Applied        bool         `json:"applied"`
	Verification   Verification `json:"verification"`
	Rollback       string       `json:"rollback"` // none / pending / confirmed / failed
	Cleanup        string       `json:"cleanup"`  // none / pending / confirmed
	ConfirmedAt    *time.Time   `json:"confirmed_at"`
	Issue          *Problem     `json:"issue,omitempty"`
}
