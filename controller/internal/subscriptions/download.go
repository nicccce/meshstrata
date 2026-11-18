package subscriptions

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

const configPath = "/subscriptions/v1/config/"
const encodedTokenLen = 43

type tokenSeal struct {
	row    dbgen.B08InsertTokenParams
	digest [sha256.Size]byte
}

func (s *Service) randomBytes(dst []byte) error {
	s.randomMu.Lock()
	_, err := io.ReadFull(s.o.Random, dst)
	s.randomMu.Unlock()
	return err
}

// B01 authenticates (purpose,Ref). Derive that Ref from all three immutable
// components so even copying ciphertext and swapping storage metadata fails.
func tokenBinding(sub, ref string, version int64) secretstore.Binding {
	h := sha256.Sum256([]byte(sub + "\x00" + ref + "\x00" + strconv.FormatInt(version, 10)))
	return secretstore.Binding{Purpose: secretstore.SubscriptionDownload, Ref: domain.ID("sub_token_" + hex.EncodeToString(h[:16]))}
}

func (s *Service) sealToken(ctx context.Context, sub domain.ID, v int64) (t tokenSeal, err error) {
	id, err := s.id("download")
	if err != nil {
		return t, err
	}
	var raw [stage3.DownloadTokenBytes]byte
	defer clear(raw[:])
	if err := s.randomBytes(raw[:]); err != nil {
		return t, err
	}
	t.digest = sha256.Sum256(raw[:])
	sealed, err := s.o.Cipher.Seal(ctx, tokenBinding(string(sub), string(id), v), raw[:])
	if err != nil {
		return t, safe(ctx, err)
	}
	t.row = dbgen.B08InsertTokenParams{ID: string(id), SubscriptionID: string(sub), TokenVersion: v,
		KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext}
	return t, nil
}

func retireToken(ctx context.Context, q *dbgen.Queries, old dbgen.Subscription, at string) error {
	if !old.DownloadTokenCredentialRef.Valid {
		return nil // legacy hash-only records have no recoverable envelope
	}
	return affected(q.B08RevokeToken(ctx, dbgen.B08RevokeTokenParams{ID: old.DownloadTokenCredentialRef.String,
		SubscriptionID: old.ID, Version: old.TokenVersion, At: nullable(at)}))
}

func (s *Service) ResetDownloadToken(ctx context.Context, m domain.Mutation, id domain.ID) (stage3.SubscriptionView, error) {
	var empty stage3.SubscriptionView
	req, err := idRequest("reset_token", m, id)
	if err != nil {
		return empty, err
	}
	if out, ok, err := s.replay(ctx, req); ok || err != nil {
		return out, err
	}
	var old dbgen.Subscription
	err = s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		old, err = q.B08Subscription(ctx, string(id))
		if err != nil {
			return err
		}
		return requireCAS(old, m)
	})
	if err != nil {
		if v, ok, replayErr := s.replay(ctx, req); ok || replayErr != nil {
			return v, replayErr
		}
		return empty, localError(ctx, err)
	}
	if !domain.Revision(old.TokenVersion).Valid() || old.TokenVersion >= int64(domain.MaxRevision) {
		return empty, problem(domain.DependencyInUse)
	}
	t, err := s.sealToken(ctx, id, old.TokenVersion+1)
	if err != nil {
		return empty, localError(ctx, err)
	}
	return s.mutate(ctx, req, func(q *dbgen.Queries, now time.Time) (stage3.SubscriptionView, error) {
		current, err := q.B08Subscription(ctx, old.ID)
		if err != nil {
			return empty, err
		}
		if err := requireCAS(current, m); err != nil {
			return empty, err
		}
		if current.TokenVersion != old.TokenVersion || current.DownloadTokenCredentialRef != old.DownloadTokenCredentialRef {
			return empty, conflict(current.Revision)
		}
		if err := q.B08InsertToken(ctx, t.row); err != nil {
			return empty, err
		}
		at := stamp(now)
		if err := affected(q.B08RotateToken(ctx, dbgen.B08RotateTokenParams{ID: current.ID, Expected: current.Revision,
			ExpectedToken: current.TokenVersion, Hash: t.digest[:], Ref: nullable(t.row.ID), UpdatedAt: at})); err != nil {
			return empty, err
		}
		if err := retireToken(ctx, q, current, at); err != nil {
			return empty, err
		}
		return getView(ctx, q, current.ID, now)
	})
}

