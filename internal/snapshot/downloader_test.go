package snapshot_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/snapshot"
)

func TestIsDatadirEmpty_MissingDir(t *testing.T) {
	empty, err := snapshot.IsDatadirEmpty("/does/not/exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !empty {
		t.Error("expected empty=true for missing dir")
	}
}

func TestIsDatadirEmpty_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	empty, err := snapshot.IsDatadirEmpty(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !empty {
		t.Error("expected empty=true for empty dir")
	}
}

func TestIsDatadirEmpty_NonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "geth.ipc"), []byte("data"), 0644)

	empty, err := snapshot.IsDatadirEmpty(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if empty {
		t.Error("expected empty=false for non-empty dir")
	}
}

func TestClientSlug_KnownClients(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"geth", "geth"},
		{"GETH", "geth"},
		{"Geth", "geth"},
		{"nethermind", "nethermind"},
		{"besu", "besu"},
		{"reth", "reth"},
		{"lighthouse", "lighthouse"},
		{"teku", "teku"},
		{"prysm", "prysm"},
		{"nimbus", "nimbus"},
		{"lodestar", "lodestar"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			slug, err := snapshot.ClientSlug(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if slug != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, slug)
			}
		})
	}
}

func TestClientSlug_UnknownClient(t *testing.T) {
	_, err := snapshot.ClientSlug("unknown-client-xyz")
	if err == nil {
		t.Error("expected error for unknown client")
	}
}

func TestLatestBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mainnet/geth/latest" {
			w.Write([]byte("21500000\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d := snapshot.NewWithBase(srv.URL)
	block, err := d.LatestBlock(context.Background(), "mainnet", "geth")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if block != "21500000" {
		t.Errorf("expected 21500000, got %q", block)
	}
}

func TestLatestBlock_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d := snapshot.NewWithBase(srv.URL)
	_, err := d.LatestBlock(context.Background(), "mainnet", "unknown")
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestDiscover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mainnet/lighthouse/latest" {
			w.Write([]byte("9000000"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d := snapshot.NewWithBase(srv.URL)
	info, err := d.Discover(context.Background(), "mainnet", "lighthouse")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.BlockNumber != "9000000" {
		t.Errorf("expected block 9000000, got %s", info.BlockNumber)
	}
	if info.Client != "lighthouse" {
		t.Errorf("expected client lighthouse, got %s", info.Client)
	}
	if info.Network != "mainnet" {
		t.Errorf("expected network mainnet, got %s", info.Network)
	}
}

func TestDiscover_UnknownClient(t *testing.T) {
	d := snapshot.NewWithBase("http://localhost")
	_, err := d.Discover(context.Background(), "mainnet", "unsupported-client")
	if err == nil {
		t.Error("expected error for unsupported client")
	}
}

func TestRestoreIfEmpty_NonEmptyDir_Skips(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "existing.db"), []byte("data"), 0644)

	d := snapshot.NewWithBase("http://should-not-be-called")
	restored, err := d.RestoreIfEmpty(context.Background(), "mainnet", "geth", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restored {
		t.Error("should not restore when dir is non-empty")
	}
}

func TestRestoreIfEmpty_UnsupportedClient_Skips(t *testing.T) {
	dir := t.TempDir() // empty dir

	// An unsupported client should skip gracefully, not error
	d := snapshot.NewWithBase("http://should-not-be-called")
	restored, err := d.RestoreIfEmpty(context.Background(), "mainnet", "grandine", dir)
	if err != nil {
		t.Fatalf("unexpected error for unsupported client: %v", err)
	}
	if restored {
		t.Error("should not restore for unsupported client")
	}
}

func TestWasRestored_WithMarker(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".snapshot-restored"), []byte("client=geth"), 0644)

	if !snapshot.WasRestored(dir) {
		t.Error("expected WasRestored=true when marker exists")
	}
}

func TestWasRestored_WithoutMarker(t *testing.T) {
	dir := t.TempDir()
	if snapshot.WasRestored(dir) {
		t.Error("expected WasRestored=false when no marker")
	}
}
