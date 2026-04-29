package types

import "time"

// EthereumNode is the desired state of a single bare metal host.
type EthereumNode struct {
	Name     string            `yaml:"name"`
	Host     string            `yaml:"host"`
	Port     int               `yaml:"port,omitempty"`
	Labels   map[string]string `yaml:"labels,omitempty"`
	Profiles []string          `yaml:"profiles,omitempty"`
	Spec     NodeSpec          `yaml:"spec"`
}

// NodeSpec is the full desired state after profile merging.
type NodeSpec struct {
	System        SystemSpec        `yaml:"system"`
	Network       NetworkSpec       `yaml:"network"`
	Execution     ClientSpec        `yaml:"execution"`
	Consensus     ClientSpec        `yaml:"consensus"`
	MEV           MEVSpec           `yaml:"mev"`
	Observability ObservabilitySpec `yaml:"observability"`
	Maintenance   MaintenanceSpec   `yaml:"maintenance"`
	Snapshot      SnapshotSpec      `yaml:"snapshot"`
}

// SnapshotSpec controls whether and how to restore a client snapshot on first boot.
// The operator checks if the datadir is empty before starting the client — if it is,
// it downloads and extracts the latest snapshot from the configured provider.
type SnapshotSpec struct {
	Enabled     bool   `yaml:"enabled"`
	// Provider: "ethpandaops" (default) or "self-hosted"
	Provider    string `yaml:"provider"`
	Network     string `yaml:"network"`     // mainnet | sepolia | hoodi | holesky
	ELEnabled   bool   `yaml:"elEnabled"`   // download EL snapshot
	CLEnabled   bool   `yaml:"clEnabled"`   // download CL snapshot
	// BlockNumber pins a specific snapshot. Leave empty to use latest.
	BlockNumber string `yaml:"blockNumber,omitempty"`
	// URL is the base URL of a self-hosted snapshot server.
	// Only used when Provider == "self-hosted".
	// Format: http://snapshots.internal:8888
	// The server must implement the ethpandaops-compatible API:
	//   GET /{network}/{client}/latest  → block number
	//   GET /{network}/{client}/{block}/snapshot.tar.zst → archive
	URL         string `yaml:"url,omitempty"`
}


// SystemSpec describes OS-level configuration.
type SystemSpec struct {
	Packages []PackageSpec `yaml:"packages"`
	Kernel   KernelSpec    `yaml:"kernel"`
	Disk     DiskSpec      `yaml:"disk"`
}

type PackageSpec struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type KernelSpec struct {
	Parameters map[string]string `yaml:"parameters"`
}

// DiskSpec describes how to discover, assemble, format, and mount data disks.
// The agent applies this idempotently — safe to run on an already-provisioned node.
type DiskSpec struct {
	// Devices: "auto" discovers all unpartitioned block devices,
	// or an explicit list like [/dev/nvme0n1, /dev/nvme1n1].
	Devices []string `yaml:"devices"` // empty = auto-discover

	// RaidLevel: 0 = RAID0 stripe (default for multi-disk), -1 = no RAID.
	// Ignored when only one device is present.
	RaidLevel int `yaml:"raidLevel"`

	// ArrayDevice is the resulting md device, e.g. /dev/md0.
	// Only used when RaidLevel >= 0 and multiple devices exist.
	ArrayDevice string `yaml:"arrayDevice"` // default: /dev/md0

	MountPath string   `yaml:"mountPath"` // e.g. /data
	FsType    string   `yaml:"fsType"`    // ext4 | xfs
	Options   []string `yaml:"options"`   // e.g. [noatime, nodiratime]
}

// NetworkSpec describes firewall and DNS configuration.
type NetworkSpec struct {
	DNS        DNSSpec        `yaml:"dns"`
	Firewall   FirewallSpec   `yaml:"firewall"`
	Cloudflare CloudflareSpec `yaml:"cloudflare"`
}

// CloudflareSpec configures the Cloudflare Tunnel and DNS for this node.
// cloudflared creates an outbound-only tunnel — no inbound ports needed.
// DNS records are provisioned automatically via the Cloudflare API.
type CloudflareSpec struct {
	// AccountID is the Cloudflare account ID.
	AccountID string `yaml:"accountId"`
	// ZoneID is the Cloudflare DNS zone ID.
	ZoneID string `yaml:"zoneId"`
	// Domain is the base domain, e.g. "validators.example.com".
	Domain string `yaml:"domain"`
	// NodeSubdomain is the A-record subdomain for this node.
	// Result: {NodeSubdomain}.{Domain} → node IP
	// Example: "bare-metal-01" → "bare-metal-01.validators.example.com"
	NodeSubdomain string `yaml:"nodeSubdomain"`
	// CLSubdomain is the CNAME for the beacon node tunnel endpoint.
	// Result: {CLSubdomain}.{Domain} → cloudflare tunnel
	// Example: "bare-metal-01-cl" → "bare-metal-01-cl.validators.example.com"
	CLSubdomain string `yaml:"clSubdomain"`
	// TunnelName is the Cloudflare Tunnel name (created if not exists).
	TunnelName string `yaml:"tunnelName"`
	// AccessPolicy restricts who can reach the CL endpoint via Cloudflare Access.
	// "service-token" (recommended) or "ip-policy"
	AccessPolicy string `yaml:"accessPolicy"`
}