func sameHash(a, b []byte) bool {
	return len(a) == sha256.Size && subtle.ConstantTimeCompare(a, b) == 1
}

func downloadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return problem(domain.NotFound)
}

// DownloadLink is a privileged in-process operation. Only the Writer guarded
// POST in HTTPHandler may expose its result outside the controller.
func (s *Service) DownloadLink(ctx context.Context, id domain.ID) (stage3.SubscriptionDownloadLink, error) {
	var empty stage3.SubscriptionDownloadLink
	if !id.Valid() {
		return empty, downloadError(ctx)
	}
	find := func() (row dbgen.B08TokenBySubscriptionRow, err error) {
		if err = s.tick(ctx); err != nil {
			return row, err
		}
		err = s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
			now, err := dbNow(ctx, q)
			if err != nil {
				return err
			}
			sub, err := q.B08Subscription(ctx, string(id))
			if err != nil || !eligible(sub, now) {
				return downloadError(ctx)
			}
			row, err = q.B08TokenBySubscription(ctx, sub.ID)
			return err
		})
		return row, err
	}
	row, err := find()
	if err != nil {
		return empty, downloadError(ctx)
	}
	raw, err := s.o.Cipher.Open(ctx, tokenBinding(row.SubscriptionID, row.ID, row.TokenVersion),
		secretstore.Sealed{KeyID: row.KeyID, Ciphertext: row.Ciphertext})
	defer clear(raw)
	if err != nil || len(raw) != stage3.DownloadTokenBytes {
		return empty, downloadError(ctx)
	}
	hash := sha256.Sum256(raw)
	current, err := find()
	if err != nil || current.ID != row.ID || current.TokenVersion != row.TokenVersion ||
		!sameHash(row.DownloadTokenHash, hash[:]) || !sameHash(current.DownloadTokenHash, hash[:]) {
		return empty, downloadError(ctx)
	}
	if ctx.Err() != nil {
		return empty, downloadError(ctx)
	}
	return stage3.SubscriptionDownloadLink{DownloadURL: s.o.PublicOrigin + configPath + base64.RawURLEncoding.EncodeToString(raw)}, nil
}

func tokenHash(token string) (h [sha256.Size]byte, ok bool) {
	if len(token) != encodedTokenLen {
		return h, false
	}
	var raw [stage3.DownloadTokenBytes]byte
	defer clear(raw[:])
	n, err := base64.RawURLEncoding.Strict().Decode(raw[:], []byte(token))
	if err != nil || n != len(raw) {
		return h, false
	}
	return sha256.Sum256(raw[:]), true
}

type entryWithCipher struct {
	row    dbgen.B08Intent
	sealed secretstore.Sealed
}

type downloadSnapshot struct {
	sub      dbgen.Subscription
	revision dbgen.B08Revision
	entries  []entryWithCipher
}

