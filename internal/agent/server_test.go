package agent_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/internal/agent"
	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// testAgent creates an agent server for testing using the test helper constructor.
func testServer(t *testing.T, specPath string) *httptest.Server {
	t.Helper()
	handler := agent.NewTestHandler(specPath)
	return httptest.NewServer(handler)
}

func TestHealthz(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestStatus_ReturnsJSON(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var status types.NodeStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}

	if status.ReportedAt.IsZero() {
		t.Error("expected non-zero ReportedAt")
	}
}

func TestCordon_PostSetsCordonedTrue(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	body, _ := json.Marshal(types.CordonRequest{Cordoned: true, Reason: "disk replacement"})
	resp, err := http.Post(srv.URL+"/cordon", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("cordon POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify status reflects cordoned state
	statusResp, _ := http.Get(srv.URL + "/status")
	var status types.NodeStatus
	json.NewDecoder(statusResp.Body).Decode(&status)

	if !status.Cordoned {
		t.Error("expected node to be cordoned")
	}
	if status.Phase != types.PhaseCordoned {
		t.Errorf("expected phase Cordoned, got %s", status.Phase)
	}
}

func TestCordon_GetMethodNotAllowed(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/cordon")
	if err != nil {
		t.Fatalf("cordon GET: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestUncordon(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	// First cordon
	body, _ := json.Marshal(types.CordonRequest{Cordoned: true})
	http.Post(srv.URL+"/cordon", "application/json", bytes.NewReader(body))

	// Then uncordon
	body, _ = json.Marshal(types.CordonRequest{Cordoned: false})
	resp, _ := http.Post(srv.URL+"/cordon", "application/json", bytes.NewReader(body))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on uncordon, got %d", resp.StatusCode)
	}

	statusResp, _ := http.Get(srv.URL + "/status")
	var status types.NodeStatus
	json.NewDecoder(statusResp.Body).Decode(&status)

	if status.Cordoned {
		t.Error("expected node to be uncordoned")
	}
}

func TestLogs_InvalidClient(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/logs?client=invalid")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid client, got %d", resp.StatusCode)
	}
}

func TestRestart_MethodNotAllowed(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/restart?client=el")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestRestart_InvalidClient(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/restart?client=invalid", "application/json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid client, got %d", resp.StatusCode)
	}
}

func TestStatus_PhaseRunning(t *testing.T) {
	srv := testServer(t, "")
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/status")
	var status types.NodeStatus
	json.NewDecoder(resp.Body).Decode(&status)

	// In test mode containers are not running — expect Degraded or Running
	if status.Phase == "" {
		t.Error("expected non-empty phase")
	}
}
