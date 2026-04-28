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
}

// SystemSpec describes OS-level configuration.
type SystemSpec struct {
	Packages []PackageSpec     `yaml:"packages"`
	Kernel   KernelSpec        `yaml:"kernel"`
	Disk     []DiskSpec        `yaml:"disk"`
}

type PackageSpec struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type KernelSpec struct {
	Parameters map[string]string `yaml:"parameters"`
}

type DiskSpec struct {
	Device    string   `yaml:"device"`
	MountPath string   `yaml:"mountPath"`
	FsType    string   `yaml:"fsType"`
	Options   []string `yaml:"options"`
}

// NetworkSpec describes firewall and DNS configuration.
type NetworkSpec struct {
	DNS      DNSSpec       `yaml:"dns"`
	Firewall FirewallSpec  `yaml:"firewall"`
	TLS      TLSSpec       `yaml:"tls"`
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
	Proto       string `yaml:"proto"` // tcp | udp | any
	Direction   string `yaml:"direction"` // inbound | outbound
	Source      string `yaml:"source,omitempty"`
	Destination string `yaml:"destination,omitempty"`
	Action      string `yaml:"action"` // allow | deny
}

type TLSSpec struct {
	Provider string   `yaml:"provider"`
	Issuer   string   `yaml:"issuer"`
	Domains  []string `yaml:"domains"`
}

// ClientSpec describes an EL or CL client container.
type ClientSpec struct {
	Client  string            `yaml:"client"`
	Image   string            `yaml:"image"`
	DataDir string            `yaml:"dataDir"`
	Flags   map[string]string `yaml:"flags"`
	Resources ResourceSpec    `yaml:"resources"`
	Ports   ClientPorts       `yaml:"ports"`
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

// ObservabilitySpec describes metrics and logging.
type ObservabilitySpec struct {
	Metrics       MetricsSpec `yaml:"metrics"`
	Logs          LogsSpec    `yaml:"logs"`
	AlloyConfig   string      `yaml:"alloyConfig,omitempty"`   // path to alloy config on host
	ObsStackDir   string      `yaml:"obsStackDir,omitempty"`   // dir for observability compose stack
}

type MetricsSpec struct {
	Enabled       bool            `yaml:"enabled"`
	Exporters     []ExporterSpec  `yaml:"exporters"`
	ScrapeInterval string         `yaml:"scrapeInterval"`
	RemoteWrite   []RemoteWrite   `yaml:"remoteWrite"`
}

type ExporterSpec struct {
	Name  string `yaml:"name"`
	Port  int    `yaml:"port"`
	Path  string `yaml:"path"`
	Image string `yaml:"image,omitempty"`
}

type RemoteWrite struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret,omitempty"`
}

type LogsSpec struct {
	Provider     string            `yaml:"provider"`
	Destinations []LogDestination  `yaml:"destinations"`
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
	Order          []string `yaml:"order"` // [cl, mev, el]
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
	Name       string        `json:"name"`
	Host       string        `json:"host"`
	Phase      Phase         `json:"phase"`
	EL         ClientStatus  `json:"el"`
	CL         ClientStatus  `json:"cl"`
	MEV        ClientStatus  `json:"mev"`
	System     SystemStatus  `json:"system"`
	Cordoned   bool          `json:"cordoned"`
	ReportedAt time.Time     `json:"reportedAt"`
	Conditions []Condition   `json:"conditions"`
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
	Field    string `json:"field"`
	Desired  string `json:"desired"`
	Actual   string `json:"actual"`
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

// Profile is a reusable partial NodeSpec.
type Profile struct {
	Name string   `yaml:"name"`
	Spec NodeSpec `yaml:"spec"`
}
