# eth-node-operator

GitOps-style operator for managing Ethereum bare metal nodes. Replaces manual Ansible/SSH workflows with a pull-based agent that continuously reconciles desired state against actual state.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                  Git / Inventory YAML                    │
│  inventory/nodes/*.yaml  +  inventory/profiles/*.yaml   │
└───────────────────────────┬─────────────────────────────┘
                            │ desired state (YAML)
                            ▼
┌─────────────────────────────────────────────────────────┐
│                    ethctl (CLI)                          │
│  nodes list | describe | cordon | sync | diff | upgrade  │
└───────────────────────────┬─────────────────────────────┘
                            │ HTTP REST
              ┌─────────────┴──────────────┐
              ▼                            ▼
   ┌─────────────────┐          ┌─────────────────┐
   │  ethagent       │          │  ethagent        │
   │  bare-metal-01    │          │  bare-metal-02     │
   │  :9000          │          │  :9000           │
   │                 │          │                  │
   │  reconciler     │          │  reconciler      │
   │  docker client  │          │  docker client   │
   │  ufw manager    │          │  ufw manager     │
   │  sys collector  │          │  sys collector   │
   └────────┬────────┘          └────────┬─────────┘
            │                            │
     ┌──────▼──────┐              ┌──────▼──────┐
     │ eth-docker  │              │ eth-docker  │
     │ geth        │              │ nethermind  │
     │ lighthouse  │              │ teku        │
     │ mev-boost   │              │ mev-boost   │
     └─────────────┘              └─────────────┘
```

## Components

| Component | Description |
|-----------|-------------|
| `ethagent` | Lightweight binary deployed on each bare metal host. Runs a 30s reconcile loop + HTTP API. |
| `ethctl` | CLI for operators. Inspect nodes, cordon, sync, stream logs, trigger upgrades. |
| `inventory/` | YAML files are the source of truth. Profile merging → per-node overrides. |

## Quick Start

### Bootstrap a new node

```bash
# 1. Copy your node spec
cp inventory/nodes/bare-metal-01.yaml inventory/nodes/your-node.yaml
# edit: name, host, profiles, any overrides

# 2. Bootstrap with Ansible (one-time)
ansible-playbook -i hosts.ini deploy/ansible/bootstrap.yaml --limit your-node

# 3. Verify
ethctl nodes list
```

### ethctl usage

```bash
# List all nodes with sync status
ethctl nodes list

# Describe a single node in detail
ethctl nodes describe bare-metal-01

# Force reconcile (apply desired state immediately)
ethctl sync bare-metal-01

# Show drift between desired and actual state
ethctl diff bare-metal-01

# Stream logs from the EL
ethctl logs bare-metal-01 --client el --follow

# Restart the CL on a node
ethctl restart bare-metal-01 --client cl

# Cordon a node (pause reconciliation for maintenance)
ethctl cordon bare-metal-01 --reason "disk replacement"
ethctl uncordon bare-metal-01

# Rolling upgrade (update inventory YAML first, then run)
ethctl upgrade --el ethereum/client-go:v1.14.9
ethctl upgrade bare-metal-01 bare-metal-02 --cl sigp/lighthouse:v5.4.0
```

## Inventory Structure

```
inventory/
├── nodes/
│   ├── bare-metal-01.yaml    ← per-node spec + profile references
│   └── bare-metal-02.yaml
├── profiles/
│   ├── mainnet-base.yaml   ← shared EL/CL images, kernel params, firewall
│   ├── observability-full.yaml  ← Prometheus exporters, Loki/Datadog shipping
│   └── mev-standard.yaml   ← mev-boost + relay list
└── relays/
    └── flashbots-mainnet.yaml
```

### Profile merging

Later profiles win over earlier ones. Node-level spec always wins over profiles.

```yaml
# bare-metal-01.yaml
profiles:
  - mainnet-base         # sets geth:v1.14.8, lighthouse:v5.3.0, firewall rules
  - observability-full   # adds metrics exporters, log destinations
  - mev-standard         # enables mev-boost with 5 relays

spec:
  # Node overrides — only what differs from profiles
  execution:
    flags:
      cache: "8192"      # this host has more RAM
```

## Reconciliation Loop

`ethagent` runs every 30 seconds:

1. **Load spec** from `/etc/ethagent/node.yaml`
2. **Skip if cordoned** (manual maintenance)
3. **Check maintenance window** (cron schedule)
4. **Reconcile execution client** — image drift → pull + stop + start
5. **Reconcile consensus client** — same
6. **Reconcile MEV boost** — same
7. **Reconcile firewall** — UFW drift check → reset + reapply rules
8. **Log actions taken**

## Agent HTTP API

Each `ethagent` exposes:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Liveness check |
| `/status` | GET | Full node status JSON |
| `/cordon` | POST | Pause/resume reconciliation |
| `/reconcile` | POST | Trigger immediate reconcile |
| `/diff` | GET | Show desired vs actual drift |
| `/logs?client=el\|cl\|mev&follow=true` | GET | Stream container logs |
| `/restart?client=el\|cl\|mev` | POST | Restart a container |

## Configuration

### ethagent flags

```
--node-name   NODE_NAME env    Node identifier (default: hostname)
--spec        SPEC_PATH env    Path to node YAML (default: /etc/ethagent/node.yaml)
--listen      LISTEN_ADDR env  HTTP listen addr (default: :9000)
--el-endpoint EL_ENDPOINT env  EL JSON-RPC URL (default: http://localhost:8545)
--cl-endpoint CL_ENDPOINT env  CL REST API URL (default: http://localhost:5052)
--log-level   LOG_LEVEL env    debug|info|warn|error (default: info)
```

### ethctl flags

```
--inventory (-i)  Path to nodes directory (default: inventory/nodes)
--port            Agent HTTP port (default: 9000)
```

## Development

```bash
# Run all tests
make test

# Run with race detector
make test-race

# Generate coverage report
make test-cover

# Build binaries
make build

# Cross-compile for linux/amd64 (for deployment)
make release-linux

# Run agent locally (reads inventory/nodes/bare-metal-01.yaml)
make run-agent
```

## Deployment

```bash
# Build the linux binary
make release-linux

# Deploy to a host
ansible-playbook -i hosts.ini deploy/ansible/bootstrap.yaml --limit bare-metal-01
```

The Ansible playbook:
- Copies the `ethagent` binary to `/usr/local/bin/`
- Copies the node spec to `/etc/ethagent/node.yaml`
- Installs and enables the systemd service
- Waits for `/healthz` to respond

## Security

- `ethagent` runs as root (required for UFW and Docker)
- Bind `--listen` to `0.0.0.0:9000` only if the port is firewalled to trusted sources
- For mTLS between `ethctl` and agents, use a reverse proxy (Caddy, Nginx) with client cert validation in front of `:9000`
- The UFW default policy is `deny-by-default`; only ports explicitly listed in `spec.network.firewall.rules` are opened

## Roadmap

- [ ] mTLS agent authentication (cert-manager issued certs)
- [ ] Kubernetes operator + CRDs for managing the inventory from EKS
- [ ] Auto-upgrade via image digest polling (patch-only mode)
- [ ] Slack/PagerDuty alerting on reconcile failures
- [ ] Web dashboard (read-only node status view)
