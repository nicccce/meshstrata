package control

import (
	"encoding/json"
	"github.com/nicccce/meshstrata/internal/domain"
	"time"
)

// BindingV2 retains the original full private binding and its first real fetch.
// Internal links have no entry, subscription identity, or expiration.
type BindingV2 struct {
	Origin   ArtifactKey
	Listener ListenerV2
}

// LinkCleanupV2 records local proof for a permanently retired internal resource.
// It is separate from subscription restriction events and has no generation.
type LinkCleanupV2 struct {
	Link            Pin
	EffectiveDigest string
	ConfirmedAt     time.Time
}

// DecodeArtifact discriminates validated immutable bytes, never re-encodes V1.
func DecodeArtifact(data []byte, key ArtifactKey, node domain.ID) (int, error) {
	if len(data) > MaxBundleBytes || DigestBytes(data) != key.TargetDigest {
		return 0, ErrBinding
	}
	var header struct {
		Format int `json:"format"`
	}
	if json.Unmarshal(data, &header) != nil {
		return 0, ErrBundle
	}
	switch header.Format {
	case 1:
		_, e := DecodeBundle(data, key, node)
		return 1, e
	case 2:
		_, e := DecodeBundleV2(data, key, node)
		return 2, e
	default:
		return 0, ErrBundle
	}
}
