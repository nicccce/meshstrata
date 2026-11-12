package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIDsRevisionAndUnknownMeasurement(t *testing.T) {
	seen := map[ID]bool{}
	for range 100 {
		id, err := NewID("node")
		if err != nil || !id.Valid() || seen[id] {
			t.Fatal(id, err)
		}
		seen[id] = true
	}
	for _, kind := range []string{"", "Node", strings.Repeat("n", 25), "node/x"} {
		if _, err := NewID(kind); err == nil {
			t.Fatal("invalid kind accepted", kind)
		}
	}
	if Revision(0).Valid() || Revision(MaxRevision+1).Valid() || !Revision(MaxRevision).Valid() {
		t.Fatal("unsafe JSON revision")
	}
	data, err := json.Marshal(Measurement{})
	if err != nil || !strings.Contains(string(data), `"value_ms":null`) {
		t.Fatal("null measurement lost", string(data), err)
	}
	for _, q := range []ListQuery{{Limit: 0}, {Limit: 201}, {Limit: 1, Cursor: strings.Repeat("x", 513)}} {
		if q.Validate() == nil {
			t.Fatal("unbounded list")
		}
	}
}
