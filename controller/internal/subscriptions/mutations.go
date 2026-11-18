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
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type desiredEntry struct {
	ID         domain.ID
	Route      control.Pin
	Generation domain.Revision
}
type review struct {
	Name        string
	ExpiresAt   time.Time
	Profile     control.Pin
	Preferences stage3.ClientPreferences
	Entries     []desiredEntry
}
type preparedEntry struct {
	snapshot stage3.EntryView
	intent   string
	fresh    *dbgen.B08InsertIntentParams
	sealed   secretstore.Sealed
}
type configPlan struct {
	rev     dbgen.B08InsertRevisionParams
	entries []preparedEntry
	prefs   stage3.ClientPreferences
}
type editing struct {
	sub      dbgen.Subscription
	head     dbgen.B08Head
	selected []dbgen.B08Intent
	legacy   []dbgen.SubscriptionEntry
	draft    stage3.SubscriptionDraft
}

func flag(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func (s *Service) Create(ctx context.Context, m domain.Mutation, d stage3.SubscriptionDraft) (stage3.SubscriptionView, error) {
	if d.ID != "" {
		return stage3.SubscriptionView{}, invalid("/id")
	}
	if d.Preferences.Mode == "" {
		d.Preferences.Mode = "tun"
		d.Preferences.AllRoutesGroup, d.Preferences.GroupByExit = true, true
	}
	return s.changeConfig(ctx, "create", m, d, "", time.Time{})
}

func (s *Service) UpdateConfig(ctx context.Context, m domain.Mutation, d stage3.SubscriptionDraft) (stage3.SubscriptionView, error) {
	return s.changeConfig(ctx, "config", m, d, d.ID, time.Time{})
}

func (s *Service) Renew(ctx context.Context, m domain.Mutation, id domain.ID, exp time.Time) (stage3.SubscriptionView, error) {
	if !validTime(exp) {
		return stage3.SubscriptionView{}, invalid("/expires_at")
	}
	return s.changeConfig(ctx, "renew", m, stage3.SubscriptionDraft{ID: id, ExpiresAt: exp}, id, exp)
}

func (s *Service) changeConfig(ctx context.Context, op string, m domain.Mutation, input stage3.SubscriptionDraft, id domain.ID, renewal time.Time) (stage3.SubscriptionView, error) {
	var empty stage3.SubscriptionView
	create := op == "create"
	if err := checkMutation(m, create); err != nil {
		return empty, err
	}
	if !create && !id.Valid() {
		return empty, invalid("/id")
	}
	input.ExpiresAt = input.ExpiresAt.UTC()
	// Validate bounds before serializing the request, including update requests
	// that omit expiry to retain the existing value.
	if op != "renew" {
		check := input
		if !create && input.ExpiresAt.IsZero() {
			check.ExpiresAt = time.Unix(1, 0).UTC()
		}
		if err := validDraft(check); err != nil {
			return empty, err
		}
	}
	r, err := makeRequest(op, m, input)
	if err != nil {
		return empty, invalid("")
	}
	if out, ok, err := s.replay(ctx, r); err != nil || ok {
		return out, err
	}
	if err := s.tick(ctx); err != nil {
		return empty, localError(ctx, err)
	}
	var old editing
	var now time.Time
	err = s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		now, err = dbNow(ctx, q)
		if err != nil || create {
			return err
		}
		old, err = getEditing(ctx, q, id, m)
		return err
	})
	if err != nil {
		// Another identical operation may have committed since the first read.
		if v, ok, replayErr := s.replay(ctx, r); ok || replayErr != nil {
			return v, replayErr
		}
		return empty, localError(ctx, err)
	}
	d := input
	if create {
		d.ID, err = s.id("sub")
		if err != nil {
			return empty, err
		}
	} else if op == "renew" {
		d = old.draft
		d.ExpiresAt = renewal.UTC()
		if !d.ExpiresAt.After(old.draft.ExpiresAt) {
			return empty, invalid("/expires_at")
		}
	} else {
		if input.ExpiresAt.IsZero() {
			d.ExpiresAt = old.draft.ExpiresAt
		}
		if !d.ExpiresAt.Equal(old.draft.ExpiresAt) {
			return empty, invalid("/expires_at")
		}
	}
	if !d.ExpiresAt.After(now) {
		return empty, invalid("/expires_at")
	}
	plan, err := s.prepareConfig(ctx, d, int64(m.ExpectedRevision)+1, old, op == "renew", now)
	if err != nil {
		return empty, localError(ctx, err)
	}
	var token tokenSeal
	if create {
		token, err = s.sealToken(ctx, d.ID, 1)
		if err != nil {
			return empty, localError(ctx, err)
		}
	}
	return s.mutate(ctx, r, func(q *dbgen.Queries, now time.Time) (stage3.SubscriptionView, error) {
		if !d.ExpiresAt.After(now) {
			return empty, problem(domain.TaskExpired)
		}
		at, p := stamp(now), plan.prefs
		if create {
			err := q.B08InsertSubscription(ctx, dbgen.B08InsertSubscriptionParams{
				ID: string(d.ID), Name: d.Name, ProfileID: string(d.Profile.ID), ProfileRevision: int64(d.Profile.Revision),
				ExpiresAt: stamp(d.ExpiresAt), AllRoutesGroup: flag(p.AllRoutesGroup), GroupByExit: flag(p.GroupByExit),
				ClientMode: p.Mode, UpdatedAt: at,
			})
			if err != nil {
				return empty, err
			}
			if err := q.B08InsertToken(ctx, token.row); err != nil {
				return empty, err
			}
			if err := affected(q.B08InstallToken(ctx, dbgen.B08InstallTokenParams{
				ID: string(d.ID), Version: 1, Hash: token.digest[:], Ref: nullable(token.row.ID),
			})); err != nil {
				return empty, err
			}
		} else {
			row, err := q.B08Subscription(ctx, string(d.ID))
			if err != nil {
				return empty, err
			}
			if err := requireCAS(row, m); err != nil {
				return empty, err
			}
			if err := affected(q.B08ChangeConfig(ctx, dbgen.B08ChangeConfigParams{
				ID: string(d.ID), Expected: row.Revision, Name: d.Name, ProfileID: string(d.Profile.ID), ProfileRevision: int64(d.Profile.Revision),
				ExpiresAt: stamp(d.ExpiresAt), AllRoutesGroup: flag(p.AllRoutesGroup), GroupByExit: flag(p.GroupByExit),
				ClientMode: p.Mode, UpdatedAt: at,
			})); err != nil {
				return empty, err
			}
		}
		if err := installConfig(ctx, q, plan, old, at); err != nil {
			return empty, err
		}
		return getView(ctx, q, string(d.ID), now)
	})
}

