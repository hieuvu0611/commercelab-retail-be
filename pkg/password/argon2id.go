// Package password hashes and verifies passwords with Argon2id.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters follow the OWASP minimum for Argon2id (19 MiB, 2 iterations, 1 lane). They are modest on purpose:
// every service runs in a 250 MB / 0.25 CPU container, and login floods are limited by the rate limiter (P1-T08).
const (
	memoryKiB   uint32 = 19 * 1024
	iterations  uint32 = 2
	parallelism uint8  = 1
	saltLength         = 16
	keyLength   uint32 = 32

	maxMemoryKiB  uint32 = 256 * 1024
	maxIterations uint32 = 10
)

// ErrInvalidHash means the stored value is not a valid Argon2id encoding.
var ErrInvalidHash = errors.New("invalid argon2id hash")

var b64 = base64.RawStdEncoding

// Hash returns the PHC-encoded Argon2id hash of plain with a fresh random salt.
func Hash(plain string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	return encode(plain, salt, memoryKiB, iterations, parallelism), nil
}

// HashWithSalt is for reproducible fixtures (seed migrations); production code must use Hash.
func HashWithSalt(plain string, salt []byte) string {
	return encode(plain, salt, memoryKiB, iterations, parallelism)
}

func encode(plain string, salt []byte, memory, iters uint32, lanes uint8) string {
	key := argon2.IDKey([]byte(plain), salt, iters, memory, lanes, keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iters, lanes, b64.EncodeToString(salt), b64.EncodeToString(key))
}

type decoded struct {
	memory, iterations uint32
	parallelism        uint8
	salt, key          []byte
}

func decode(hash string) (decoded, error) {
	parts := strings.Split(hash, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return decoded{}, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return decoded{}, ErrInvalidHash
	}
	var d decoded
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &d.memory, &d.iterations, &d.parallelism); err != nil {
		return decoded{}, ErrInvalidHash
	}
	// Bounds stop a tampered hash from forcing huge allocations or a division by zero.
	if d.memory == 0 || d.memory > maxMemoryKiB || d.iterations == 0 || d.iterations > maxIterations || d.parallelism == 0 {
		return decoded{}, ErrInvalidHash
	}
	var err error
	if d.salt, err = b64.DecodeString(parts[4]); err != nil || len(d.salt) == 0 {
		return decoded{}, ErrInvalidHash
	}
	if d.key, err = b64.DecodeString(parts[5]); err != nil || len(d.key) == 0 {
		return decoded{}, ErrInvalidHash
	}
	return d, nil
}

// Verify reports whether plain matches hash. A wrong password returns (false, nil); a malformed hash
// returns (false, ErrInvalidHash). The comparison is constant-time.
func Verify(plain, hash string) (bool, error) {
	d, err := decode(hash)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(plain), d.salt, d.iterations, d.memory, d.parallelism, uint32(len(d.key)))
	return subtle.ConstantTimeCompare(candidate, d.key) == 1, nil
}

// NeedsRehash reports whether hash is malformed or was made with weaker parameters than the current ones,
// so callers can upgrade it after a successful login.
func NeedsRehash(hash string) bool {
	d, err := decode(hash)
	if err != nil {
		return true
	}
	return d.memory < memoryKiB || d.iterations < iterations || d.parallelism != parallelism || uint32(len(d.key)) < keyLength
}
