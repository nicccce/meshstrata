package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"net/http"
	"testing"
)

func TestArtifactUsesMTLSAndChecksPinnedBytes(t *testing.T) {
	data := []byte("test-only-private-bundle")
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) == 0 || r.URL.Path != "/control/v1/tasks/"+string(testTask())+"/bundle" {
			w.WriteHeader(403)
			return
		}
		w.Write(data)
	})
	client, e := New(clientOptions(server, config, &fakeRuntime{}))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(data)
	key := protocol.ArtifactKey{TaskID: testTask(), NodeID: testNode(), TargetRevision: 1, TargetDigest: hex.EncodeToString(hash[:])}
	got, e := client.DownloadArtifact(context.Background(), key, 1024)
	if e != nil || string(got) != string(data) {
		t.Fatal(e)
	}
	if _, e = client.DownloadArtifact(context.Background(), key, 2); e == nil {
		t.Fatal("oversized artifact accepted")
	}
	key.TargetDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, e = client.DownloadArtifact(context.Background(), key, 1024); e == nil {
		t.Fatal("digest mismatch accepted")
	}
}
