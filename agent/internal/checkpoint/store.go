package checkpoint

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"golang.org/x/sys/unix"
)

const (
	stateFile  = "checkpoint.aead"
	anchorFile = "initialized.aead"
	magic      = "meshstrata-agent-aead-v1\x00"
	// Bounded reads fail closed; deployment must size and monitor the store
	// for the permanent identity and execution ledger.
	maxStateBytes = 256 << 20
)

var (
	keyPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	ErrKey      = errors.New("CHECKPOINT_KEY_REQUIRED")
	ErrCorrupt  = errors.New("CHECKPOINT_INVALID_OR_WRONG_KEY")
	ErrConflict = errors.New("CHECKPOINT_REVISION_CONFLICT")
	ErrState    = errors.New("CHECKPOINT_INVALID_STATE")
	ErrClosed   = errors.New("CHECKPOINT_UNAVAILABLE")
)

type Options struct {
	Dir             string
	NodeID          domain.ID
	KeyID           string
	Key             []byte
	Now             func() time.Time
	RequireExisting bool
}

type diskState struct {
	State control.DurableState
	// Retain the last restriction result per identity after a receipt, including
	// pending cleanup. LastResult in Executions remains the command result.
	Restrictions map[domain.ID]control.Result
}

type Store struct {
	watermark domain.Revision
	mu        sync.Mutex
	dir       *PrivateDir
	lock      *os.File
	aead      cipher.AEAD
	node      domain.ID
	keyID     string
	unusable  bool
}

var _ control.LocalState = (*Store)(nil)

