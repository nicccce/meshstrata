package control

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type pausedArtifact struct {
	*testArtifact
	entered chan struct{}
	resume  chan struct{}
}

func (*pausedArtifact) ArtifactFormat(context.Context, domain.ID, domain.ID) (int, error) {
	return 2, nil
}
func (a *pausedArtifact) Open(ctx context.Context, _ domain.ID, _ protocol.ArtifactKey) (io.ReadCloser, error) {
	return io.NopCloser(&pausedReader{ctx: ctx, entered: a.entered, resume: a.resume, source: bytes.NewReader(a.data)}), nil
}

type pausedReader struct {
	ctx     context.Context
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
	source  *bytes.Reader
}

func (r *pausedReader) Read(b []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-r.resume:
		return r.source.Read(b)
	}
}

func TestV2ArtifactResponseRechecksCurrentSession(t *testing.T) {
	f := wsSetup(t)
	f.control.o.EnableV2, f.control.o.RequireRuntime = true, true
	c, session := f.dialV2(t)
	h := f.v2Envelope("hello", session)
	h.RecoveryFormat = 2
	h.Hello = &protocol.Hello{Runtime: true, NodeID: f.materials.NodeID, RevocationEpoch: 1, PendingResults: []protocol.Result{}}
	if err := c.WriteJSON(h); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !f.control.RuntimeConnected(f.materials.NodeID) {
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	data := []byte("private immutable V2 source")
	a := &pausedArtifact{testArtifact: &testArtifact{node: f.materials.NodeID, key: protocol.ArtifactKey{TaskID: f.task, NodeID: f.materials.NodeID, TargetRevision: 1, TargetDigest: protocol.DigestBytes(data)}, data: data}, entered: make(chan struct{}), resume: make(chan struct{})}
	f.control.o.Artifacts, f.control.o.ArtifactResolver = a, a
	var once sync.Once
	resume := func() { once.Do(func() { close(a.resume) }) }
	defer resume()
	transport := &http.Transport{TLSClientConfig: f.config}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	req, err := http.NewRequest("GET", f.http.URL+"/control/v1/tasks/"+string(f.task)+"/bundle", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Meshstrata-Session", string(session))
	done := make(chan error, 1)
	go func() {
		response, err := client.Do(req)
		if err != nil {
			done <- err
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err == nil && (response.StatusCode != http.StatusForbidden || bytes.Contains(body, data)) {
			err = ErrUnavailable
		}
		done <- err
	}()
	select {
	case <-a.entered:
	case <-time.After(time.Second):
		t.Fatal("artifact not opened")
	}
	_, fresh := f.dialV2(t)
	if fresh == session {
		t.Fatal("session not rotated")
	}
	resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("artifact response stalled")
	}
}
