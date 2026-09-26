package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backend is the interface for secret storage backends.
// All implementations must be safe for concurrent use.
type Backend interface {
	// Get returns the secret value for the given name/path.
	Get(name string) (string, error)
	// List returns all secret names available under the backend's namespace.
	List() ([]string, error)
	// Set stores a secret value.
	Set(name string, value []byte) error
	// Delete removes a secret.
	Delete(name string) error
	// Rename renames a secret from oldName to newName.
	Rename(oldName, newName string) error
}

// LocalBackend implements Backend with AES-256-GCM encryption.
// The encryption key is derived from Passphrase + a random salt stored at
// KeyFile (0600). Secrets live under Dir/, one file per secret. Names are
// paths and may contain slashes (e.g. "myapp/db-password" → Dir/myapp/db-password).
//
// Key file format: 32 raw bytes of random salt (binary).
// AES key = PBKDF2-HMAC-SHA256(Passphrase, salt, 200000, 32).
// Secret file format: <12-byte random nonce><AES-256-GCM ciphertext+tag>
//
// Passphrase may be nil/empty — in that case derivation uses an empty key,
// and security depends solely on the salt file remaining unreadable.
// Set Passphrase from CHAMELEON_SECRET_PASSPHRASE for an additional layer.
type LocalBackend struct {
	Dir        string // e.g. ~/.chameleon/secrets
	KeyFile    string // e.g. ~/.chameleon/key
	Passphrase []byte // from CHAMELEON_SECRET_PASSPHRASE; nil = no passphrase
}

const saltSize = 32

// validateSecretName rejects empty names and any path component that is ".", "..", or empty,
// preventing path traversal outside Dir.
func validateSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("secret name must not be empty")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid secret name %q: path components must not be empty, '.', or '..'", name)
		}
	}
	return nil
}

// deriveKey computes PBKDF2-HMAC-SHA256(passphrase, salt, 200000 iterations, 32 bytes).
// Uses only stdlib crypto — no external dependencies.
func deriveKey(passphrase, salt []byte) []byte {
	const iterations = 200_000
	mac := hmac.New(sha256.New, passphrase)
	mac.Write(salt)
	mac.Write([]byte{0, 0, 0, 1}) // PBKDF2 block counter = 1
	t := mac.Sum(nil)             // T_1 = U_1, len=32
	u := append([]byte(nil), t...)
	for i := 1; i < iterations; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(u[:0]) // U_{i+1} = HMAC(passphrase, U_i)
		for j := range t {
			t[j] ^= u[j]
		}
	}
	return t // 32 bytes
}

// loadOrCreateKey reads the salt from KeyFile (creating it on first use) and
// returns the derived AES-256 key. Uses O_EXCL to avoid a race when two
// callers initialise the store concurrently for the first time.
func (b *LocalBackend) loadOrCreateKey() ([]byte, error) {
	if data, err := os.ReadFile(b.KeyFile); err == nil {
		if len(data) != saltSize {
			return nil, fmt.Errorf("corrupt key file %s: expected %d bytes, got %d", b.KeyFile, saltSize, len(data))
		}
		return deriveKey(b.Passphrase, data), nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read key %s: %w", b.KeyFile, err)
	}

	// Key does not exist — create it atomically.
	if err := os.MkdirAll(filepath.Dir(b.KeyFile), 0700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	f, err := os.OpenFile(b.KeyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		// Lost the race — another goroutine created the key; read theirs.
		data, err := os.ReadFile(b.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read key after race %s: %w", b.KeyFile, err)
		}
		if len(data) != saltSize {
			return nil, fmt.Errorf("corrupt key file %s: expected %d bytes, got %d", b.KeyFile, saltSize, len(data))
		}
		return deriveKey(b.Passphrase, data), nil
	}
	if err != nil {
		return nil, fmt.Errorf("create key %s: %w", b.KeyFile, err)
	}
	if _, err := f.Write(salt); err != nil {
		f.Close()
		os.Remove(b.KeyFile)
		return nil, fmt.Errorf("write key %s: %w", b.KeyFile, err)
	}
	f.Close()
	return deriveKey(b.Passphrase, salt), nil
}

func (b *LocalBackend) secretPath(name string) string {
	return filepath.Join(b.Dir, filepath.FromSlash(name))
}

// Get decrypts and returns the secret value for name.
func (b *LocalBackend) Get(name string) (string, error) {
	if err := validateSecretName(name); err != nil {
		return "", err
	}
	key, err := b.loadOrCreateKey()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(b.secretPath(name))
	if os.IsNotExist(err) {
		return "", fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return "", err
	}
	if len(data) < 12 {
		return "", fmt.Errorf("secret %q: corrupt (too short)", name)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plaintext, err := gcm.Open(nil, data[:12], data[12:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret %q: wrong passphrase or corrupt data", name)
	}
	return string(plaintext), nil
}

// Set encrypts value and writes it to the store under name.
func (b *LocalBackend) Set(name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	key, err := b.loadOrCreateKey()
	if err != nil {
		return err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, value, nil)

	path := b.secretPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	return os.WriteFile(path, ciphertext, 0600)
}

// Delete removes the secret file for name and prunes any empty parent directories.
func (b *LocalBackend) Delete(name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	path := b.secretPath(name)
	if err := os.Remove(path); os.IsNotExist(err) {
		return fmt.Errorf("secret %q not found", name)
	} else if err != nil {
		return err
	}
	for dir := filepath.Dir(path); dir != b.Dir; dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break
		}
	}
	return nil
}

// Rename moves a secret from oldName to newName without re-encrypting.
// Returns an error if oldName does not exist or newName already exists.
func (b *LocalBackend) Rename(oldName, newName string) error {
	if err := validateSecretName(oldName); err != nil {
		return err
	}
	if err := validateSecretName(newName); err != nil {
		return err
	}
	oldPath := b.secretPath(oldName)
	newPath := b.secretPath(newName)
	if _, err := os.Stat(oldPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("secret %q not found", oldName)
		}
		return fmt.Errorf("stat secret %q: %w", oldName, err)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("secret %q already exists", newName)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	for dir := filepath.Dir(oldPath); dir != b.Dir; dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break
		}
	}
	return nil
}

// List returns all secret names by walking Dir. Returns nil (not an error)
// if the secrets directory does not exist yet.
func (b *LocalBackend) List() ([]string, error) {
	var names []string
	err := filepath.Walk(b.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(b.Dir, path)
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return names, err
}
