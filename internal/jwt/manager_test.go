package jwt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/jwt"
)

func TestEnsureExists_CreatesNewSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwtsecret")

	m := jwt.NewManager(path)
	secret, created, err := m.EnsureExists()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected created=true for new secret")
	}
	if !strings.HasPrefix(secret, "0x") {
		t.Errorf("secret must start with 0x, got: %s", secret)
	}
	if len(secret) != 66 { // "0x" + 64 hex chars
		t.Errorf("expected 66 chars, got %d: %s", len(secret), secret)
	}
}

func TestEnsureExists_IdempotentWhenExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwtsecret")

	m := jwt.NewManager(path)

	// Create once
	first, _, err := m.EnsureExists()
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Call again — must not regenerate
	second, created, err := m.EnsureExists()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created {
		t.Error("expected created=false when secret already exists")
	}
	if first != second {
		t.Errorf("secret changed on second call: %q != %q", first, second)
	}
}

func TestGenerate_ProducesValidFormat(t *testing.T) {
	dir := t.TempDir()
	m := jwt.NewManager(filepath.Join(dir, "jwtsecret"))

	secret, err := m.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if !strings.HasPrefix(secret, "0x") {
		t.Errorf("expected 0x prefix, got: %q", secret)
	}
	if len(secret) != 66 {
		t.Errorf("expected 66 chars, got %d", len(secret))
	}
}

func TestGenerate_TwoCallsProduceDifferentSecrets(t *testing.T) {
	dir := t.TempDir()
	m1 := jwt.NewManager(filepath.Join(dir, "jwt1"))
	m2 := jwt.NewManager(filepath.Join(dir, "jwt2"))

	s1, _ := m1.Generate()
	s2, _ := m2.Generate()

	if s1 == s2 {
		t.Error("two generated secrets should not be identical")
	}
}

func TestRead_ReturnsPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwtsecret")

	m := jwt.NewManager(path)
	written, _ := m.Generate()

	read, err := m.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read != written {
		t.Errorf("read %q, want %q", read, written)
	}
}

func TestRead_FileMissing(t *testing.T) {
	m := jwt.NewManager("/does/not/exist/jwtsecret")
	_, err := m.Read()
	if err == nil {
		t.Error("expected error reading missing file")
	}
}

func TestVerify_ValidSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwtsecret")
	m := jwt.NewManager(path)

	m.Generate()
	if err := m.Verify(); err != nil {
		t.Errorf("unexpected verify error: %v", err)
	}
}

func TestVerify_InvalidFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwtsecret")

	cases := []struct {
		name    string
		content string
	}{
		{"no 0x prefix", "3f4a2b1c8d9e0f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f90"},
		{"too short", "0x3f4a2b"},
		{"non-hex chars", "0xgggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.WriteFile(path, []byte(tc.content), 0640)
			m := jwt.NewManager(path)
			if err := m.Verify(); err == nil {
				t.Errorf("expected verify error for %q", tc.content)
			}
		})
	}
}

func TestEnsureExists_CreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "jwtsecret")

	m := jwt.NewManager(path)
	_, _, err := m.EnsureExists()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected file to be created including parent dirs")
	}
}

func TestEnsureExists_ExistingInvalidSecretReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwtsecret")

	// Write a garbage file
	os.WriteFile(path, []byte("not-a-valid-secret"), 0640)

	m := jwt.NewManager(path)
	_, _, err := m.EnsureExists()
	if err == nil {
		t.Error("expected error for invalid existing secret")
	}
}
