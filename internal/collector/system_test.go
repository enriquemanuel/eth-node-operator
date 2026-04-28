package collector_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/collector"
)

func writeProcFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write proc file %s: %v", name, err)
	}
}

func fakeProcDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	writeProcFile(t, dir, "meminfo", `MemTotal:       65536000 kB
MemFree:        32768000 kB
Buffers:         1024000 kB
Cached:          4096000 kB
SwapTotal:       8388608 kB
SwapFree:        8388608 kB
`)

	writeProcFile(t, dir, "uptime", "86400.00 172800.00\n") // 24 hours up

	writeProcFile(t, dir, "version",
		"Linux version 6.5.0-ubuntu (gcc) #1 SMP Mon Jan 1 00:00:00 UTC 2024\n")

	return dir
}

func TestCollect_MemoryStats(t *testing.T) {
	proc := fakeProcDir(t)
	c := collector.NewSystemCollectorWithPath(proc)

	status, err := c.Collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	// MemTotal: 65536000 kB = ~62.5 GB
	if status.MemTotalGB < 60 || status.MemTotalGB > 65 {
		t.Errorf("unexpected total mem: %.2f GB", status.MemTotalGB)
	}

	// MemUsed = Total - Free - Buffers - Cached
	// = 65536000 - 32768000 - 1024000 - 4096000 = 27648000 kB = ~26.4 GB
	if status.MemUsedGB < 25 || status.MemUsedGB > 28 {
		t.Errorf("unexpected used mem: %.2f GB", status.MemUsedGB)
	}
}

func TestCollect_UptimeHours(t *testing.T) {
	proc := fakeProcDir(t)
	c := collector.NewSystemCollectorWithPath(proc)

	status, err := c.Collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	// 86400 seconds = 24 hours
	if status.UptimeHours < 23.9 || status.UptimeHours > 24.1 {
		t.Errorf("expected ~24h uptime, got %.2f", status.UptimeHours)
	}
}

func TestCollect_KernelVersion(t *testing.T) {
	proc := fakeProcDir(t)
	c := collector.NewSystemCollectorWithPath(proc)

	status, err := c.Collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if status.KernelVer != "6.5.0-ubuntu" {
		t.Errorf("unexpected kernel version: %s", status.KernelVer)
	}
}

func TestCollect_MissingProcMeminfo(t *testing.T) {
	proc := t.TempDir() // empty, no meminfo
	writeProcFile(t, proc, "uptime", "100.0 200.0\n")
	writeProcFile(t, proc, "version", "Linux version 6.0\n")

	c := collector.NewSystemCollectorWithPath(proc)
	_, err := c.Collect()
	if err == nil {
		t.Error("expected error when meminfo is missing")
	}
}

func TestCollect_Hostname(t *testing.T) {
	proc := fakeProcDir(t)
	c := collector.NewSystemCollectorWithPath(proc)

	status, err := c.Collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if status.Hostname == "" {
		t.Error("expected non-empty hostname")
	}
}