func getEditing(ctx context.Context, q *dbgen.Queries, id domain.ID, m domain.Mutation) (v editing, err error) {
	v.sub, err = q.B08Subscription(ctx, string(id))
	if err != nil {
		return v, err
	}
	if err = requireCAS(v.sub, m); err != nil {
		return v, err
	}
	v.head, err = q.B08Head(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, problem(domain.NotImplemented) // no reviewed stage3 pins in 002
	}
	if err != nil {
		return v, err
	}
	r, err := q.B08Revision(ctx, dbgen.B08RevisionParams{SubscriptionID: string(id), Revision: v.head.DesiredRevision})
	if err != nil {
		return v, err
	}
	exp, err := parse(v.sub.ExpiresAt)
	if err != nil {
		return v, err
	}
	v.draft = stage3.SubscriptionDraft{ID: id, Name: v.sub.Name, ExpiresAt: exp, Profile: profilePin(r)}
	if err := json.Unmarshal([]byte(r.PreferencesJson), &v.draft.Preferences); err != nil {
		return v, err
	}
	v.selected, err = q.B08RevisionEntries(ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: string(id), Revision: r.Revision})
	if err != nil {
		return v, err
	}
	for _, i := range v.selected {
		v.draft.Routes = append(v.draft.Routes, routePin(i))
	}
	v.legacy, err = q.B08LegacyEntries(ctx, string(id))
	return v, err
}

