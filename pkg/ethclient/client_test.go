package ethclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enriquemanuel/eth-node-operator/pkg/ethclient"
)

func makeELServer(t *testing.T, method string, result interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  result,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func makeCLServer(t *testing.T, path string, body interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(body)
	}))
}

func TestELSyncing_WhenSynced(t *testing.T) {
	srv := makeELServer(t, "eth_syncing", false)
	defer srv.Close()

	c := ethclient.New(srv.URL, "")
	syncing, err := c.ELSyncing(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if syncing {
		t.Error("expected syncing=false when node is synced")
	}
}

func TestELSyncing_WhenSyncing(t *testing.T) {
	srv := makeELServer(t, "eth_syncing", map[string]interface{}{
		"startingBlock": "0x0",
		"currentBlock":  "0x100",
		"highestBlock":  "0x200",
	})
	defer srv.Close()

	c := ethclient.New(srv.URL, "")
	syncing, err := c.ELSyncing(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !syncing {
		t.Error("expected syncing=true when node is mid-sync")
	}
}

func TestELBlockNumber(t *testing.T) {
	srv := makeELServer(t, "eth_blockNumber", "0x1234567")
	defer srv.Close()

	c := ethclient.New(srv.URL, "")
	block, err := c.ELBlockNumber(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if block != 0x1234567 {
		t.Errorf("expected block 0x1234567, got %d", block)
	}
}

func TestELPeerCount(t *testing.T) {
	srv := makeELServer(t, "net_peerCount", "0x52")
	defer srv.Close()

	c := ethclient.New(srv.URL, "")
	peers, err := c.ELPeerCount(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if peers != 82 {
		t.Errorf("expected 82 peers, got %d", peers)
	}
}

func TestCLSyncing_WhenSynced(t *testing.T) {
	srv := makeCLServer(t, "/eth/v1/node/syncing", map[string]interface{}{
		"data": map[string]interface{}{
			"is_syncing":    false,
			"head_slot":     "8000000",
			"sync_distance": "0",
		},
	})
	defer srv.Close()

	c := ethclient.New("", srv.URL)
	syncing, err := c.CLSyncing(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if syncing {
		t.Error("expected syncing=false when CL is at head")
	}
}

func TestCLSyncing_WhenSyncing(t *testing.T) {
	srv := makeCLServer(t, "/eth/v1/node/syncing", map[string]interface{}{
		"data": map[string]interface{}{
			"is_syncing":    true,
			"head_slot":     "7500000",
			"sync_distance": "500000",
		},
	})
	defer srv.Close()

	c := ethclient.New("", srv.URL)
	syncing, err := c.CLSyncing(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !syncing {
		t.Error("expected syncing=true when CL is mid-sync")
	}
}

func TestCLPeerCount(t *testing.T) {
	srv := makeCLServer(t, "/eth/v1/node/peer_count", map[string]interface{}{
		"meta": map[string]interface{}{
			"count": "120",
		},
	})
	defer srv.Close()

	c := ethclient.New("", srv.URL)
	peers, err := c.CLPeerCount(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if peers != 120 {
		t.Errorf("expected 120 peers, got %d", peers)
	}
}

func TestELSyncing_ServerUnreachable(t *testing.T) {
	c := ethclient.New("http://127.0.0.1:19999", "")
	_, err := c.ELSyncing(context.Background())
	if err == nil {
		t.Error("expected error when server is unreachable")
	}
}
