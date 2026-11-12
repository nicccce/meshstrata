package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// RFC 9106 second recommendation: 64 MiB, three passes, four lanes, 16-byte
// salt and 32-byte tag. Service serializes KDF work to bound peak KDF memory.
const passwordPrefix = "$argon2id$v=19$m=65536,t=3,p=4$"

func hashPassword(random io.Reader, password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", err
	}
	tag := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	defer clear(tag)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(tag), nil
}
func checkPassword(encoded, password string) bool {
	// Accept exactly the implemented parameter version, preventing database
	// corruption from requesting unbounded work. Future upgrades need a parser version.
	if !strings.HasPrefix(encoded, passwordPrefix) {
		return false
	}
	fields := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
	if len(fields) != 2 {
		return false
	}
	salt, e := base64.RawStdEncoding.Strict().DecodeString(fields[0])
	if e != nil || len(salt) != 16 {
		return false
	}
	expected, e := base64.RawStdEncoding.Strict().DecodeString(fields[1])
	if e != nil || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	defer clear(actual)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
