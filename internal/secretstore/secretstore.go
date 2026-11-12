// Package secretstore is the process-local encryption boundary implemented by B01.
// It contains no key loading, cryptography, database or transport implementation.
package secretstore

import (
	"context"
	"errors"

	"github.com/nicccce/meshstrata/internal/domain"
)

type Purpose string

// These values match migration 002 credentials.purpose. Extending this set needs
// an incremental migration; subscription_download is reserved for B08/010.
const (
	ProxyEntry    Purpose = "proxy_entry"
	InternalProxy Purpose = "internal_proxy"
	DNSAPI        Purpose = "dns_api"
	LocationAPI   Purpose = "location_api"
	ManagementCA  Purpose = "management_ca"
	Tunnel        Purpose = "tunnel"
	Certificate   Purpose = "certificate"
)

var (
	ErrInvalidBinding = errors.New("invalid secret binding")
	ErrDecrypt        = errors.New("secret unavailable")
	ErrKeyUnavailable = errors.New("encryption key unavailable")
)

// Binding must be authenticated along with the ciphertext, preventing both
// cross-purpose and cross-record substitution. Ref maps to credentials.id.
type Binding struct {
	Ref     domain.ID
	Purpose Purpose
}

// Sealed maps to credentials.key_id/ciphertext. KeyID is the opaque key version,
// never the key bytes. Ciphertext includes the versioned encryption envelope.
// Only repositories/backups may persist it; it is not a browser DTO or node payload.
type Sealed struct {
	KeyID      string
	Ciphertext []byte
}

// Cipher is injected into Controller consumers (including B02), never Agent.
// Seal uses the current key; Open selects the recorded KeyID. B01 rotation opens
// and re-seals with the SAME binding, then repository CAS replaces the envelope.
// No implicit key creation on Open failure. Wrong binding, corrupt envelope or
// unavailable old key return nil plaintext and ErrDecrypt (errors.Is); Seal with
// no current key returns ErrKeyUnavailable. Errors contain no secret or binding.
// This interface never transports plaintext. Master keys stay local; plaintext
// only leaves through a purpose owner's explicitly authorized private delivery
// or export (e.g. B08's download-link action), never ordinary logs/DTOs. Caller
// owns returned bytes and clears them after use. CA private keys never leave Controller.
// Node private keys and local Mihomo secrets must never be passed here.
type Cipher interface {
	Seal(context.Context, Binding, []byte) (Sealed, error)
	Open(context.Context, Binding, Sealed) ([]byte, error)
}
