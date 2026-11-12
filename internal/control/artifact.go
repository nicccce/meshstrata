package control

import (
	"context"
	"io"

	"github.com/nicccce/meshstrata/internal/domain"
)

// ArtifactKey names an immutable, node-scoped configuration bundle. The digest
// covers the exact transferred bytes and its pinned rule/DNS/egress revision vector.
// B04 supplies the bundle compiler/store, B02 enforces mTLS identity at transport,
// B05 verifies the bytes before preparing a candidate. Secrets are never in URLs.
type ArtifactKey struct {
	TaskID         domain.ID
	NodeID         domain.ID
	TargetRevision domain.Revision
	TargetDigest   string
}

// ArtifactSource must authorize the authenticated node against the ledger; knowing
// an artifact key is not authority. Streams are bounded, private and never logged.
type ArtifactSource interface {
	Open(context.Context, domain.ID, ArtifactKey) (io.ReadCloser, error)
}
