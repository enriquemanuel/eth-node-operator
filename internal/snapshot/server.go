package snapshot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server serves client snapshots over HTTP in a format compatible with
// the ethpandaops snapshot API. This means the Downloader works identically
// whether pointing at ethpandaops or your self-hosted server.
//
// URL layout:
//
//	GET /                              → JSON index of all available snapshots
//	GET /{network}/{client}/latest     → block number of latest snapshot (text)
//	GET /{network}/{client}/{block}/snapshot.tar.zst → the archive
//
// Snapshot storage layout on disk (rootDir):
//
//	{rootDir}/{network}/{client}/latest            → text: block number
//	{rootDir}/{network}/{client}/{block}/snapshot.tar.zst
type Server struct {
	rootDir string
	log     *slog.Logger
	mux     *http.ServeMux
}

// NewServer returns a Server rooted at rootDir.
func NewServer(rootDir string, log *slog.Logger) *Server {
	s := &Server{rootDir: rootDir, log: log}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	// All other paths are handled by the file-based lookup
	s.mux.HandleFunc("/mainnet/", s.handleSnapshot)
	s.mux.HandleFunc("/sepolia/", s.handleSnapshot)
	s.mux.HandleFunc("/hoodi/", s.handleSnapshot)
	s.mux.HandleFunc("/holesky/", s.handleSnapshot)
	return s
}

// Handler returns the http.Handler for this server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// handleHealthz is a liveness probe.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// handleIndex returns a JSON index of all available snapshots.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	type snapshotEntry struct {
		Network     string    `json:"network"`
		Client      string    `json:"client"`
		BlockNumber string    `json:"blockNumber"`
		URL         string    `json:"url"`
		SizeBytes   int64     `json:"sizeBytes,omitempty"`
		UpdatedAt   time.Time `json:"updatedAt"`
	}

	var entries []snapshotEntry

	networks, _ := os.ReadDir(s.rootDir)
	for _, net := range networks {
		if !net.IsDir() {
			continue
		}
		clients, _ := os.ReadDir(filepath.Join(s.rootDir, net.Name()))
		for _, cl := range clients {
			if !cl.IsDir() {
				continue
			}
			latestPath := filepath.Join(s.rootDir, net.Name(), cl.Name(), "latest")
			data, err := os.ReadFile(latestPath)
			if err != nil {
				continue
			}
			block := strings.TrimSpace(string(data))
			archivePath := filepath.Join(s.rootDir, net.Name(), cl.Name(), block, "snapshot.tar.zst")
			info, _ := os.Stat(archivePath)
			var size int64
			var updatedAt time.Time
			if info != nil {
				size = info.Size()
				updatedAt = info.ModTime()
			}
			host := r.Host
			if host == "" {
				host = "localhost"
			}
			entries = append(entries, snapshotEntry{
				Network:     net.Name(),
				Client:      cl.Name(),
				BlockNumber: block,
				URL:         fmt.Sprintf("http://%s/%s/%s/%s/snapshot.tar.zst", host, net.Name(), cl.Name(), block),
				SizeBytes:   size,
				UpdatedAt:   updatedAt,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// handleSnapshot serves either the latest block number or the snapshot archive.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	// Path: /{network}/{client}/latest   OR
	//       /{network}/{client}/{block}/snapshot.tar.zst
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)

	switch len(parts) {
	case 3:
		// /{network}/{client}/latest
		if parts[2] != "latest" {
			http.NotFound(w, r)
			return
		}
		s.handleLatest(w, r, parts[0], parts[1])

	case 4:
		// /{network}/{client}/{block}/snapshot.tar.zst
		if parts[3] != "snapshot.tar.zst" {
			http.NotFound(w, r)
			return
		}
		s.handleArchive(w, r, parts[0], parts[1], parts[2])

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request, network, client string) {
	latestPath := filepath.Join(s.rootDir, network, client, "latest")
	data, err := os.ReadFile(latestPath)
	if err != nil {
		s.log.Warn("latest block not found", "network", network, "client", client)
		http.Error(w, fmt.Sprintf("no snapshot available for %s/%s", network, client), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(strings.TrimSpace(string(data))))
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request, network, client, block string) {
	archivePath := filepath.Join(s.rootDir, network, client, block, "snapshot.tar.zst")
	f, err := os.Open(archivePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("snapshot not found: %s/%s/%s", network, client, block), http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat error", http.StatusInternalServerError)
		return
	}

	s.log.Info("serving snapshot", "network", network, "client", client, "block", block,
		"size_gb", fmt.Sprintf("%.1f", float64(info.Size())/(1<<30)))

	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="snapshot.tar.zst"`))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	// Support range requests (curl --continue-at)
	http.ServeContent(w, r, "snapshot.tar.zst", info.ModTime(), f)
}