func (s *Service) profile(ctx context.Context, pin control.Pin) (stage3.ProfileSnapshot, error) {
	p, err := s.o.Rules.Profile(ctx, pin)
	if err != nil {
		return stage3.ProfileSnapshot{}, safe(ctx, err)
	}
	p, err = clone(p)
	if err != nil {
		return stage3.ProfileSnapshot{}, safe(ctx, err)
	}
	digest, err := control.ComponentDigest(p.Spec)
	if err != nil || !pin.Valid() || p.Pin != pin || digest != pin.Digest {
		return stage3.ProfileSnapshot{}, problem(unavailable)
	}
	for _, set := range p.Spec.Sets {
		digest, err := control.ComponentDigest(set.Spec)
		if err != nil || !set.Pin.Valid() || set.Pin.Digest != digest {
			return stage3.ProfileSnapshot{}, problem(unavailable)
		}
	}
	return p, nil
}

func (s *Service) route(ctx context.Context, pin control.Pin) (stage3.PublishedRoute, error) {
	r, err := s.o.Routes.PublishedRoute(ctx, pin)
	if err != nil {
		return stage3.PublishedRoute{}, safe(ctx, err)
	}
	r, err = clone(r)
	if err != nil {
		return stage3.PublishedRoute{}, safe(ctx, err)
	}
	digest, err := stage3.RouteDigest(r)
	if err != nil || r.Pin != pin || pin.Digest != digest {
		return stage3.PublishedRoute{}, problem(unavailable)
	}
	r.Verification = domain.NotChecked
	return r, nil
}

func (s *Service) prepareConfig(ctx context.Context, d stage3.SubscriptionDraft, rev int64, old editing, rotate bool, now time.Time) (plan configPlan, err error) {
	if err := validDraft(d); err != nil {
		return plan, err
	}
	profile, err := s.profile(ctx, d.Profile)
	if err != nil {
		return plan, err
	}
	existing := map[domain.ID]dbgen.B08Intent{}
	var slot int64
	for _, i := range old.legacy {
		slot = max(slot, i.Position+1)
	}
	for _, i := range old.selected {
		existing[domain.ID(i.RouteID)] = i
		slot = max(slot, i.LegacyPosition+1)
	}
	p := review{Name: d.Name, ExpiresAt: d.ExpiresAt.UTC(), Profile: d.Profile, Preferences: d.Preferences}
	preview := stage3.ClientSnapshot{SubscriptionID: d.ID, Revision: domain.Revision(rev), Preferences: d.Preferences, Profile: profile}
	for _, pin := range d.Routes {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		r, err := s.route(ctx, pin)
		if err != nil {
			return plan, err
		}

		prior, have := existing[pin.ID]
		if !have {
			if err := s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
				g, e := q.I03RouteGeneration(ctx, dbgen.I03RouteGenerationParams{SubscriptionID: string(d.ID), RouteID: string(pin.ID)})
				prior.Generation = g
				return e
			}); err != nil {
				return plan, err
			}
		}
		var e preparedEntry
		if have && routePin(prior) == pin && !rotate {
			if prior.State != "confirmed" || prior.RevokedAt.Valid {
				return plan, problem(domain.DependencyInUse)
			}
			if _, err := rowRoute(prior); err != nil {
				return plan, err
			}
			e.snapshot, err = entryView(prior, now)
			e.intent = prior.ID
		} else {
			e, err = s.prepareEntry(ctx, d, r, rev, prior, slot, now)
			if !have {
				slot++
			}
		}
		if err != nil {
			return plan, err
		}
		p.Entries = append(p.Entries, desiredEntry{e.snapshot.ID, pin, e.snapshot.Generation})
		preview.Entries = append(preview.Entries, e.snapshot)
		plan.entries = append(plan.entries, e)
	}
	// Preview is a pure validation call; give it independent slices/pointers.
	preview, err = clone(preview)
	if err != nil {
		return plan, err
	}
	result, err := s.o.Client.Preview(preview)
	if err != nil {
		return plan, safe(ctx, err)
	}
	for _, issue := range result.Issues {
		switch issue.Code {
		case "ENTRY_NOT_CONFIRMED", "LIMITED_COVERAGE", "DNS_EGRESS_NOT_INFERRED", "DNS_TERMINAL_EGRESS_NOT_CHECKED":
		default:
			return plan, invalid("/preferences")
		}
	}
	pin, err := stage3.NewPin(d.ID, domain.Revision(rev), p)
	if err != nil {
		return plan, err
	}
	pref, err := jsonValue(d.Preferences)
	plan.prefs = d.Preferences
	plan.rev = dbgen.B08InsertRevisionParams{SubscriptionID: string(d.ID), Revision: rev, Name: d.Name, Digest: pin.Digest,
		ExpiresAt: stamp(d.ExpiresAt), ProfileID: string(d.Profile.ID), ProfileRevision: int64(d.Profile.Revision),
		ProfileDigest: d.Profile.Digest, PreferencesJson: pref}
	return plan, err
}

