package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

// HelloSource is the limited stage-1 boundary. It cannot execute or acknowledge.
// B05 must use Runtime with atomic durable checkpoint/outbox semantics instead.
type HelloSource interface {
	Hello(context.Context) (protocol.Hello, error)
}

type ControlOnly struct {
	dir  string
	node domain.ID
}
type pristineState struct {
	Version         int       `json:"version"`
	Mode            string    `json:"mode"`
	NodeID          domain.ID `json:"node_id"`
	AppliedRevision int       `json:"applied_revision"`
	AppliedDigest   string    `json:"applied_digest"`
	RevocationEpoch int       `json:"revocation_epoch"`
	PendingResults  int       `json:"pending_results"`
}

// OpenControlOnly requires the component lock. Unknown state is never erased or
// interpreted as a new business runtime. The marker binds this fact to node ID.
func OpenControlOnly(dir string, node domain.ID) (*ControlOnly, error) {
	c := &ControlOnly{dir: dir, node: node}
	if !node.Valid() || c.checkFiles() != nil {
		return nil, ErrUnavailable
	}
	path := filepath.Join(dir, "stage1-control.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		raw, _ := json.Marshal(pristineState{Version: 1, Mode: "control_only", NodeID: node, RevocationEpoch: 1})
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, ErrUnavailable
		}
		_, e = f.Write(raw)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil || ce != nil {
			return nil, ErrUnavailable
		}
		d, e := os.Open(dir)
		if e != nil {
			return nil, ErrUnavailable
		}
		e = d.Sync()
		d.Close()
		if e != nil {
			return nil, ErrUnavailable
		}
	}
	if _, err := c.Hello(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *ControlOnly) checkFiles() error {
	entries, e := os.ReadDir(c.dir)
	if e != nil {
		return ErrUnavailable
	}
	for _, v := range entries {
		switch v.Name() {
		case "process.lock", "health.sock", "identity", "stage1-control.json":
		default:
			return ErrUnavailable
		}
	}
	return nil
}
func (c *ControlOnly) Hello(ctx context.Context) (protocol.Hello, error) {
	if ctx.Err() != nil {
		return protocol.Hello{}, ctx.Err()
	}
	if c.checkFiles() != nil {
		return protocol.Hello{}, ErrUnavailable
	}
	raw, e := privatefile.Read(filepath.Join(c.dir, "stage1-control.json"), 4096)
	if e != nil {
		return protocol.Hello{}, ErrUnavailable
	}
	defer clear(raw)
	var state pristineState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || state != (pristineState{Version: 1, Mode: "control_only", NodeID: c.node, RevocationEpoch: 1}) {
		return protocol.Hello{}, ErrUnavailable
	}
	return protocol.Hello{NodeID: c.node, RevocationEpoch: 1, PendingResults: []protocol.Result{}}, nil
}