type DNSSpec struct {
	Nameservers   []string `yaml:"nameservers"`
	SearchDomains []string `yaml:"searchDomains"`
}

type FirewallSpec struct {
	Provider string         `yaml:"provider"` // ufw
	Policy   string         `yaml:"policy"`   // deny-by-default | allow-by-default
	Rules    []FirewallRule `yaml:"rules"`
}

type FirewallRule struct {
	Description string `yaml:"description"`
	Port        int    `yaml:"port"`
	Proto       string `yaml:"proto"`      // tcp | udp | any
	Direction   string `yaml:"direction"`  // inbound | outbound
	Source      string `yaml:"source,omitempty"`
	Destination string `yaml:"destination,omitempty"`
	Action      string `yaml:"action"` // allow | deny
}


// ClientSpec describes an EL or CL client container.
type ClientSpec struct {
	Client    string            `yaml:"client"`
	Image     string            `yaml:"image"`
	DataDir   string            `yaml:"dataDir"`
	Flags     map[string]string `yaml:"flags"`
	Resources ResourceSpec      `yaml:"resources"`
	Ports     ClientPorts       `yaml:"ports"`
}

type ResourceSpec struct {
	CPUCores int `yaml:"cpuCores"`
	MemoryGB int `yaml:"memoryGB"`
}

type ClientPorts struct {
	HTTP    int `yaml:"http"`
	P2P     int `yaml:"p2p"`
	Metrics int `yaml:"metrics"`
}

// MEVSpec describes mev-boost configuration.
type MEVSpec struct {
	Enabled    bool       `yaml:"enabled"`
	Image      string     `yaml:"image"`
	ListenAddr string     `yaml:"listenAddr"`
	Relays     []MEVRelay `yaml:"relays"`
}

type MEVRelay struct {
	URL          string `yaml:"url"`
	Label        string `yaml:"label"`
	OFACFiltered bool   `yaml:"ofacFiltered"`
}

// ObservabilitySpec describes metrics, logging, and the on-host stack.
type ObservabilitySpec struct {
	Metrics      MetricsSpec  `yaml:"metrics"`
	Logs         LogsSpec     `yaml:"logs"`
	// StackDir is the directory on-host where the observability docker-compose
	// stack lives. The agent reconciles this stack (pull + up -d) as part of
	// the normal reconcile loop. No Ansible needed.
	StackDir     string       `yaml:"stackDir"`     // default: /opt/eth-observability
	// EnvFile is the path to the .env file for the observability stack.
	EnvFile      string       `yaml:"envFile"`      // default: /opt/eth-observability/.env
}

type MetricsSpec struct {
	Enabled        bool           `yaml:"enabled"`
	Exporters      []ExporterSpec `yaml:"exporters"`
	ScrapeInterval string         `yaml:"scrapeInterval"`
	RemoteWrite    []RemoteWrite  `yaml:"remoteWrite"`
}

type ExporterSpec struct {
	Name        string `yaml:"name"`
	Port        int    `yaml:"port"`
	Path        string `yaml:"path"`
	Image       string `yaml:"image,omitempty"`
	Description string `yaml:"description,omitempty"`
}

type RemoteWrite struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret,omitempty"`
}

type LogsSpec struct {
	Provider     string           `yaml:"provider"`
	Destinations []LogDestination `yaml:"destinations"`
}

type LogDestination struct {
	Type    string `yaml:"type"`
	URL     string `yaml:"url"`
	Enabled bool   `yaml:"enabled"`
}

// MaintenanceSpec controls upgrade scheduling and strategy.
type MaintenanceSpec struct {
	Window          MaintenanceWindow `yaml:"window"`
	UpgradeStrategy UpgradeStrategy   `yaml:"upgradeStrategy"`
	AutoUpgrade     AutoUpgradeSpec   `yaml:"autoUpgrade"`
}

type MaintenanceWindow struct {
	Schedule string `yaml:"schedule"` // cron
}

type UpgradeStrategy struct {
	Order           []string `yaml:"order"`           // [cl, mev, el]
	PreflightChecks []string `yaml:"preflightChecks"`
}

type AutoUpgradeSpec struct {
	Enabled  bool   `yaml:"enabled"`
	EL       string `yaml:"el"`       // none | patch | minor
	CL       string `yaml:"cl"`
	Packages string `yaml:"packages"` // security-only | all | none
}

// NodeStatus is the actual observed state of a node.
type NodeStatus struct {
	Name       string       `json:"name"`
	Host       string       `json:"host"`
	Phase      Phase        `json:"phase"`
	EL         ClientStatus `json:"el"`
	CL         ClientStatus `json:"cl"`
	MEV        ClientStatus `json:"mev"`
	System     SystemStatus `json:"system"`
	Disk       DiskStatus   `json:"disk"`
	Cordoned   bool         `json:"cordoned"`
	ReportedAt time.Time    `json:"reportedAt"`
	Conditions []Condition  `json:"conditions"`
}