func (s *Service) prepareEntry(ctx context.Context, d stage3.SubscriptionDraft, route stage3.PublishedRoute, rev int64,
	old dbgen.B08Intent, slot int64, now time.Time) (preparedEntry, error) {
	var result preparedEntry
	entry, gen := domain.ID(old.EntryKey), old.Generation+1
	if !domain.Revision(gen).Valid() {
		return result, problem(domain.DependencyInUse)
	}
	if entry == "" {
		var err error
		entry, err = s.id("entry")
		if err != nil {
			return result, err
		}
	} else {
		slot = old.LegacyPosition
	}
	ids := make([]domain.ID, 0, 4)
	for _, kind := range []string{"identity", "cred", "intent", "cleanup"} {
		v, err := s.id(kind)
		if err != nil {
			return result, err
		}
		ids = append(ids, v)
	}
	endpoint := route.Tail.Hops[0].Endpoint
	sealed, err := s.sealProxy(ctx, ids[1], endpoint)
	if err != nil {
		return result, err
	}
	r, err := jsonValue(route)
	if err != nil {
		return result, err
	}
	exp := d.ExpiresAt.UTC()
	return preparedEntry{
		snapshot: stage3.EntryView{ID: entry, IdentityID: ids[0], Generation: domain.Revision(gen),
			Route: route, Name: route.Name, Endpoint: endpoint, ExpiresAt: exp, Sync: "pending", Cleanup: "none"},
		intent: string(ids[2]), sealed: sealed,
		fresh: &dbgen.B08InsertIntentParams{ID: string(ids[2]), SubscriptionID: string(d.ID), SubjectRevision: rev,
			EntryKey: string(entry), IdentityKey: string(ids[0]), CredentialRef: string(ids[1]), CleanupID: string(ids[3]), Generation: gen,
			LegacyPosition: slot, RouteID: string(route.Pin.ID), RouteRevision: int64(route.Pin.Revision), RouteDigest: route.Pin.Digest,
			RouteJson: r, ExpiresAt: stamp(exp), Deadline: stamp(earlier(exp, now.Add(releaseWindow)))},
	}, nil
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func installConfig(ctx context.Context, q *dbgen.Queries, plan configPlan, old editing, at string) error {
	if err := q.B08InsertRevision(ctx, plan.rev); err != nil {
		return err
	}
	keep := map[string]bool{}
	routes := map[domain.ID]bool{}
	for pos, p := range plan.entries {
		keep[p.intent], routes[p.snapshot.Route.Pin.ID] = true, true
		if p.fresh != nil {
			if p.fresh.Deadline <= at {
				return problem(domain.TaskExpired)
			}
			if err := q.B01InsertCredential(ctx, dbgen.B01InsertCredentialParams{ID: p.fresh.CredentialRef,
				Purpose: string(secretstore.ProxyEntry), KeyID: p.sealed.KeyID, Ciphertext: p.sealed.Ciphertext, CreatedAt: at}); err != nil {
				return err
			}
			if err := q.B08InsertIntent(ctx, *p.fresh); err != nil {
				return err
			}
		} else {
			i, err := q.B08Intent(ctx, p.intent)
			if err != nil {
				return err
			}
			if i.State != "confirmed" || i.RevokedAt.Valid || i.ExpiresAt <= at {
				return problem(domain.RevisionConflict)
			}
		}
		if err := q.B08InsertRevisionEntry(ctx, dbgen.B08InsertRevisionEntryParams{SubscriptionID: plan.rev.SubscriptionID,
			Revision: plan.rev.Revision, Position: int64(pos), RouteID: string(p.snapshot.Route.Pin.ID), IntentID: p.intent}); err != nil {
			return err
		}
	}
	if old.head.SubscriptionID == "" {
		if err := q.B08InsertHead(ctx, dbgen.B08InsertHeadParams{SubscriptionID: plan.rev.SubscriptionID, DesiredRevision: plan.rev.Revision}); err != nil {
			return err
		}
	} else {
		if err := affected(q.B08AdvanceDesired(ctx, dbgen.B08AdvanceDesiredParams{Next: plan.rev.Revision,
			SubscriptionID: plan.rev.SubscriptionID, Expected: old.head.DesiredRevision})); err != nil {
			return err
		}
		live, err := q.B08AllLiveIntents(ctx, plan.rev.SubscriptionID)
		if err != nil {
			return err
		}
		for _, i := range live {
			if !keep[i.ID] {
				if err := retire(ctx, q, i, at); err != nil {
					return err
				}
			}
		}
		legacy, err := q.B08LegacyEntries(ctx, plan.rev.SubscriptionID)
		if err != nil {
			return err
		}
		for _, e := range legacy {
			if !routes[domain.ID(e.RouteID)] && !e.RevokedAt.Valid {
				if _, err := q.B08RemoveEntry(ctx, dbgen.B08RemoveEntryParams{ID: e.ID, At: nullable(at)}); err != nil {
					return err
				}
			}
		}
	}
	return confirmRevision(ctx, q, plan.rev.SubscriptionID, plan.rev.Revision, at)
}

func retire(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent, at string) error {
	if i.RevokedAt.Valid {
		return nil
	}
	if err := affected(q.B08RetireIntent(ctx, dbgen.B08RetireIntentParams{ID: i.ID, At: nullable(at)})); err != nil {
		return err
	}
	if i.IdentityID.Valid {
		return affected(q.B08RetireIdentity(ctx, dbgen.B08RetireIdentityParams{ID: i.IdentityID.String, At: nullable(at)}))
	}
	if i.ReserveStarted == 0 {
		if err := affected(q.B08ConfirmNoReserve(ctx, i.ID)); err != nil {
			return err
		}
		return revokeCredential(ctx, q, i, at)
	}
	return nil
}

func revokeCredential(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent, at string) error {
	_, err := q.B01RevokeCredential(ctx, dbgen.B01RevokeCredentialParams{ID: i.CredentialRef,
		Purpose: string(secretstore.ProxyEntry), RevokedAt: nullable(at)})
	return err
}

func (s *Service) Delete(ctx context.Context, m domain.Mutation, id domain.ID) (stage3.SubscriptionView, error) {
	r, err := idRequest("delete", m, id)
	if err != nil {
		return stage3.SubscriptionView{}, err
	}
	return s.mutate(ctx, r, func(q *dbgen.Queries, now time.Time) (stage3.SubscriptionView, error) {
		var empty stage3.SubscriptionView
		row, err := q.B08Subscription(ctx, string(id))
		if err != nil {
			return empty, err
		}
		if err := requireCAS(row, m); err != nil {
			return empty, err
		}
		at := stamp(now)
		if err := affected(q.B08RevokeSubscription(ctx, dbgen.B08RevokeSubscriptionParams{At: nullable(at), ID: row.ID, Expected: row.Revision})); err != nil {
			return empty, err
		}
		if err := retireToken(ctx, q, row, at); err != nil {
			return empty, err
		}
		intents, err := q.B08AllLiveIntents(ctx, row.ID)
		if err != nil {
			return empty, err
		}
		for _, i := range intents {
			if err := retire(ctx, q, i, at); err != nil {
				return empty, err
			}
		}
		entries, err := q.B08LegacyEntries(ctx, row.ID)
		if err != nil {
			return empty, err
		}
		for _, e := range entries {
			if !e.RevokedAt.Valid {
				if _, err := q.B08RemoveEntry(ctx, dbgen.B08RemoveEntryParams{ID: e.ID, At: nullable(at)}); err != nil {
					return empty, err
				}
				if err := q.B08RetireLegacyIdentities(ctx, dbgen.B08RetireLegacyIdentitiesParams{At: nullable(at), EntryID: e.ID}); err != nil {
					return empty, err
				}
			}
		}
		return getView(ctx, q, row.ID, now)
	})
}

func idRequest(op string, m domain.Mutation, id domain.ID) (request, error) {
	if err := checkMutation(m, false); err != nil {
		return request{}, err
	}
	if !id.Valid() {
		return request{}, invalid("/id")
	}
	return makeRequest(op, m, id)
}
