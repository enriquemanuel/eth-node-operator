package ethclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client speaks to an Ethereum execution or consensus layer over HTTP.
type Client struct {
	elURL  string
	clURL  string
	http   *http.Client
}

// New returns a Client targeting the given EL and CL endpoints.
func New(elURL, clURL string) *Client {
	return &Client{
		elURL: elURL,
		clURL: clURL,
		http: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) call(ctx context.Context, url, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	})
	if err != nil {
		return fmt.Errorf("marshal rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc call to %s: %w", url, err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return json.Unmarshal(rpcResp.Result, out)
}

// ELSyncing returns true if the execution layer is still syncing.
func (c *Client) ELSyncing(ctx context.Context) (bool, error) {
	var result json.RawMessage
	if err := c.call(ctx, c.elURL, "eth_syncing", nil, &result); err != nil {
		return false, err
	}
	// eth_syncing returns false when synced, object when syncing
	return string(result) != "false", nil
}

// ELBlockNumber returns the current block number from the execution layer.
func (c *Client) ELBlockNumber(ctx context.Context) (uint64, error) {
	var hexBlock string
	if err := c.call(ctx, c.elURL, "eth_blockNumber", nil, &hexBlock); err != nil {
		return 0, err
	}
	var n uint64
	fmt.Sscanf(hexBlock, "0x%x", &n)
	return n, nil
}

// ELPeerCount returns the number of peers connected to the execution layer.
func (c *Client) ELPeerCount(ctx context.Context) (int, error) {
	var hexCount string
	if err := c.call(ctx, c.elURL, "net_peerCount", nil, &hexCount); err != nil {
		return 0, err
	}
	var n int
	fmt.Sscanf(hexCount, "0x%x", &n)
	return n, nil
}

type clSyncResp struct {
	Data struct {
		IsSyncing    bool   `json:"is_syncing"`
		HeadSlot     string `json:"head_slot"`
		SyncDistance string `json:"sync_distance"`
	} `json:"data"`
}

// CLSyncing returns true if the consensus layer is still syncing.
func (c *Client) CLSyncing(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.clURL+"/eth/v1/node/syncing", nil)
	if err != nil {
		return false, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("cl syncing request: %w", err)
	}
	defer resp.Body.Close()

	var sync clSyncResp
	if err := json.NewDecoder(resp.Body).Decode(&sync); err != nil {
		return false, fmt.Errorf("decode cl sync response: %w", err)
	}
	return sync.Data.IsSyncing, nil
}

type clPeerResp struct {
	Meta struct {
		Count string `json:"count"`
	} `json:"meta"`
}

// CLPeerCount returns the number of peers connected to the consensus layer.
func (c *Client) CLPeerCount(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.clURL+"/eth/v1/node/peer_count", nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cl peer count request: %w", err)
	}
	defer resp.Body.Close()

	var peer clPeerResp
	if err := json.NewDecoder(resp.Body).Decode(&peer); err != nil {
		return 0, fmt.Errorf("decode cl peer response: %w", err)
	}

	var n int
	fmt.Sscanf(peer.Meta.Count, "%d", &n)
	return n, nil
}

// CLHeadSlot returns the current head slot from the consensus layer.
func (c *Client) CLHeadSlot(ctx context.Context) (uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.clURL+"/eth/v1/node/syncing", nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cl head slot request: %w", err)
	}
	defer resp.Body.Close()

	var sync clSyncResp
	if err := json.NewDecoder(resp.Body).Decode(&sync); err != nil {
		return 0, fmt.Errorf("decode cl sync response: %w", err)
	}

	var slot uint64
	fmt.Sscanf(sync.Data.HeadSlot, "%d", &slot)
	return slot, nil
}
