package control

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

// Snapshot names BOTH the Controller artifact and the locally filtered rendered
// bytes. A verified artifact may later have a different EffectiveDigest at expiry.
// B05 persists each phase separately; pointer nil means that phase never existed.
type Snapshot struct {
	Format          int             `json:"format,omitempty"`
	TaskID          domain.ID       `json:"task_id"`
	Revision        domain.Revision `json:"revision"`
	ArtifactDigest  string          `json:"artifact_digest"`
	EffectiveDigest string          `json:"effective_digest"`
	Epoch           domain.Revision `json:"epoch"`
}

func (s Snapshot) Validate() error {
	if s.Format != 0 && s.Format != 1 && s.Format != 2 {
		return ErrBinding
	}
	if !s.TaskID.Valid() || !validTarget(s.Revision, s.ArtifactDigest) || !digestPattern.MatchString(s.EffectiveDigest) || !s.Epoch.Valid() {
		return ErrBinding
	}
	return nil
}

// Restriction is evidence of a local decision associated with Result's original
// grant task/target, NOT a fabricated Controller task or an updated publish success.
// ObservedAt is the persisted monotonic wall-clock floor. Epoch is the last known
// Controller epoch (local expiry never increments it). EffectiveDigest names actual
// rendered bytes; restriction success requires fresh B03 revocation evidence.
type Restriction struct {
	IdentityID      domain.ID       `json:"identity_id"`
	ListenerID      domain.ID       `json:"listener_id"`
	Generation      domain.Revision `json:"generation"`
	Reason          string          `json:"reason"`
	ObservedAt      time.Time       `json:"observed_at"`
	Epoch           domain.Revision `json:"epoch"`
	EffectiveDigest string          `json:"effective_digest"`
}

func (r Restriction) validate() error {
	if !r.IdentityID.Valid() || !r.ListenerID.Valid() || !r.Generation.Valid() || !r.Epoch.Valid() || !utc(r.ObservedAt) || !digestPattern.MatchString(r.EffectiveDigest) || (r.Reason != "expired" && r.Reason != "revoked") {
		return ErrAuthorization
	}
	return nil
}

// FilterAuthorizations computes the safe inputs before startup/reload/rollback.
// B05 must persist its returned union + clock floor atomically BEFORE activating
// bytes and enqueue evidence in the same local transaction as its checkpoint.
// This helper is pure; it does not claim persistence, process control or verification.
// localEpoch/now come from durable state: now=max(wall clock, persisted floor).
func FilterAuthorizations(b Bundle, localEpoch domain.Revision, local []Tombstone, now time.Time) ([]BundleListener, []Tombstone, domain.Revision, error) {
	if b.Validate() != nil || !localEpoch.Valid() || !utc(now) {
		return nil, nil, 0, ErrAuthorization
	}
	epoch := localEpoch
	if b.RevocationEpoch > epoch {
		epoch = b.RevocationEpoch
	}
	tombs := map[domain.ID]Tombstone{}
	merge := func(t Tombstone) error {
		if !t.IdentityID.Valid() || !t.Generation.Valid() || !t.Epoch.Valid() || t.Epoch > epoch || !utc(t.At) || (t.Reason != "expired" && t.Reason != "revoked") {
			return ErrAuthorization
		}
		if old, ok := tombs[t.IdentityID]; ok {
			if old.Generation != t.Generation {
				return ErrAuthorization
			}
			if old.At.Before(t.At) {
				t.At = old.At
			}
			if old.Epoch > t.Epoch {
				t.Epoch = old.Epoch
			}
			if old.Reason == "revoked" {
				t.Reason = old.Reason
			}
		}
		tombs[t.IdentityID] = t
		return nil
	}
	for _, list := range [][]Tombstone{local, b.Tombstones} {
		for _, t := range list {
			if err := merge(t); err != nil {
				return nil, nil, 0, err
			}
		}
	}
	active := []BundleListener{}
	for _, l := range b.Listeners {
		a := l.Authorization
		if old, ok := tombs[a.IdentityID]; ok {
			if old.Generation != a.Generation {
				return nil, nil, 0, ErrAuthorization
			}
			continue
		}
		reason := ""
		at := now
		if a.RevokedAt != nil {
			reason = "revoked"
			at = *a.RevokedAt
		} else if !now.Before(a.ExpiresAt) {
			reason = "expired"
			at = a.ExpiresAt
		}
		if reason != "" {
			if err := merge(Tombstone{a.IdentityID, a.Generation, epoch, at, reason}); err != nil {
				return nil, nil, 0, err
			}
			continue
		}
		active = append(active, l)
	}
	out := make([]Tombstone, 0, len(tombs))
	for _, t := range tombs {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IdentityID < out[j].IdentityID })
	return active, out, epoch, nil
}

// Authorization is the legacy read-only expiration view; persistence uses Grant.
type Authorization struct {
	IdentityID domain.ID
	EntryID    domain.ID
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

func (a Authorization) Active(now time.Time) bool {
	return a.RevokedAt == nil && now.Before(a.ExpiresAt)
}

// CheckGrant rejects reuse of an identity with different immutable authorization,
// credentials or listener fields. A later task can carry the same grant; Origin
// remains the first task so offline evidence always has a real ledger parent.
func CheckGrant(previous Grant, next BundleListener) error {
	if previous.Listener.Authorization.IdentityID != next.Authorization.IdentityID {
		return ErrAuthorization
	}
	a, err := json.Marshal(previous.Listener)
	if err != nil {
		return ErrAuthorization
	}
	b, err := json.Marshal(next)
	if err != nil || !bytes.Equal(a, b) {
		return ErrAuthorization
	}
	return nil
}
