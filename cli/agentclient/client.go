package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"io"
	"net/http"
	"time"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Client talks to an ethagent HTTP server.
type Client struct {
	baseURL string
	http    *http.Client
	node    string
	apiKey  string
}

// New returns a Client targeting the given agent URL.
func New(nodeName, host string, port int) *Client {
	return NewWithKey(nodeName, host, port, "")
}

// NewWithKey returns a Client with a Bearer token for authenticated endpoints.
// The API key is read from ETHAGENT_API_KEY env var if key is empty.
func NewWithKey(nodeName, host string, port int, key string) *Client {
	if key == "" {
		key = os.Getenv("ETHAGENT_API_KEY")
	}
	return &Client{
		node:    nodeName,
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
		http:    &http.Client{Timeout: 10 * time.Second},
		apiKey:  key,
	}
}

// NodeName returns the node name this client targets.
func (c *Client) NodeName() string {
	return c.node
}

// GetStatus returns the current status of the agent node.
func (c *Client) GetStatus(ctx context.Context) (*types.NodeStatus, error) {
	resp, err := c.get(ctx, "/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var status types.NodeStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	status.Name = c.node
	return &status, nil
}

// Cordon pauses reconciliation on the node.
func (c *Client) Cordon(ctx context.Context, reason string) error {
	return c.postJSON(ctx, "/cordon", types.CordonRequest{Cordoned: true, Reason: reason})
}

// Uncordon resumes reconciliation on the node.
func (c *Client) Uncordon(ctx context.Context) error {
	return c.postJSON(ctx, "/cordon", types.CordonRequest{Cordoned: false})
}

// TriggerReconcile forces an immediate reconcile cycle on the node.
func (c *Client) TriggerReconcile(ctx context.Context) (*types.ReconcileResult, error) {
	resp, err := c.post(ctx, "/reconcile", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result types.ReconcileResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode reconcile result: %w", err)
	}
	return &result, nil
}

// GetDiff returns the drift between desired and actual state.
func (c *Client) GetDiff(ctx context.Context) (*types.DiffResult, error) {
	resp, err := c.get(ctx, "/diff")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var diff types.DiffResult
	if err := json.NewDecoder(resp.Body).Decode(&diff); err != nil {
		return nil, fmt.Errorf("decode diff: %w", err)
	}
	return &diff, nil
}

// StreamLogs returns a reader for the specified client's log output.
func (c *Client) StreamLogs(ctx context.Context, client string, follow bool) (io.ReadCloser, error) {
	followStr := "false"
	if follow {
		followStr = "true"
	}
	url := fmt.Sprintf("%s/logs?client=%s&follow=%s", c.baseURL, client, followStr)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream logs from %s: %w", c.node, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("logs error from %s: status %d", c.node, resp.StatusCode)
	}
	return resp.Body, nil
}

// Restart restarts a specific client on the node.
func (c *Client) Restart(ctx context.Context, client string) error {
	resp, err := c.post(ctx, fmt.Sprintf("/restart?client=%s", client), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Healthz checks if the agent is alive.
func (c *Client) Healthz(ctx context.Context) error {
	resp, err := c.get(ctx, "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent %s unhealthy: status %d", c.node, resp.StatusCode)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", c.node, path, err)
	}
	return resp, nil
}

func (c *Client) post(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", c.node, path, err)
	}
	return resp, nil
}

func (c *Client) postJSON(ctx context.Context, path string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.node, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: status %d: %s", c.node, path, resp.StatusCode, string(body))
	}
	return nil
}
