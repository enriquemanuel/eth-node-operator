package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/enriquemanuel/eth-node-operator/internal/collector"
	"github.com/enriquemanuel/eth-node-operator/internal/discover"
	"github.com/enriquemanuel/eth-node-operator/internal/maintenance"
	"github.com/enriquemanuel/eth-node-operator/internal/reconciler"
	"github.com/enriquemanuel/eth-node-operator/internal/ufw"
	"github.com/enriquemanuel/eth-node-operator/pkg/dockerclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/ethclient"
	"github.com/enriquemanuel/eth-node-operator/pkg/inventory"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Agent is the HTTP server that runs on each bare metal host.
type Agent struct {
	nodeName    string
	specPath    string
	listenAddr  string
	reconciler  *reconciler.Reconciler
	sysCollector *collector.SystemCollector
	docker      *dockerclient.Client
	eth         *ethclient.Client
	log         *slog.Logger

	apiKey   string

	mu       sync.RWMutex
	cordoned bool
	reason   string
	lastSpec *types.NodeSpec
}

// Config holds configuration for the agent.
type Config struct {
	NodeName   string
	SpecPath   string
	ListenAddr string
	ELEndpoint string
	CLEndpoint string
}

// New creates a new Agent.
func New(cfg Config, log *slog.Logger) *Agent {
	docker := dockerclient.New()
	eth := ethclient.New(cfg.ELEndpoint, cfg.CLEndpoint)
	ufwMgr := ufw.New()
	rec := reconciler.New(docker, eth, ufwMgr, log)
	sys := collector.NewSystemCollector()

	apiKey, err := loadOrCreateAPIKey()
	if err != nil {
		log.Warn("could not load/create API key, write endpoints will be unprotected", "err", err)
	}

	return &Agent{
		nodeName:     cfg.NodeName,
		specPath:     cfg.SpecPath,
		listenAddr:   cfg.ListenAddr,
		reconciler:   rec,
		sysCollector: sys,
		docker:       docker,
		eth:          eth,
		log:          log,
		apiKey:       apiKey,
	}
}

// Start runs the agent: HTTP server + reconciliation loop.
func (a *Agent) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", a.handleStatus)
	mux.HandleFunc("/cordon", requireAPIKey(a.apiKey, a.handleCordon))
	mux.HandleFunc("/reconcile", requireAPIKey(a.apiKey, a.handleReconcile))
	mux.HandleFunc("/diff", a.handleDiff)
	mux.HandleFunc("/logs", a.handleLogs)
	mux.HandleFunc("/restart", requireAPIKey(a.apiKey, a.handleRestart))
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/discover", a.handleDiscover)

	srv := &http.Server{
		Addr:           a.listenAddr,
		Handler:        http.MaxBytesHandler(mux, 1<<20), // 1 MB max request body
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   60 * time.Second, // longer for log streaming
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 16, // 64 KB
	}

	go a.reconcileLoop(ctx)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	a.log.Info("agent starting", "addr", a.listenAddr, "node", a.nodeName)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("agent server: %w", err)
	}
	return nil
}

// reconcileLoop runs reconciliation every 30 seconds.
func (a *Agent) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run once immediately on start
	a.runReconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runReconcile(ctx)
		}
	}
}

func (a *Agent) runReconcile(ctx context.Context) {
	a.mu.RLock()
	cordoned := a.cordoned
	a.mu.RUnlock()

	if cordoned {
		a.log.Info("skipping reconcile: node is cordoned")
		return
	}

	spec, err := a.loadSpec()
	if err != nil {
		a.log.Error("load spec", "err", err)
		return
	}

	// Check maintenance window if set
	if spec.Maintenance.Window.Schedule != "" {
		win, err := maintenance.New(spec.Maintenance.Window.Schedule)
		if err != nil {
			a.log.Warn("invalid maintenance window", "err", err)
		} else if !win.IsOpen(time.Now().UTC()) {
			a.log.Info("outside maintenance window, skipping upgrade reconciliation")
			// Still reconcile non-upgrade items (firewall, running containers)
		}
	}

	result, err := a.reconciler.Reconcile(ctx, *spec)
	if err != nil {
		a.log.Error("reconcile failed", "err", err)
		return
	}
	if len(result.Actions) > 0 {
		a.log.Info("reconcile complete", "actions", result.Actions, "duration", result.Duration)
	}
	if len(result.Errors) > 0 {
		a.log.Warn("reconcile had errors", "errors", result.Errors)
	}
}

