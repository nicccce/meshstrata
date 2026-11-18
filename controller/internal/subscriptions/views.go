package subscriptions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func routePin(i dbgen.B08Intent) control.Pin {
	return control.Pin{ID: domain.ID(i.RouteID), Revision: domain.Revision(i.RouteRevision), Digest: i.RouteDigest}
}

func subjectPin(r dbgen.B08Revision) control.Pin {
	return control.Pin{ID: domain.ID(r.SubscriptionID), Revision: domain.Revision(r.Revision), Digest: r.Digest}
}

func profilePin(r dbgen.B08Revision) control.Pin {
	return control.Pin{ID: domain.ID(r.ProfileID), Revision: domain.Revision(r.ProfileRevision), Digest: r.ProfileDigest}
}

func entryView(i dbgen.B08Intent, now time.Time) (v stage3.EntryView, err error) {
	v.ID, v.IdentityID, v.Generation = domain.ID(i.EntryKey), domain.ID(i.IdentityKey), domain.Revision(i.Generation)
	if err := json.Unmarshal([]byte(i.RouteJson), &v.Route); err != nil {
		return v, err
	}
	if v.Route.Pin != routePin(i) || !v.Route.Pin.Valid() || len(v.Route.Tail.Hops) == 0 {
		return v, problem(unavailable)
	}
	v.Name = v.Route.Name
	// An intent's planned route does not assert a dedicated endpoint exists.
	if i.EndpointJson.Valid {
		if err := json.Unmarshal([]byte(i.EndpointJson.String), &v.Endpoint); err != nil {
			return v, err
		}
	}
	v.ExpiresAt, err = parse(i.ExpiresAt)
	if err != nil {
		return v, err
	}
	v.RevokedAt, err = stringTime(i.RevokedAt)
	if err != nil {
		return v, err
	}
	v.Cleanup, v.Sync = i.CleanupState, "pending"
	if i.RevokedAt.Valid || !v.ExpiresAt.After(now) {
		if v.Cleanup == "none" {
			v.Cleanup = "pending"
		}
		if v.Cleanup == "confirmed" {
			v.Sync = "confirmed" // cleanup, never an active download entry
		}
	} else if i.State == "confirmed" {
		v.Sync = i.State
	}
	if i.State == "failed" && v.ExpiresAt.After(now) {
		v.Sync = "failed"
	}
	return v, nil
}

func (s *Service) Get(ctx context.Context, id domain.ID) (v stage3.SubscriptionView, err error) {
	if !id.Valid() {
		return v, problem(domain.NotFound)
	}
	if err := s.tick(ctx); err != nil {
		return v, localError(ctx, err)
	}
	err = s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
		now, e := dbNow(ctx, q)
		if e != nil {
			return e
		}
		v, e = getView(ctx, q, string(id), now)
		return e
	})
	if err = localError(ctx, err); err != nil {
		return stage3.SubscriptionView{}, err
	}
	for _, entry := range v.Entries {
		digest, err := stage3.RouteDigest(entry.Route)
		if err != nil || entry.Route.Pin.Digest != digest {
			return stage3.SubscriptionView{}, problem(unavailable)
		}
	}
	return v, nil
}

func getView(ctx context.Context, q *dbgen.Queries, id string, now time.Time) (v stage3.SubscriptionView, err error) {
	row, err := q.B08Subscription(ctx, id)
	if err != nil {
		return v, err
	}
	v.Entity = domain.Entity{ID: domain.ID(id), Name: row.Name, Revision: domain.Revision(row.Revision)}
	if v.Entity.UpdatedAt, err = parse(row.UpdatedAt); err != nil {
		return v, err
	}
	if v.ExpiresAt, err = parse(row.ExpiresAt); err != nil {
		return v, err
	}
	if v.RevokedAt, err = stringTime(row.RevokedAt); err != nil {
		return v, err
	}
	v.Entries = []stage3.EntryView{}
	v.Sync = "pending"
	h, err := q.B08Head(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil // 002 legacy record; no reviewed stage3 pins to project
	}
	if err != nil {
		return v, err
	}
	r, err := q.B08Revision(ctx, dbgen.B08RevisionParams{SubscriptionID: id, Revision: h.DesiredRevision})
	if err != nil {
		return v, err
	}
	v.Profile = profilePin(r)
	intents, err := q.B08RevisionEntries(ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: id, Revision: h.DesiredRevision})
	if err != nil {
		return v, err
	}
	v.Sync = "confirmed"
	for _, i := range intents {
		e, err := entryView(i, now)
		if err != nil {
			return v, err
		}
		v.Entries = append(v.Entries, e)
		if e.Sync == "failed" || v.Sync != "failed" && e.Sync == "pending" {
			v.Sync = e.Sync
		}
	}
	pending, err := q.B08CleanupPending(ctx, id)
	if err != nil {
		return v, err
	}
	if pending && v.Sync != "failed" {
		v.Sync = "pending"
	}
	return v, nil
}

// ManagementView is an explicit allowlist for I03's ordinary management DTO.
// It never serializes PublishedRoute, preferences or any credential metadata.
type ManagementView struct {
	domain.Entity
	ExpiresAt time.Time         `json:"expires_at"`
	RevokedAt *time.Time        `json:"revoked_at"`
	Profile   control.Pin       `json:"profile"`
	Entries   []ManagementEntry `json:"entries"`
	Sync      string            `json:"sync"`
}
type ManagementEntry struct {
	ID         domain.ID       `json:"id"`
	IdentityID domain.ID       `json:"identity_id"`
	Generation domain.Revision `json:"generation"`
	Route      control.Pin     `json:"route"`
	Name       string          `json:"name"`
	Host       string          `json:"host,omitempty"`
	Port       uint16          `json:"port,omitempty"`
	Protocol   domain.Protocol `json:"protocol,omitempty"`
	ExpiresAt  time.Time       `json:"expires_at"`
	RevokedAt  *time.Time      `json:"revoked_at"`
	Sync       string          `json:"sync"`
	Cleanup    string          `json:"cleanup"`
}

func copyTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	t := *v
	return &t
}

func ProjectManagement(v stage3.SubscriptionView) ManagementView {
	out := ManagementView{Entity: v.Entity, ExpiresAt: v.ExpiresAt, RevokedAt: copyTime(v.RevokedAt),
		Profile: v.Profile, Sync: v.Sync, Entries: make([]ManagementEntry, 0, len(v.Entries))}
	for _, e := range v.Entries {
		out.Entries = append(out.Entries, ManagementEntry{ID: e.ID, IdentityID: e.IdentityID, Generation: e.Generation,
			Route: e.Route.Pin, Name: e.Name, Host: e.Endpoint.Host, Port: e.Endpoint.Port, Protocol: e.Endpoint.Protocol,
			ExpiresAt: e.ExpiresAt, RevokedAt: copyTime(e.RevokedAt), Sync: e.Sync, Cleanup: e.Cleanup})
	}
	return out
}