func Open(o Options) (*Store, error) {
	if len(o.Key) != 32 || !keyPattern.MatchString(o.KeyID) {
		return nil, ErrKey
	}
	if !o.NodeID.Valid() {
		return nil, ErrState
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	dir, err := OpenPrivateDir(o.Dir)
	if err != nil {
		return nil, err
	}
	lock, err := dir.OpenFile("checkpoint.lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		dir.Close()
		return nil, err
	}
	block, _ := aes.NewCipher(o.Key)
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		lock.Close()
		dir.Close()
		return nil, err
	}
	s := &Store{dir: dir, lock: lock, aead: aead, node: o.NodeID, keyID: o.KeyID}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		s.Close()
		return nil, ErrConflict
	}
	if err := s.initialize(o.Now().UTC(), o.RequireExisting); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize(now time.Time, requireExisting bool) error {
	marker, markerErr := s.read(anchorFile, 64)
	if markerErr == nil {
		const prefix = "initialized-v1:"
		if !strings.HasPrefix(string(marker), prefix) {
			return ErrCorrupt
		}
		value, err := strconv.ParseInt(string(marker[len(prefix):]), 10, 64)
		s.watermark = domain.Revision(value)
		if err != nil || (value != 0 && !s.watermark.Valid()) {
			return ErrCorrupt
		}
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	disk, err := s.readState()
	if !requireExisting && errors.Is(err, os.ErrNotExist) && errors.Is(markerErr, os.ErrNotExist) {
		names, err := s.dir.file.Readdirnames(-1)
		if err != nil {
			return ErrCorrupt
		}
		for _, name := range names {
			if name != "checkpoint.lock" && !strings.HasPrefix(name, ".write-") {
				return ErrCorrupt
			}
		}
		disk = diskState{State: control.DurableState{
			Checkpoint: control.Checkpoint{NodeID: s.node}, RevocationEpoch: 1, ClockFloor: now,
		}, Restrictions: make(map[domain.ID]control.Result)}
		if validState(disk.State, s.node) != nil {
			return ErrState
		}
		return s.persist(disk)
	}
	if err != nil || (errors.Is(markerErr, os.ErrNotExist) && disk.State.Revision != 0) {
		return ErrCorrupt
	}
	if markerErr != nil || disk.State.Revision > s.watermark {
		// Only the state rename may lead the watermark, e.g. if the
		// process exited between the two directory syncs.
		return s.mark(disk.State.Revision)
	}
	return nil
}

func (s *Store) mark(revision domain.Revision) error {
	if err := s.write(anchorFile, []byte("initialized-v1:"+strconv.FormatInt(int64(revision), 10))); err != nil {
		s.unusable = true
		return err
	}
	s.watermark = revision
	return nil
}

func (s *Store) aad(name string) []byte {
	return []byte(magic + string(s.node) + "\x00" + s.keyID + "\x00" + name)
}

func (s *Store) read(name string, limit int64) ([]byte, error) {
	b, err := s.dir.Read(name, limit+int64(len(magic)+s.aead.Overhead()))
	if err != nil {
		return nil, err
	}
	if len(b) < len(magic) || string(b[:len(magic)]) != magic {
		return nil, ErrCorrupt
	}
	out, err := s.aead.Open(nil, nil, b[len(magic):], s.aad(name))
	if err != nil {
		return nil, ErrCorrupt
	}
	return out, nil
}

func (s *Store) write(name string, data []byte) error {
	sealed := s.aead.Seal([]byte(magic), nil, data, s.aad(name))
	renamed, err := s.dir.Replace(name, sealed)
	if renamed && err != nil {
		// The caller cannot know whether this revision will survive a power
		// failure. Force a reopen and recovery instead of retrying stale CAS.
		s.unusable = true
	}
	return err
}

func (s *Store) readState() (diskState, error) {
	var d diskState
	b, err := s.read(stateFile, maxStateBytes)
	if err != nil {
		return d, err
	}
	if json.Unmarshal(b, &d) != nil || d.Restrictions == nil || d.State.Revision < s.watermark || validState(d.State, s.node) != nil {
		return diskState{}, ErrCorrupt
	}
	return d, nil
}

func (s *Store) persist(d diskState) error {
	b, err := json.Marshal(d)
	if err != nil || len(b) > maxStateBytes {
		return ErrState
	}
	if err := s.write(stateFile, b); err != nil {
		return err
	}
	return s.mark(d.State.Revision)
}

func (s *Store) available(ctx context.Context) error {
	if s.dir == nil || s.unusable {
		return ErrClosed
	}
	return ctx.Err()
}

func (s *Store) Load(ctx context.Context) (control.DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(ctx); err != nil {
		return control.DurableState{}, err
	}
	d, err := s.readState()
	return d.State, err
}

func (s *Store) Commit(ctx context.Context, expected domain.Revision, next control.DurableState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(ctx); err != nil {
		return err
	}
	d, err := s.readState()
	if err != nil {
		return err
	}
	if expected != d.State.Revision {
		return ErrConflict
	}
	if err := mergeState(d.State, &next); err != nil {
		return err
	}
	next.Revision = expected + 1
	if err := validState(next, s.node); err != nil {
		return err
	}
	for _, result := range next.Outbox {
		for _, evidence := range result.Restrictions {
			if result.Sequence > d.Restrictions[evidence.IdentityID].Sequence {
				d.Restrictions[evidence.IdentityID] = result
			}
		}
	}
	d.State = next
	return s.persist(d)
}

func (s *Store) Acknowledge(ctx context.Context, receipt control.Receipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(ctx); err != nil {
		return err
	}
	if validateMessage(s.node, "receipt", nil, &receipt) != nil {
		return ErrState
	}
	d, err := s.readState()
	if err != nil {
		return err
	}
	for i, result := range d.State.Outbox {
		if result.TaskID == receipt.TaskID && result.NodeID == receipt.NodeID && result.Sequence == receipt.Sequence {
			d.State.Revision++
			if !d.State.Revision.Valid() {
				return ErrState
			}
			d.State.Outbox = append(d.State.Outbox[:i], d.State.Outbox[i+1:]...)
			return s.persist(d)
		}
	}
	return nil
}

// RestrictionResults preserves evidence progress across receipts and restarts.
func (s *Store) RestrictionResults(ctx context.Context) (map[domain.ID]control.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(ctx); err != nil {
		return nil, err
	}
	d, err := s.readState()
	return d.Restrictions, err
}

func artifactName(key control.ArtifactKey) (string, error) {
	digest, err := hex.DecodeString(key.TargetDigest)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != key.TargetDigest {
		return "", control.ErrBinding
	}
	return "bundle-" + key.TargetDigest + ".aead", nil
}

// SaveArtifact stores the immutable, authenticated source of a checkpoint
// snapshot. The caller commits a pointer only after this file is durable.
func (s *Store) SaveArtifact(ctx context.Context, key control.ArtifactKey, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(ctx); err != nil {
		return err
	}
	if _, err := control.DecodeBundle(b, key, s.node); err != nil {
		return err
	}
	name, _ := artifactName(key)
	old, err := s.read(name, control.MaxBundleBytes)
	if err == nil {
		if control.DigestBytes(old) != key.TargetDigest {
			return ErrCorrupt
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.write(name, b)
}

func (s *Store) ReadArtifact(ctx context.Context, key control.ArtifactKey) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(ctx); err != nil {
		return nil, err
	}
	name, err := artifactName(key)
	if err != nil {
		return nil, err
	}
	b, err := s.read(name, control.MaxBundleBytes)
	if err != nil {
		return nil, err
	}
	if _, err := control.DecodeBundle(b, key, s.node); err != nil {
		return nil, ErrCorrupt
	}
	return b, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == nil {
		return nil
	}
	err := errors.Join(s.lock.Close(), s.dir.Close())
	s.dir = nil
	return err
}
