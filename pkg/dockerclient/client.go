package dockerclient

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/enriquemanuel/eth-node-operator/pkg/types"
)

// Commander abstracts exec.Command for testing.
type Commander interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// RealCommander runs actual docker commands.
type RealCommander struct{}

func (r *RealCommander) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Client manages Docker containers on the local host.
type Client struct {
	cmd Commander
}

// New returns a Client using real docker commands.
func New() *Client {
	return &Client{cmd: &RealCommander{}}
}

// NewWithCommander returns a Client using a custom Commander (for testing).
func NewWithCommander(cmd Commander) *Client {
	return &Client{cmd: cmd}
}

// ContainerInfo holds the inspect result we care about.
type ContainerInfo struct {
	Name    string
	Running bool
	Image   string
	Version string
	StartedAt time.Time
}

// Inspect returns runtime info for a named container.
func (c *Client) Inspect(ctx context.Context, name string) (*ContainerInfo, error) {
	out, err := c.cmd.Run(ctx,
		"inspect", name,
		"--format", "{{.State.Running}}|{{.Config.Image}}|{{.State.StartedAt}}",
	)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", name, err)
	}

	parts := strings.SplitN(out, "|", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("unexpected inspect output: %q", out)
	}

	running := parts[0] == "true"
	image := parts[1]
	version := extractVersion(image)

	startedAt, _ := time.Parse(time.RFC3339Nano, parts[2])

	return &ContainerInfo{
		Name:      name,
		Running:   running,
		Image:     image,
		Version:   version,
		StartedAt: startedAt,
	}, nil
}

// Pull pulls a docker image.
func (c *Client) Pull(ctx context.Context, image string) error {
	_, err := c.cmd.Run(ctx, "pull", image)
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

// Stop stops a named container gracefully.
func (c *Client) Stop(ctx context.Context, name string) error {
	_, err := c.cmd.Run(ctx, "stop", name)
	if err != nil {
		return fmt.Errorf("stop %s: %w", name, err)
	}
	return nil
}

// Start starts a named container.
func (c *Client) Start(ctx context.Context, name string) error {
	_, err := c.cmd.Run(ctx, "start", name)
	if err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	return nil
}

// Restart restarts a named container.
func (c *Client) Restart(ctx context.Context, name string) error {
	_, err := c.cmd.Run(ctx, "restart", name)
	if err != nil {
		return fmt.Errorf("restart %s: %w", name, err)
	}
	return nil
}

// UpdateImage stops the container, updates its image, and recreates it.
// For eth-docker managed containers this calls docker compose up -d.
func (c *Client) UpdateImage(ctx context.Context, name, newImage string) error {
	if err := c.Pull(ctx, newImage); err != nil {
		return err
	}
	if err := c.Stop(ctx, name); err != nil {
		return err
	}
	_, err := c.cmd.Run(ctx, "run", "-d", "--name", name, newImage)
	if err != nil {
		return fmt.Errorf("recreate %s with %s: %w", name, newImage, err)
	}
	return nil
}

// Logs streams logs from a named container. Caller is responsible for closing the reader.
func (c *Client) Logs(ctx context.Context, name string, follow bool) (io.ReadCloser, error) {
	args := []string{"logs", name}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, "--timestamps")

	cmd := exec.CommandContext(ctx, "docker", args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start log stream for %s: %w", name, err)
	}

	go func() {
		cmd.Wait()
		pw.Close()
	}()

	return pr, nil
}

// ListRunning returns names of all running containers.
func (c *Client) ListRunning(ctx context.Context) ([]string, error) {
	out, err := c.cmd.Run(ctx, "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ApplyComposeUpdate runs docker compose pull + up -d in the given directory.
func (c *Client) ApplyComposeUpdate(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "docker", "compose", "pull")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compose pull: %s: %w", string(out), err)
	}

	cmd = exec.CommandContext(ctx, "docker", "compose", "up", "-d", "--remove-orphans")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compose up: %s: %w", string(out), err)
	}
	return nil
}

// InspectAll returns ContainerInfo for a set of well-known eth-docker containers.
func (c *Client) InspectAll(ctx context.Context, names []string) map[string]*ContainerInfo {
	result := make(map[string]*ContainerInfo, len(names))
	for _, name := range names {
		info, err := c.Inspect(ctx, name)
		if err != nil {
			result[name] = &ContainerInfo{Name: name, Running: false}
			continue
		}
		result[name] = info
	}
	return result
}

// extractVersion pulls the tag from an image string like ethereum/client-go:v1.14.8
func extractVersion(image string) string {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return "unknown"
}

// ContainerNames returns the docker container names for a given NodeSpec.
func ContainerNames(spec types.NodeSpec) map[string]string {
	return map[string]string{
		"el":  containerName(spec.Execution.Client),
		"cl":  containerName(spec.Consensus.Client),
		"mev": "mev-boost",
	}
}

func containerName(client string) string {
	switch strings.ToLower(client) {
	case "geth":
		return "execution"
	case "nethermind":
		return "execution"
	case "besu":
		return "execution"
	case "lighthouse":
		return "consensus"
	case "teku":
		return "consensus"
	case "prysm":
		return "consensus"
	default:
		return client
	}
}
