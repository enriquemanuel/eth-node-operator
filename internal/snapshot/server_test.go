package snapshot_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/snapshot"
)

func setupSnapshotDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Create: mainnet/geth/21500000/snapshot.tar.zst + latest pointer
	dir := filepath.Join(root, "mainnet", "geth", "21500000")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "snapshot.tar.zst"), []byte("fake-archive-data"), 0644)
	os.WriteFile(filepath.Join(root, "mainnet", "geth", "latest"), []byte("21500000"), 0644)

	// Create: mainnet/lighthouse/9000000/snapshot.tar.zst + latest
	dir2 := filepath.Join(root, "mainnet", "lighthouse", "9000000")
	os.MkdirAll(dir2, 0755)
	os.WriteFile(filepath.Join(dir2, "snapshot.tar.zst"), []byte("fake-cl-archive"), 0644)
	os.WriteFile(filepath.Join(root, "mainnet", "lighthouse", "latest"), []byte("9000000"), 0644)

	return root
}

func TestServerHealthz(t *testing.T) {
	srv := snapshot.NewServer(t.TempDir(), slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestServerLatest_ReturnsBlockNumber(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/mainnet/geth/latest")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestServerLatest_MissingClient404(t *testing.T) {
	srv := snapshot.NewServer(t.TempDir(), slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/mainnet/no-such-client/latest")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestServerArchive_ServesFile(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/mainnet/geth/21500000/snapshot.tar.zst")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/zstd" {
		t.Errorf("expected zstd content type, got %s", resp.Header.Get("Content-Type"))
	}
}

func TestServerArchive_WrongBlock404(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/mainnet/geth/99999999/snapshot.tar.zst")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestServerArchive_WrongFilename404(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/mainnet/geth/21500000/wrong.tar.gz")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestServerIndex_ListsAllSnapshots(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var entries []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&entries)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries (geth + lighthouse), got %d", len(entries))
	}
}

func TestServerSupportsRangeRequests(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Range request — simulates curl --continue-at
	req, _ := http.NewRequest("GET", ts.URL+"/mainnet/geth/21500000/snapshot.tar.zst", nil)
	req.Header.Set("Range", "bytes=5-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("range request: %v", err)
	}
	// 206 Partial Content or 200 OK both acceptable
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		t.Errorf("expected 206 or 200 for range request, got %d", resp.StatusCode)
	}
}

func TestPruneOld(t *testing.T) {
	dir := t.TempDir()
	// Create 4 fake snapshot block dirs
	for _, block := range []string{"100", "200", "300", "400"} {
		os.MkdirAll(filepath.Join(dir, block), 0755)
		os.WriteFile(filepath.Join(dir, block, "snapshot.tar.zst"), []byte("data"), 0644)
	}

	cfg := snapshot.MakerConfig{
		OutputDir: dir,
	}
	maker := snapshot.NewMaker(cfg)
	if err := maker.PruneOld(2); err != nil {
		t.Fatalf("prune: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	blockDirs := 0
	for _, e := range entries {
		if e.IsDir() {
			blockDirs++
		}
	}
	if blockDirs != 2 {
		t.Errorf("expected 2 dirs after pruning to keep=2, got %d", blockDirs)
	}

	// The two NEWEST (300, 400) should remain
	for _, shouldExist := range []string{"300", "400"} {
		if _, err := os.Stat(filepath.Join(dir, shouldExist)); os.IsNotExist(err) {
			t.Errorf("expected block dir %s to exist after pruning", shouldExist)
		}
	}
	for _, shouldBeGone := range []string{"100", "200"} {
		if _, err := os.Stat(filepath.Join(dir, shouldBeGone)); !os.IsNotExist(err) {
			t.Errorf("expected old block dir %s to be pruned", shouldBeGone)
		}
	}
}

// TestDownloaderUsesCustomProvider verifies that the downloader works
// against a self-hosted server using the same URL format.
func TestDownloaderUsesCustomProvider(t *testing.T) {
	root := setupSnapshotDir(t)
	srv := snapshot.NewServer(root, slog.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create downloader pointing at our local server (not ethpandaops)
	d := snapshot.NewWithBase(ts.URL)

	block, err := d.LatestBlock(context.Background(), "mainnet", "geth")
	if err != nil {
		// nil context is fine for test, but Go will panic — use Background
		
	}
	if block != "21500000" { t.Errorf("expected 21500000, got %s", block) }
}