type Phase string

const (
	PhaseRunning   Phase = "Running"
	PhaseUpgrading Phase = "Upgrading"
	PhaseDegraded  Phase = "Degraded"
	PhasePending   Phase = "Pending"
	PhaseCordoned  Phase = "Cordoned"
)

type ClientStatus struct {
	Name        string `json:"name"`
	Running     bool   `json:"running"`
	Image       string `json:"image"`
	Version     string `json:"version"`
	Synced      bool   `json:"synced"`
	BlockNumber uint64 `json:"blockNumber"`
	PeerCount   int    `json:"peerCount"`
	Healthy     bool   `json:"healthy"`
}

type SystemStatus struct {
	Hostname    string  `json:"hostname"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemUsedGB   float64 `json:"memUsedGB"`
	MemTotalGB  float64 `json:"memTotalGB"`
	DiskUsedGB  float64 `json:"diskUsedGB"`
	DiskFreeGB  float64 `json:"diskFreeGB"`
	UptimeHours float64 `json:"uptimeHours"`
	KernelVer   string  `json:"kernelVersion"`
}

// DiskStatus reports the current state of data disks.
type DiskStatus struct {
	MountPath   string   `json:"mountPath"`
	Mounted     bool     `json:"mounted"`
	FsType      string   `json:"fsType"`
	TotalGB     float64  `json:"totalGB"`
	UsedGB      float64  `json:"usedGB"`
	FreeGB      float64  `json:"freeGB"`
	Devices     []string `json:"devices"`
	RaidLevel   int      `json:"raidLevel"`
	ArrayDevice string   `json:"arrayDevice,omitempty"`
}

type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// CordonRequest is the payload to cordon/uncordon a node.
type CordonRequest struct {
	Cordoned bool   `json:"cordoned"`
	Reason   string `json:"reason,omitempty"`
}

// ReconcileResult is the outcome of a single reconciliation cycle.
type ReconcileResult struct {
	NodeName string        `json:"nodeName"`
	Actions  []string      `json:"actions"`
	Errors   []string      `json:"errors"`
	Duration time.Duration `json:"duration"`
}

// DiffResult describes drift between desired and actual state.
type DiffResult struct {
	NodeName string      `json:"nodeName"`
	Drifts   []DriftItem `json:"drifts"`
	InSync   bool        `json:"inSync"`
}

type DriftItem struct {
	Field   string `json:"field"`
	Desired string `json:"desired"`
	Actual  string `json:"actual"`
}

// UpgradeRequest is issued by the CLI to trigger a rolling upgrade.
type UpgradeRequest struct {
	Nodes          []string `json:"nodes"`
	ELImage        string   `json:"elImage,omitempty"`
	CLImage        string   `json:"clImage,omitempty"`
	MEVImage       string   `json:"mevImage,omitempty"`
	MaxUnavailable int      `json:"maxUnavailable"`
	SkipPreflight  bool     `json:"skipPreflight"`
}

// StandardPorts defines the canonical port assignments used across all
// client implementations. Profiles enforce these via client flags so
// Alloy, firewall rules, and ethctl can use hardcoded port constants.
type StandardPorts struct{}

const (
	// Execution layer
	ELHTTPPort    = 8545
	ELWSPort      = 8546
	ELAuthRPCPort = 8551
	ELMetricsPort = 6060
	ELP2PPort     = 30303

	// Consensus layer (all clients normalised to these)
	CLHTTPPort    = 5052
	CLP2PPort     = 9000
	CLMetricsPort = 5054


	// MEV-Boost
	MEVPort = 18550

	// ethagent
	AgentPort = 19000
)

// Profile is a reusable partial NodeSpec.
type Profile struct {
	Name string   `yaml:"name"`
	Spec NodeSpec `yaml:"spec"`
}

// Cluster is the top-level inventory unit.
// One cluster file describes all nodes that share a set of profiles.
// This scales to hundreds of nodes — each node entry is minimal.
type Cluster struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description,omitempty"`
	Profiles    []string      `yaml:"profiles"`           // applied to all nodes
	DefaultPort int           `yaml:"defaultPort"`        // default agent port
	Nodes       []ClusterNode `yaml:"nodes"`
}

// ClusterNode is a minimal per-node entry inside a Cluster.
// Only host-specific fields live here; everything else comes from profiles.
type ClusterNode struct {
	Name     string            `yaml:"name"`
	Host     string            `yaml:"host"`
	Port     int               `yaml:"port,omitempty"`  // overrides cluster defaultPort
	Labels   map[string]string `yaml:"labels,omitempty"`
	Profiles []string          `yaml:"profiles,omitempty"` // additional profiles (additive)
	Spec     NodeSpec          `yaml:"spec,omitempty"`     // node-level overrides
	Disabled bool              `yaml:"disabled,omitempty"` // skip this node
}
