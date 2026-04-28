// Package jwt manages the Engine API JWT secret shared between EL and CL.
//
// The Ethereum Engine API (EIP-3675) requires both the execution and consensus
// layer to authenticate with a shared 32-byte secret stored as a hex file.
//
// Format (eth-docker compatible): "0x" + 64 lowercase hex characters
// Example: 0x3f4a2b1c8d9e0f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9
//
// The file must be:
//   - Readable by both the EL and CL container users
//   - Permissions: 640 (root:root or docker group)
//   - Never regenerated if it already exists (breaks running consensus)
package jwt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SecretLength is the required byte length of the JWT secret (32 bytes = 256 bits).
	SecretLength = 32

	// DefaultPath is the conventional path used by eth-docker and this operator.
	DefaultPath = "/data/jwtsecret"
)

// Manager handles JWT secret lifecycle on a bare metal node.
type Manager struct {
	path string
}

// NewManager returns a Manager for the given file path.
func NewManager(path string) *Manager {
	if path == "" {
		path = DefaultPath
	}
	return &Manager{path: path}
}

// EnsureExists generates the JWT secret if it does not already exist.
// If the file exists and is valid, it is left untouched (idempotent).
// Returns the hex-encoded secret (with 0x prefix) and whether it was newly created.
func (m *Manager) EnsureExists() (secret string, created bool, err error) {
	// Check if already exists
	existing, err := m.Read()
	if err == nil {
		if err := validate(existing); err != nil {
			return "", false, fmt.Errorf("existing JWT secret at %s is invalid: %w", m.path, err)
		}
		return existing, false, nil
	}

	if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("check JWT secret: %w", err)
	}

	// Generate new secret
	secret, err = m.Generate()
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}

// Generate creates a new cryptographically random JWT secret and writes it to disk.
// It will overwrite any existing file. Use EnsureExists for idempotent behavior.
func (m *Manager) Generate() (string, error) {
	raw := make([]byte, SecretLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate JWT secret entropy: %w", err)
	}

	secret := "0x" + hex.EncodeToString(raw)

	if err := m.write(secret); err != nil {
		return "", err
	}

	return secret, nil
}

// Read reads the JWT secret from disk and returns it with the 0x prefix.
func (m *Manager) Read() (string, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(data))
	if err := validate(secret); err != nil {
		return "", fmt.Errorf("invalid JWT secret at %s: %w", m.path, err)
	}
	return secret, nil
}

// Path returns the file path this manager uses.
func (m *Manager) Path() string {
	return m.path
}

// Verify checks whether the JWT secret file exists and is valid.
func (m *Manager) Verify() error {
	secret, err := m.Read()
	if err != nil {
		return err
	}
	return validate(secret)
}

func (m *Manager) write(secret string) error {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(m.path), 0750); err != nil {
		return fmt.Errorf("create JWT secret directory: %w", err)
	}

	// Write atomically: write to temp file, then rename
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(secret+"\n"), 0640); err != nil {
		return fmt.Errorf("write JWT secret: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install JWT secret: %w", err)
	}
	return nil
}

// validate checks that a secret string is the correct format:
// "0x" prefix + exactly 64 lowercase hex characters.
func validate(secret string) error {
	if !strings.HasPrefix(secret, "0x") {
		return fmt.Errorf("must start with 0x")
	}
	hexPart := secret[2:]
	if len(hexPart) != 64 {
		return fmt.Errorf("hex part must be 64 characters, got %d", len(hexPart))
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return fmt.Errorf("invalid hex: %w", err)
	}
	return nil
}