func (s *Service) loadDownload(ctx context.Context, hash [sha256.Size]byte, loadCipher bool) (snap downloadSnapshot, err error) {
	if err = s.tick(ctx); err != nil {
		return snap, err
	}
	err = s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
		now, err := dbNow(ctx, q)
		if err != nil {
			return err
		}
		sub, err := q.B08SubscriptionByHash(ctx, hash[:])
		if err != nil || !eligible(sub, now) || !sameHash(sub.DownloadTokenHash, hash[:]) {
			return downloadError(ctx)
		}
		head, err := q.B08Head(ctx, sub.ID)
		if err != nil || !head.ConfirmedRevision.Valid {
			return downloadError(ctx)
		}
		rev, err := q.B08Revision(ctx, dbgen.B08RevisionParams{SubscriptionID: sub.ID, Revision: head.ConfirmedRevision.Int64})
		if err != nil {
			return err
		}
		ownExpiry, err := parse(rev.ExpiresAt)
		if err != nil || !ownExpiry.After(now) {
			return downloadError(ctx)
		}
		entries, err := q.B08RevisionEntries(ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: sub.ID, Revision: rev.Revision})
		if err != nil {
			return err
		}
		if len(entries) == 0 || len(entries) > stage3.MaxSubscriptionEntries {
			return downloadError(ctx)
		}
		snap.sub, snap.revision = sub, rev
		for _, i := range entries {
			if i.State != "confirmed" || !i.TaskID.Valid || i.RevokedAt.Valid || i.CleanupState != "none" ||
				!i.IdentityID.Valid || !i.EntryID.Valid || i.ExpiresAt <= stamp(now) {
				return downloadError(ctx)
			}
			l, err := q.B08LegacyEntryByRoute(ctx, dbgen.B08LegacyEntryByRouteParams{SubscriptionID: sub.ID, RouteID: i.RouteID})
			if err != nil || l.RevokedAt.Valid || l.ID != i.EntryKey || l.ExpiresAt != i.ExpiresAt || l.ListenerID != i.ListenerID.String {
				return downloadError(ctx)
			}
			identity, err := q.B08LatestIdentity(ctx, l.ID)
			if err != nil || identity.RevokedAt.Valid || identity.ID != i.IdentityKey || identity.EntryID != l.ID ||
				identity.Generation != i.Generation || identity.ExpiresAt != i.ExpiresAt || identity.CredentialRef != i.CredentialRef {
				return downloadError(ctx)
			}
			row, err := q.B01GetCredential(ctx, dbgen.B01GetCredentialParams{ID: i.CredentialRef, Purpose: string(secretstore.ProxyEntry)})
			if err != nil {
				return err
			}
			e := entryWithCipher{row: i}
			if loadCipher {
				e.sealed = secretstore.Sealed{KeyID: row.KeyID, Ciphertext: row.Ciphertext}
			}
			snap.entries = append(snap.entries, e)
		}
		return nil
	})
	return snap, err
}

func sameSnapshot(a, b downloadSnapshot) bool {
	if a.sub.ID != b.sub.ID || a.sub.Revision != b.sub.Revision || a.sub.TokenVersion != b.sub.TokenVersion ||
		a.sub.DownloadTokenCredentialRef != b.sub.DownloadTokenCredentialRef || a.sub.ExpiresAt != b.sub.ExpiresAt ||
		a.revision != b.revision || len(a.entries) != len(b.entries) {
		return false
	}
	for k, i := range a.entries {
		if i.row != b.entries[k].row {
			return false
		}
	}
	return true
}

func (s *Service) generation(ctx context.Context, snap downloadSnapshot) (input stage3.GenerationInput, err error) {
	if err := json.Unmarshal([]byte(snap.revision.PreferencesJson), &input.Snapshot.Preferences); err != nil {
		return input, err
	}
	p, err := s.profile(ctx, profilePin(snap.revision))
	if err != nil {
		return input, err
	}
	input.Snapshot.Profile = p
	input.Snapshot.SubscriptionID = domain.ID(snap.sub.ID)
	input.Snapshot.Revision = domain.Revision(snap.revision.Revision)
	for _, e := range snap.entries {
		view, err := entryView(e.row, time.Time{})
		if err != nil {
			return input, err
		}
		input.Snapshot.Entries = append(input.Snapshot.Entries, view)
		ref := domain.ID(e.row.CredentialRef)
		secret, err := s.openProxy(ctx, ref, e.sealed)
		if err != nil {
			return input, err
		}
		input.Secrets = append(input.Secrets, stage3.EntrySecret{IdentityID: view.IdentityID, Credential: control.PrivateCredential{
			Ref: ref, Audience: input.Snapshot.SubscriptionID, Password: secret.Password, UUID: secret.UUID,
		}})
	}
	// Also rebind the ordered config/entry payload to the reviewed subscription Pin.
	r := review{Name: snap.revision.Name, Profile: p.Pin, Preferences: input.Snapshot.Preferences}
	r.ExpiresAt, err = parse(snap.revision.ExpiresAt)
	if err != nil {
		return input, err
	}
	for _, e := range input.Snapshot.Entries {
		r.Entries = append(r.Entries, desiredEntry{e.ID, e.Route.Pin, e.Generation})
	}
	pin, err := stage3.NewPin(input.Snapshot.SubscriptionID, input.Snapshot.Revision, r)
	if err != nil || pin != subjectPin(snap.revision) {
		return input, problem(unavailable)
	}
	return input, stage3.ValidateGenerationBinding(input)
}

