package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// testHandler is a minimal HTTP handler for testing that avoids real docker/eth deps.
type testHandler struct {
	mu       sync.RWMutex
	cordoned bool
	reason   string
	log      *slog.Logger
}

// NewTestHandler returns an http.Handler suitable for unit tests.
// It replaces real docker/eth calls with safe stubs.
func NewTestHandler(_ string) http.Handler {
	h := &testHandler{
		log: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", h.handleStatus)
	mux.HandleFunc("/cordon", h.handleCordon)
	mux.HandleFunc("/reconcile", h.handleReconcile)
	mux.HandleFunc("/diff", h.handleDiff)
	mux.HandleFunc("/logs", h.handleLogs)
	mux.HandleFunc("/restart", h.handleRestart)
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

func (h *testHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	cordoned := h.cordoned
	h.mu.RUnlock()

	phase := types.PhaseRunning
	if cordoned {
		phase = types.PhaseCordoned
	}

	status := types.NodeStatus{
		Name:       "test-node",
		Phase:      phase,
		Cordoned:   cordoned,
		ReportedAt: time.Now().UTC(),
		EL:         types.ClientStatus{Name: "el", Running: false},
		CL:         types.ClientStatus{Name: "cl", Running: false},
		MEV:        types.ClientStatus{Name: "mev", Running: false},
	}

	if !cordoned && (!status.EL.Running || !status.CL.Running) {
		status.Phase = types.PhaseDegraded
	}
	if cordoned {
		status.Phase = types.PhaseCordoned
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (h *testHandler) handleCordon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req types.CordonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	h.cordoned = req.Cordoned
	h.reason = req.Reason
	h.mu.Unlock()

	action := "uncordoned"
	if req.Cordoned {
		action = "cordoned"
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"%s"}`, action)
}

func (h *testHandler) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := types.ReconcileResult{
		NodeName: "test-node",
		Actions:  []string{"test reconcile triggered"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *testHandler) handleDiff(w http.ResponseWriter, r *http.Request) {
	diff := types.DiffResult{NodeName: "test-node", InSync: true}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(diff)
}

func (h *testHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	switch client {
	case "el", "cl", "mev":
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "2024-01-01T00:00:00Z [INFO] %s logs stub\n", client)
	default:
		http.Error(w, "client must be el, cl, or mev", http.StatusBadRequest)
	}
}

func (h *testHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	client := r.URL.Query().Get("client")
	switch client {
	case "el", "cl", "mev":
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"restarted":"%s"}`, client)
	default:
		http.Error(w, "client must be el, cl, or mev", http.StatusBadRequest)
	}
}

func (h *testHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}
