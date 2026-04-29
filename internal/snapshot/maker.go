// Package snapshot provides both download (restore) and creation (snapshot server)
// functionality for Ethereum client datadirs.
package snapshot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MakerConfig describes how to create snapshots for a client.
type MakerConfig struct {
	// Client is the client name (geth, lighthouse, etc.)
	Client string
	// DataDir is the client datadir to snapshot.
	DataDir string
	// OutputDir is where completed snapshots are written.
	OutputDir string
	// ContainerName is the Docker container to stop before snapshotting.
	ContainerName string
	// StopTimeout is how long to wait for graceful container shutdown.
	StopTimeout time.Duration
	// ELRPCURL is used to read the current block number (EL clients only).
	ELRPCURL string
	// CLRestURL is used to read the current slot (CL clients only).
	CLRestURL string
}

// SnapshotResult describes a completed snapshot.
type SnapshotResult struct {
	Client      string
	DataDir     string
	OutputPath  string
	BlockNumber string
	CompressedSize int64
	Duration    time.Duration
	CreatedAt   time.Time
}

// Maker creates snapshots by stopping a client, archiving its datadir,
// then restarting the client. Snapshots are hosted by Server.
type Maker struct {
	cfg MakerConfig
}

// NewMaker returns a Maker for the given config.
func NewMaker(cfg MakerConfig) *Maker {
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 2 * time.Minute
	}
	return &Maker{cfg: cfg}
}

// CreateSnapshot stops the client, archives the datadir, and restarts.
// The archive is written to OutputDir/{blockNumber}/snapshot.tar.zst
// in ethpandaops-compatible format so the same downloader works for both.
func (m *Maker) CreateSnapshot(ctx context.Context) (*SnapshotResult, error) {
	start := time.Now()

	if err := os.MkdirAll(m.cfg.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	// 1. Get current block/slot before stopping
	blockNumber, err := m.currentBlock(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current block: %w", err)
	}

	// 2. Stop the container gracefully
	if err := m.stopContainer(ctx); err != nil {
		return nil, fmt.Errorf("stop container %s: %w", m.cfg.ContainerName, err)
	}
	defer m.startContainer(ctx) //nolint:errcheck — always restart even on error

	// 3. Create output directory for this block
	blockDir := filepath.Join(m.cfg.OutputDir, blockNumber)
	if err := os.MkdirAll(blockDir, 0755); err != nil {
		return nil, fmt.Errorf("create block dir: %w", err)
	}

	outPath := filepath.Join(blockDir, "snapshot.tar.zst")

	// 4. Archive datadir → zstd compressed tar
	// --use-compress-program=zstd for streaming compression
	// -c: create, --use-compress-program: pipe through zstd
	// Uses zstd level 3 (good balance of speed vs size for large datadirs)
	// No bash -c: args are passed directly, no shell injection possible.
	cmd := exec.CommandContext(ctx,
		"tar",
		"--use-compress-program=zstd -T0 -3",
		"-cf", outPath,
		"-C", m.cfg.DataDir,
		".",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		os.Remove(outPath) //nolint:errcheck
		return nil, fmt.Errorf("create snapshot archive: %w", err)
	}

	// 5. Write "latest" pointer file
	latestPath := filepath.Join(m.cfg.OutputDir, "latest")
	if err := os.WriteFile(latestPath, []byte(blockNumber), 0644); err != nil {
		return nil, fmt.Errorf("write latest pointer: %w", err)
	}

	// 6. Get compressed size
	info, _ := os.Stat(outPath)
	var compressedSize int64
	if info != nil {
		compressedSize = info.Size()
	}

	return &SnapshotResult{
		Client:         m.cfg.Client,
		DataDir:        m.cfg.DataDir,
		OutputPath:     outPath,
		BlockNumber:    blockNumber,
		CompressedSize: compressedSize,
		Duration:       time.Since(start),
		CreatedAt:      time.Now().UTC(),
	}, nil
}

// PruneOld removes snapshot archives older than keep most recent ones.
func (m *Maker) PruneOld(keep int) error {
	entries, err := os.ReadDir(m.cfg.OutputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}

	// Collect numeric block dirs
	type entry struct {
		name  string
		block uint64
	}
	var dirs []entry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.ParseUint(e.Name(), 10, 64)
		if err != nil {
			continue // skip non-numeric dirs
		}
		dirs = append(dirs, entry{e.Name(), n})
	}

	// Sort ascending by block number
	for i := 0; i < len(dirs)-1; i++ {
		for j := i + 1; j < len(dirs); j++ {
			if dirs[i].block > dirs[j].block {
				dirs[i], dirs[j] = dirs[j], dirs[i]
			}
		}
	}

	// Remove oldest beyond keep count
	toRemove := len(dirs) - keep
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(m.cfg.OutputDir, dirs[i].name)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove old snapshot %s: %w", path, err)
		}
	}
	return nil
}

func (m *Maker) stopContainer(ctx context.Context) error {
	timeout := int(m.cfg.StopTimeout.Seconds())
	cmd := exec.CommandContext(ctx, "docker", "stop",
		fmt.Sprintf("--time=%d", timeout),
		m.cfg.ContainerName,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker stop: %s: %w", string(out), err)
	}
	return nil
}

func (m *Maker) startContainer(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "start", m.cfg.ContainerName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker start: %s: %w", string(out), err)
	}
	return nil
}

func (m *Maker) currentBlock(ctx context.Context) (string, error) {
	// Try EL first (returns block number)
	if m.cfg.ELRPCURL != "" {
		block, err := fetchELBlock(ctx, m.cfg.ELRPCURL)
		if err == nil {
			return block, nil
		}
	}
	// Try CL (returns slot number)
	if m.cfg.CLRestURL != "" {
		slot, err := fetchCLSlot(ctx, m.cfg.CLRestURL)
		if err == nil {
			return slot, nil
		}
	}
	// Fall back to timestamp if neither available
	return fmt.Sprintf("%d", time.Now().Unix()), nil
}

func fetchELBlock(ctx context.Context, rpcURL string) (string, error) {
	cmd := exec.CommandContext(ctx, "curl", "-sf", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-d", `{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`,
		rpcURL,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Parse "result":"0x1234567"
	s := string(out)
	start := strings.Index(s, `"result":"`) + len(`"result":"`)
	if start < len(`"result":"`) {
		return "", fmt.Errorf("parse block number response")
	}
	end := strings.Index(s[start:], `"`)
	if end < 0 {
		return "", fmt.Errorf("parse block number end")
	}
	hex := s[start : start+end]
	var n uint64
	fmt.Sscanf(hex, "0x%x", &n)
	return fmt.Sprintf("%d", n), nil
}

func fetchCLSlot(ctx context.Context, restURL string) (string, error) {
	cmd := exec.CommandContext(ctx, "curl", "-sf",
		restURL+"/eth/v1/node/syncing",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	s := string(out)
	start := strings.Index(s, `"head_slot":"`) + len(`"head_slot":"`)
	if start < len(`"head_slot":"`) {
		return "", fmt.Errorf("parse head slot")
	}
	end := strings.Index(s[start:], `"`)
	if end < 0 {
		return "", fmt.Errorf("parse head slot end")
	}
	return s[start : start+end], nil
}