func (s *Service) download(ctx context.Context, token string, provider bool) ([]byte, error) {
	h, ok := tokenHash(token)
	if !ok {
		return nil, downloadError(ctx)
	}
	snap, err := s.loadDownload(ctx, h, true)
	if err != nil {
		return nil, downloadError(ctx)
	}
	input, err := s.generation(ctx, snap)
	// Strings are a frozen Go contract; clear the owning slice and clear each
	// originating plaintext byte buffer immediately in openProxy.
	defer clear(input.Secrets)
	if err != nil || ctx.Err() != nil {
		return nil, downloadError(ctx)
	}
	var payload []byte
	if provider {
		payload, err = s.o.Client.BuildProvider(input)
	} else {
		payload, err = s.o.Client.BuildFull(input)
	}
	if err != nil || len(payload) == 0 || len(payload) > stage3.MaxConfigBytes {
		clear(payload)
		return nil, downloadError(ctx)
	}
	current, err := s.loadDownload(ctx, h, false)
	if err != nil || !sameSnapshot(snap, current) || ctx.Err() != nil {
		clear(payload)
		return nil, downloadError(ctx)
	}
	return payload, nil
}

func (s *Service) DownloadFull(ctx context.Context, token string) (stage3.FullConfig, error) {
	return s.download(ctx, token, false)
}

func (s *Service) DownloadProvider(ctx context.Context, token string) (stage3.ProxyProvider, error) {
	return s.download(ctx, token, true)
}

func (s *Service) sealProxy(ctx context.Context, ref domain.ID, endpoint control.ProxyEndpoint) (secretstore.Sealed, error) {
	var key [32]byte
	defer clear(key[:])
	n := len(key)
	if endpoint.Protocol == domain.VLESS || endpoint.Cipher == "2022-blake3-aes-128-gcm" {
		n = 16
	}
	if err := s.randomBytes(key[:n]); err != nil {
		return secretstore.Sealed{}, err
	}
	secret := stage3.ProxySecret{}
	switch endpoint.Protocol {
	case domain.VLESS:
		key[6] = key[6]&0x0f | 0x40
		key[8] = key[8]&0x3f | 0x80
		b := make([]byte, 36)
		hex.Encode(b[0:8], key[:4])
		hex.Encode(b[9:13], key[4:6])
		hex.Encode(b[14:18], key[6:8])
		hex.Encode(b[19:23], key[8:10])
		hex.Encode(b[24:36], key[10:16])
		b[8], b[13], b[18], b[23] = '-', '-', '-', '-'
		secret.UUID = string(b)
		clear(b)
	case domain.SS2022:
		secret.Password = base64.StdEncoding.EncodeToString(key[:n])
	case domain.Trojan, domain.Hysteria2:
		secret.Password = base64.RawURLEncoding.EncodeToString(key[:n])
	default:
		return secretstore.Sealed{}, problem(unavailable)
	}
	raw, err := json.Marshal(secret)
	defer clear(raw)
	if err != nil {
		return secretstore.Sealed{}, err
	}
	return s.o.Cipher.Seal(ctx, secretstore.Binding{Purpose: secretstore.ProxyEntry, Ref: ref}, raw)
}

func (s *Service) openProxy(ctx context.Context, ref domain.ID, sealed secretstore.Sealed) (v stage3.ProxySecret, err error) {
	raw, err := s.o.Cipher.Open(ctx, secretstore.Binding{Purpose: secretstore.ProxyEntry, Ref: ref}, sealed)
	defer clear(raw)
	if err != nil || len(raw) > 2048 || !strictJSON(raw, &v) {
		return stage3.ProxySecret{}, problem(unavailable)
	}
	return v, nil
}