func (a *Agent) loadSpec() (*types.NodeSpec, error) {
	node, err := inventory.LoadNode(a.specPath)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.lastSpec = &node.Spec
	a.mu.Unlock()
	return &node.Spec, nil
}

// handleStatus collects and returns the full node status.
func (a *Agent) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sys, err := a.sysCollector.Collect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	elStatus := a.collectClientStatus(ctx, "execution", "el")
	clStatus := a.collectClientStatus(ctx, "consensus", "cl")
	mevStatus := a.collectClientStatus(ctx, "mev-boost", "mev")

	a.mu.RLock()
	cordoned := a.cordoned
	a.mu.RUnlock()

	phase := types.PhaseRunning
	if cordoned {
		phase = types.PhaseCordoned
	} else if !elStatus.Running || !clStatus.Running {
		phase = types.PhaseDegraded
	}

	status := types.NodeStatus{
		Name:       a.nodeName,
		Phase:      phase,
		EL:         elStatus,
		CL:         clStatus,
		MEV:        mevStatus,
		System:     sys,
		Cordoned:   cordoned,
		ReportedAt: time.Now().UTC(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *Agent) collectClientStatus(ctx context.Context, containerName, clientType string) types.ClientStatus {
	info, err := a.docker.Inspect(ctx, containerName)
	if err != nil {
		return types.ClientStatus{Name: clientType, Running: false}
	}

	status := types.ClientStatus{
		Name:    clientType,
		Running: info.Running,
		Image:   info.Image,
		Version: info.Version,
		Healthy: info.Running,
	}

	if !info.Running {
		return status
	}

	switch clientType {
	case "el":
		syncing, _ := a.eth.ELSyncing(ctx)
		status.Synced = !syncing
		status.BlockNumber, _ = a.eth.ELBlockNumber(ctx)
		status.PeerCount, _ = a.eth.ELPeerCount(ctx)
	case "cl":
		syncing, _ := a.eth.CLSyncing(ctx)
		status.Synced = !syncing
		status.PeerCount, _ = a.eth.CLPeerCount(ctx)
	}

	return status
}

// handleCordon pauses or resumes the reconciliation loop.
func (a *Agent) handleCordon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.CordonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	a.cordoned = req.Cordoned
	a.reason = req.Reason
	a.mu.Unlock()

	action := "uncordoned"
	if req.Cordoned {
		action = "cordoned"
	}
	a.log.Info("node "+action, "reason", req.Reason)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"%s","reason":"%s"}`, action, req.Reason)
}

// handleReconcile triggers an immediate reconciliation cycle.
func (a *Agent) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	spec, err := a.loadSpec()
	if err != nil {
		http.Error(w, fmt.Sprintf("load spec: %v", err), http.StatusInternalServerError)
		return
	}

	result, err := a.reconciler.Reconcile(r.Context(), *spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleDiff returns drift between desired and actual state.
func (a *Agent) handleDiff(w http.ResponseWriter, r *http.Request) {
	spec, err := a.loadSpec()
	if err != nil {
		http.Error(w, fmt.Sprintf("load spec: %v", err), http.StatusInternalServerError)
		return
	}

	diff, err := a.reconciler.Diff(r.Context(), *spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(diff)
}

// handleLogs streams logs from the requested client container.
func (a *Agent) handleLogs(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	follow := r.URL.Query().Get("follow") == "true"

	containerName := ""
	switch client {
	case "el":
		containerName = "execution"
	case "cl":
		containerName = "consensus"
	case "mev":
		containerName = "mev-boost"
	default:
		http.Error(w, "client must be el, cl, or mev", http.StatusBadRequest)
		return
	}

	rc, err := a.docker.Logs(r.Context(), containerName, follow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	buf := make([]byte, 4096)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

// handleRestart restarts a specific client container.
func (a *Agent) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	client := r.URL.Query().Get("client")
	containerName := ""
	switch client {
	case "el":
		containerName = "execution"
	case "cl":
		containerName = "consensus"
	case "mev":
		containerName = "mev-boost"
	default:
		http.Error(w, "client must be el, cl, or mev", http.StatusBadRequest)
		return
	}

	if err := a.docker.Restart(r.Context(), containerName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.log.Info("container restarted via API", "container", containerName)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"restarted":"%s"}`, containerName)
}

// handleDiscover runs node auto-discovery probes and returns the results.
// Used by ethctl discover to generate cluster file YAML snippets.
// GET-only, no auth required — read-only probe with no side effects.
func (a *Agent) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report := discover.Run(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// handleHealthz is a simple liveness check.
func (a *Agent) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}
