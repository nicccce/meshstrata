// Test-only reader of an offline Agent checkpoint. It emits no credentials.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/localstate"
)

func main() {
	stateDir := flag.String("state", "", "offline isolated Agent state directory")
	node := flag.String("node", "", "enrolled node ID")
	flag.Parse()
	if err := inspect(*stateDir, domain.ID(*node)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspect(stateDir string, node domain.ID) error {
	unlock, err := localstate.Lock(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	dir := filepath.Join(stateDir, "runtime")
	root, err := checkpoint.OpenPrivateDir(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	key, err := root.Read("checkpoint.key", 32)
	if err != nil {
		return err
	}
	defer clear(key)
	store, err := checkpoint.Open(checkpoint.Options{
		Dir: filepath.Join(dir, "checkpoint"), KeyID: "runtime-v1", Key: key, NodeID: node, RequireExisting: true,
	})
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.Load(context.Background())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Revision   domain.Revision `json:"revision"`
		Epoch      domain.Revision `json:"epoch"`
		Outbox     int             `json:"outbox"`
		Tombstones int             `json:"tombstones"`
		Checkpoint any             `json:"checkpoint"`
	}{state.Revision, state.RevocationEpoch, len(state.Outbox), len(state.Tombstones), state.Checkpoint})
}
