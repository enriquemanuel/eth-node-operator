package collector

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// SystemCollector gathers OS-level metrics from /proc and /sys.
type SystemCollector struct {
	procPath string // override for testing
}

// NewSystemCollector returns a SystemCollector reading from real /proc.
func NewSystemCollector() *SystemCollector {
	return &SystemCollector{procPath: "/proc"}
}

// NewSystemCollectorWithPath returns a SystemCollector with a custom /proc path.
func NewSystemCollectorWithPath(path string) *SystemCollector {
	return &SystemCollector{procPath: path}
}

// Collect returns current system metrics.
func (s *SystemCollector) Collect() (types.SystemStatus, error) {
	hostname, _ := os.Hostname()

	memTotal, memUsed, err := s.memStats()
	if err != nil {
		return types.SystemStatus{}, fmt.Errorf("mem stats: %w", err)
	}

	diskUsed, diskFree, err := diskStats("/data")
	if err != nil {
		// Non-fatal: /data might not exist in all envs
		diskUsed, diskFree = 0, 0
	}

	uptimeHours, err := s.uptime()
	if err != nil {
		uptimeHours = 0
	}

	kernelVer, _ := s.kernelVersion()

	return types.SystemStatus{
		Hostname:    hostname,
		CPUPercent:  cpuPercent(),
		MemUsedGB:   bytesToGB(memUsed),
		MemTotalGB:  bytesToGB(memTotal),
		DiskUsedGB:  bytesToGB(diskUsed),
		DiskFreeGB:  bytesToGB(diskFree),
		UptimeHours: uptimeHours,
		KernelVer:   kernelVer,
	}, nil
}

func (s *SystemCollector) memStats() (total, used uint64, err error) {
	f, err := os.Open(s.procPath + "/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var memFree, memBuffers, memCached uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		val *= 1024 // kB to bytes

		switch fields[0] {
		case "MemTotal:":
			total = val
		case "MemFree:":
			memFree = val
		case "Buffers:":
			memBuffers = val
		case "Cached:":
			memCached = val
		}
	}
	used = total - memFree - memBuffers - memCached
	return total, used, scanner.Err()
}

func (s *SystemCollector) uptime() (float64, error) {
	f, err := os.Open(s.procPath + "/uptime")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var uptimeSecs float64
	fmt.Fscanf(f, "%f", &uptimeSecs)
	return uptimeSecs / 3600.0, nil
}

func (s *SystemCollector) kernelVersion() (string, error) {
	data, err := os.ReadFile(s.procPath + "/version")
	if err != nil {
		return runtime.Version(), err
	}
	parts := strings.Fields(string(data))
	if len(parts) >= 3 {
		return parts[2], nil
	}
	return string(data), nil
}

func diskStats(path string) (used, free uint64, err error) {
	var stat syscallStatfs
	if err := statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	used = total - avail
	free = avail
	return used, free, nil
}

func cpuPercent() float64 {
	// A real implementation would read /proc/stat twice with a sleep interval.
	// This reads the current idle percentage from /proc/stat as a snapshot.
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			break
		}
		var vals [8]uint64
		for i := 1; i <= 7; i++ {
			vals[i-1], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		user, nice, system, idle, iowait, irq, softirq :=
			vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6]

		total := user + nice + system + idle + iowait + irq + softirq
		if total == 0 {
			return 0
		}
		return float64(total-idle) / float64(total) * 100.0
	}
	return 0
}

func bytesToGB(b uint64) float64 {
	return float64(b) / (1024 * 1024 * 1024)
}
