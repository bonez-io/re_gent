package selfhosted

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These follow the OWASP baseline recommendation for an
// interactive login (19 MiB memory, 2 iterations would be the absolute floor;
// this composition spends a bit more since a self-hosted instance signs in
// infrequently compared to a multi-tenant service). Encoding the parameters in
// the stored hash means they can be tuned later without invalidating
// passwords hashed under the previous settings.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// hashPassword returns a self-describing Argon2id hash string in the same
// "$argon2id$v=..$m=..,t=..,p=..$salt$hash" shape libsodium/argon2-cffi tools
// use, so the parameters travel with the hash.
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum))
	return encoded, nil
}

// verifyPassword reports whether password matches the Argon2id hash produced
// by hashPassword, in constant time.
func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unrecognized password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("parse hash version: %w", err)
	}
	var mem uint32
	var iterations uint32
	var threads uint8
	for _, field := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return false, errors.New("malformed password hash parameters")
		}
		val, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return false, fmt.Errorf("parse hash parameter %q: %w", kv[0], err)
		}
		switch kv[0] {
		case "m":
			mem = uint32(val)
		case "t":
			iterations = uint32(val)
		case "p":
			threads = uint8(val)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, iterations, mem, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, nil
	}
	return true, nil
}

// setupCodeAlphabet excludes visually ambiguous characters (0/O, 1/I/L) so a
// code read off a screen and typed by hand is unlikely to be mistyped.
const setupCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// newSetupCode returns a code shaped like "7KQ2-M9XA": two groups of four
// characters from setupCodeAlphabet, separated by a hyphen.
func newSetupCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate setup code: %w", err)
	}
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, setupCodeAlphabet[int(v)%len(setupCodeAlphabet)])
	}
	return string(out), nil
}

// newRandomPassword returns a random password drawn from a large alphabet,
// suitable as the initial admin password RFC 0005 step 0 prints once.
func newRandomPassword(length int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, length)
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	for i, v := range raw {
		b[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(b), nil
}

// secretBox encrypts small at-rest secrets (OAuth client secrets, SMTP
// passwords) with AES-256-GCM under a key that lives in a file on the data
// volume, separate from identity.db. Losing the key file makes stored
// secrets unrecoverable but never exposes them from a copy of the database
// alone; regenerating auth-method settings after a key loss is expected to be
// rare enough that this is the right trade-off for a single-node beta.
type secretBox struct {
	aead cipher.AEAD
}

// loadOrCreateKey reads a base64-encoded random key of the given length from
// path, generating and persisting one (mode 0600) if it does not exist yet.
// Used for both the auth-method secretBox key and the OAuth state-signing
// key, so a losing either file only ever costs re-doing that one piece of
// configuration (re-entering a client secret, or re-starting an in-flight
// sign-in), never any user's password or PAT.
func loadOrCreateKey(path string, length int) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read key file %s: %w", path, err)
		}
		key := make([]byte, length)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
		if err := writeSecretFile(path, base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, fmt.Errorf("write key file %s: %w", path, err)
		}
		return key, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != length {
		return nil, fmt.Errorf("key file %s is corrupt", path)
	}
	return decoded, nil
}

func openSecretBox(dataDir string) (*secretBox, error) {
	key, err := loadOrCreateKey(filepath.Join(dataDir, "secrets.key"), 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build AEAD: %w", err)
	}
	return &secretBox{aead: aead}, nil
}

func (b *secretBox) seal(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (b *secretBox) open(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	n := b.aead.NonceSize()
	if len(ciphertext) < n {
		return "", errors.New("stored secret is truncated")
	}
	nonce, box := ciphertext[:n], ciphertext[n:]
	plaintext, err := b.aead.Open(nil, nonce, box, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt stored secret: %w", err)
	}
	return string(plaintext), nil
}
