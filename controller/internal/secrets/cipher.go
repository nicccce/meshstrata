// Package secrets implements Controller-local credential encryption and storage.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/nicccce/meshstrata/internal/secretstore"
)

// Keys are copied at construction. KeyFiles loads exact 32-byte raw AES keys from
// preprovisioned private local files. A version may use one source only. No path
// is created and no missing key is repaired, including during decryption.
type Options struct {
	CurrentKeyID string
	Keys         map[string][]byte
	KeyFiles     map[string]string
	Random       io.Reader
}
type localCipher struct {
	current string
	keys    map[string]cipher.AEAD
	random  io.Reader
	mu      sync.Mutex
}

const maxPlaintext = 1024 * 1024
const envelope = "MS\x01\x01" // magic, format v1, AES-256-GCM
func keyIDValid(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
func validBinding(b secretstore.Binding) bool {
	if !b.Ref.Valid() || len(b.Purpose) < 1 || len(b.Purpose) > 64 {
		return false
	}
	// Syntax is extensible for future owner migrations. Repository persists only
	// the purposes allowed by the installed database schema (002 today).
	for _, c := range b.Purpose {
		if !(c >= 'a' && c <= 'z' || c == '_') {
			return false
		}
	}
	return true
}
func New(options Options) (secretstore.Cipher, error) {
	if !keyIDValid(options.CurrentKeyID) {
		return nil, secretstore.ErrKeyUnavailable
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if nilDependency(options.Random) {
		return nil, secretstore.ErrKeyUnavailable
	}
	c := &localCipher{current: options.CurrentKeyID, keys: map[string]cipher.AEAD{}, random: options.Random}
	material := make(map[string][]byte)
	defer func() {
		for _, key := range material {
			clear(key)
		}
	}()
	for id, key := range options.Keys {
		material[id] = append([]byte(nil), key...)
	}
	for id, path := range options.KeyFiles {
		if _, exists := material[id]; exists {
			return nil, secretstore.ErrKeyUnavailable
		}
		key, err := readKeyFile(path)
		if err != nil {
			return nil, secretstore.ErrKeyUnavailable
		}
		material[id] = key
	}
	for id, key := range material {
		if !keyIDValid(id) || len(key) != 32 {
			return nil, secretstore.ErrKeyUnavailable
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, secretstore.ErrKeyUnavailable
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, secretstore.ErrKeyUnavailable
		}
		c.keys[id] = aead
	}
	if c.keys[c.current] == nil {
		return nil, secretstore.ErrKeyUnavailable
	}
	return c, nil
}
func aad(b secretstore.Binding, keyID string) []byte {
	dst := []byte(envelope)
	for _, part := range []string{string(b.Ref), string(b.Purpose), keyID} {
		dst = binary.BigEndian.AppendUint16(dst, uint16(len(part)))
		dst = append(dst, part...)
	}
	return dst
}
func (c *localCipher) Seal(ctx context.Context, b secretstore.Binding, plaintext []byte) (secretstore.Sealed, error) {
	if !validBinding(b) {
		return secretstore.Sealed{}, secretstore.ErrInvalidBinding
	}
	if err := ctx.Err(); err != nil {
		return secretstore.Sealed{}, err
	}
	aead := c.keys[c.current]
	if aead == nil {
		return secretstore.Sealed{}, secretstore.ErrKeyUnavailable
	}
	if len(plaintext) > maxPlaintext {
		return secretstore.Sealed{}, errors.New("secret too large")
	}
	nonce := make([]byte, aead.NonceSize())
	c.mu.Lock()
	_, err := io.ReadFull(c.random, nonce)
	c.mu.Unlock()
	if err != nil {
		return secretstore.Sealed{}, secretstore.ErrKeyUnavailable
	}
	dst := append([]byte(envelope), nonce...)
	dst = aead.Seal(dst, nonce, plaintext, aad(b, c.current))
	return secretstore.Sealed{KeyID: c.current, Ciphertext: dst}, nil
}
func (c *localCipher) Open(ctx context.Context, b secretstore.Binding, sealed secretstore.Sealed) ([]byte, error) {
	if !validBinding(b) {
		return nil, secretstore.ErrInvalidBinding
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	aead := c.keys[sealed.KeyID]
	if aead == nil || len(sealed.Ciphertext) < len(envelope)+12+16 || len(sealed.Ciphertext) > len(envelope)+12+16+maxPlaintext || !strings.HasPrefix(string(sealed.Ciphertext[:min(len(envelope), len(sealed.Ciphertext))]), envelope) {
		return nil, secretstore.ErrDecrypt
	}
	body := sealed.Ciphertext[len(envelope):]
	nonce := body[:aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, body[aead.NonceSize():], aad(b, sealed.KeyID))
	if err != nil {
		clear(plaintext)
		return nil, secretstore.ErrDecrypt
	}
	return plaintext, nil
}
