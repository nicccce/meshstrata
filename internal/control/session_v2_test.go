package control

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestV2EnvelopeStrictSessionAndCapability(t *testing.T) {
	node := bid("node")
	m := SessionV2{Message: Message{Version: 2, Type: "hello", MessageID: bid("message"), NodeID: node, SentAt: time.Now(), Hello: &Hello{NodeID: node, Runtime: true, RevocationEpoch: 1, PendingResults: []Result{}}}, SessionID: bid("session"), RecoveryFormat: 2}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSessionV2(data, node); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data, node); err == nil {
		t.Fatal("legacy decoder accepted v2 session")
	}
	for _, bad := range [][]byte{
		bytes.Replace(data, []byte(`"recovery_format":2`), []byte(`"recovery_format":1`), 1),
		bytes.Replace(data, []byte(`"runtime":true`), []byte(`"runtime":false`), 1),
		bytes.Replace(data, []byte(`"runtime":true`), []byte(`"runtime":true,"runtime":true`), 1),
		bytes.Replace(data, []byte(`"version":2`), []byte(`"version":2,"version":2`), 1),
		bytes.Replace(data, []byte(`"version":2`), []byte(`"version":2,"arbitrary":1`), 1),
		append(append([]byte{}, data...), []byte(`{}`)...),
	} {
		if _, err := DecodeSessionV2(bad, node); err == nil {
			t.Fatal("malformed v2 envelope accepted", string(bad))
		}
	}
	m.Type, m.Hello, m.RecoveryFormat = "welcome", nil, 0
	if m.Validate(node) != nil {
		t.Fatal("valid welcome")
	}
	m.RecoveryFormat = 2
	if m.Validate(node) == nil {
		t.Fatal("welcome invented agent capability")
	}
}
