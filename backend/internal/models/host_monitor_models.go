package models

import "time"

// HostMetricsResponse is the comprehensive host monitoring response
// covering all resource dimensions: system, CPU, memory, network, disk,
// accelerators (GPU/NPU), processes, docker, and logged-in users.
type HostMetricsResponse struct {
	System          SystemInfo        `json:"system"`
	CPU             CPUInfo           `json:"cpu"`
	Load            LoadInfo          `json:"load"`
	Memory          MemoryInfo        `json:"memory"`
	Swap            SwapInfo          `json:"swap"`
	Networks        []NetworkInfo     `json:"networks"`
	NetworkSummary  NetworkSummary    `json:"network_summary"`
	Disks           []HostDiskInfo    `json:"disks"`
	DiskGroups      []DiskGroup       `json:"disk_groups"`
	GPU             *GPUInfo          `json:"gpu,omitempty"`
	NPU             *NPUInfo          `json:"npu,omitempty"`
	Processes       []ProcessInfo     `json:"processes,omitempty"`
	Docker          *DockerInfo       `json:"docker,omitempty"`
	Users           []UserInfo        `json:"users,omitempty"`
	History         MetricsHistory    `json:"history"`
	SampledAt       time.Time         `json:"sampled_at"`
	ExtendedCachedAt *time.Time       `json:"extended_cached_at,omitempty"` // When extended data (GPU/Docker/NPU/Users) was last refreshed; nil if not cached
}

// SystemInfo contains basic system identification.
type SystemInfo struct {
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Kernel    string `json:"kernel"`
	UptimeSec uint64 `json:"uptime_sec"`
}

// CPUInfo contains CPU usage and topology information.
type CPUInfo struct {
	Model        string      `json:"model"`
	Cores        uint32      `json:"cores"`
	Threads      uint32      `json:"threads"`
	UsagePercent float64     `json:"usage_percent"`
	PerCore      []CoreUsage `json:"per_core,omitempty"`
	UsageSource  string      `json:"usage_source"` // "aggregate" | "core_weighted" | "warming_up"
}

// CoreUsage represents usage percentage for a single CPU core.
type CoreUsage struct {
	ID    uint32  `json:"id"`
	Usage float64 `json:"usage"`
}

// LoadInfo contains system load averages.
type LoadInfo struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// MemoryInfo contains detailed memory usage information.
type MemoryInfo struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	CachedBytes    uint64  `json:"cached_bytes"`
	BuffersBytes   uint64  `json:"buffers_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
}

// SwapInfo contains swap usage information.
type SwapInfo struct {
	TotalBytes   uint64  `json:"total_bytes"`
	UsedBytes    uint64  `json:"used_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

// NetworkInfo contains per-interface network statistics.
type NetworkInfo struct {
	Name          string  `json:"name"`
	State         string  `json:"state"` // "up" | "down"
	IPAddress     string  `json:"ip_address,omitempty"`
	MACAddress    string  `json:"mac_address,omitempty"`
	Virtual       bool    `json:"virtual"` // true if virtual interface
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
	RxTotalBytes  uint64  `json:"rx_total_bytes"`
	TxTotalBytes  uint64  `json:"tx_total_bytes"`
}

// NetworkSummary contains aggregate network statistics.
type NetworkSummary struct {
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

// HostDiskInfo contains per-partition disk statistics.
type HostDiskInfo struct {
	Device        string  `json:"device"`
	Mount         string  `json:"mount"`
	FSType        string  `json:"fs_type"`
	TotalBytes    uint64  `json:"total_bytes"`
	UsedBytes     uint64  `json:"used_bytes"`
	AvailBytes    uint64  `json:"avail_bytes"`
	UsagePercent  float64 `json:"usage_percent"`
	ReadBytesSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesSec float64 `json:"write_bytes_per_sec"`
}

// DiskGroup represents a physical disk with one or more partitions.
// Structure: Physical Disk → Partitions → Mount Points
type DiskGroup struct {
	Device        string       `json:"device"`         // Physical disk name (e.g., "/dev/sda")
	TotalBytes    uint64       `json:"total_bytes"`    // Sum of all partition totals
	UsedBytes     uint64       `json:"used_bytes"`     // Sum of all partition usage
	UsagePercent  float64      `json:"usage_percent"`  // Calculated from totals
	ReadBytesSec  float64      `json:"read_bytes_per_sec"`
	WriteBytesSec float64      `json:"write_bytes_per_sec"`
	Partitions    []Partition  `json:"partitions"`     // Partitions on this disk
}

// Partition represents a disk partition with one or more mount points.
type Partition struct {
	Device       string       `json:"device"`        // Partition device (e.g., "/dev/sda1")
	TotalBytes   uint64       `json:"total_bytes"`
	UsedBytes    uint64       `json:"used_bytes"`
	UsagePercent float64      `json:"usage_percent"`
	ReadBytesSec float64      `json:"read_bytes_per_sec"`
	WriteBytesSec float64     `json:"write_bytes_per_sec"`
	Mounts       []MountPoint `json:"mounts"`        // Mount points for this partition
}

// MountPoint represents a single mount point.
type MountPoint struct {
	Path       string  `json:"path"`        // Mount point path (e.g., "/", "/boot")
	FSType     string  `json:"fs_type"`     // Filesystem type
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

// GPUInfo contains NVIDIA GPU monitoring data.
type GPUInfo struct {
	Available     bool         `json:"available"`
	DriverVersion string       `json:"driver_version"`
	CudaVersion   string       `json:"cuda_version"`
	GPUs          []GPDevice   `json:"gpus"`
	Processes     []GPUProcess `json:"processes,omitempty"`
}

// GPDevice represents a single NVIDIA GPU device.
type GPDevice struct {
	Index           uint32   `json:"index"`
	Name            string   `json:"name"`
	UUID            string   `json:"uuid"`
	TemperatureC    *float64 `json:"temperature_c"`
	UtilGPU         *float64 `json:"utilization_gpu_percent"`
	UtilMemory      *float64 `json:"utilization_memory_percent"`
	MemoryTotalMB   uint64   `json:"memory_total_mb"`
	MemoryUsedMB    uint64   `json:"memory_used_mb"`
	MemoryFreeMB    uint64   `json:"memory_free_mb"`
	PowerDrawW      *float64 `json:"power_draw_w"`
	PowerLimitW     *float64 `json:"power_limit_w"`
	FanSpeedPercent *float64 `json:"fan_speed_percent"`
	PState          string   `json:"pstate"`
}

// GPUProcess represents a process using GPU resources.
type GPUProcess struct {
	PID          uint32 `json:"pid"`
	Name         string `json:"name"`
	UsedMemoryMB uint64 `json:"used_memory_mb"`
	GPUIndex     uint32 `json:"gpu_index"`
}

// NPUInfo contains Ascend NPU monitoring data.
type NPUInfo struct {
	Available     bool        `json:"available"`
	DriverVersion string      `json:"driver_version"`
	CannVersion   string      `json:"cann_version"`
	NPUs          []NPUDevice `json:"npus"`
	Processes     []NPUProcess `json:"processes,omitempty"`
}

// NPUDevice represents a single Ascend NPU device.
type NPUDevice struct {
	Index          uint32   `json:"index"`
	Name           string   `json:"name"`
	Health         string   `json:"health"`
	TemperatureC   *float64 `json:"temperature_c"`
	UtilAICore     *float64 `json:"utilization_aicore_percent"`
	UtilMemory     *float64 `json:"utilization_memory_percent"`
	MemoryTotalMB  uint64   `json:"memory_total_mb"`
	MemoryUsedMB   uint64   `json:"memory_used_mb"`
	PowerDrawW     *float64 `json:"power_draw_w"`
}

// NPUProcess represents a process using NPU resources.
type NPUProcess struct {
	NPUIndex     uint32 `json:"npu_index"`
	PID          uint32 `json:"pid"`
	Name         string `json:"name"`
	UsedMemoryMB uint64 `json:"used_memory_mb"`
}

// ProcessInfo represents a running process.
type ProcessInfo struct {
	PID     uint32  `json:"pid"`
	PPID    uint32  `json:"ppid"`
	User    string  `json:"user"`
	State   string  `json:"state"`
	CPU     float64 `json:"cpu_percent"`
	Memory  float64 `json:"memory_percent"`
	RSSBytes uint64 `json:"rss_bytes"`
	Elapsed string  `json:"elapsed"`
	Command string  `json:"command"`
}

// DockerInfo contains Docker daemon monitoring data.
type DockerInfo struct {
	Available        bool              `json:"available"`
	Version          string            `json:"version"`
	ComposeAvailable bool              `json:"compose_available"`
	Containers       []DockerContainer `json:"containers"`
	Images           []DockerImage     `json:"images,omitempty"`
	ImagesCount      int               `json:"images_count"`
	VolumesCount     int               `json:"volumes_count"`
	NetworksCount    int               `json:"networks_count"`
}

// DockerContainer represents a Docker container with runtime stats.
type DockerContainer struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Image    string  `json:"image"`
	State    string  `json:"state"`
	Status   string  `json:"status"`
	CPUPerc  float64 `json:"cpu_percent"`
	MemUsage string  `json:"memory_usage"`
}

// DockerImage represents a Docker image.
type DockerImage struct {
	ID          string   `json:"id"`
	Repository  string   `json:"repository"`
	Tag         string   `json:"tag"`
	Size        int64    `json:"size"`
	CreatedAt   string   `json:"created_at"`
	CreatedSince string  `json:"created_since"`
	Containers  int      `json:"containers"`
}

// UserInfo represents a logged-in user session.
type UserInfo struct {
	Name        string `json:"name"`
	Terminal    string `json:"terminal"`
	From        string `json:"from"`
	LoginTime   string `json:"login_time"`
	DurationSec uint64 `json:"duration_sec"`
}

// MetricsHistory contains time-series history for charting.
type MetricsHistory struct {
	CPU      []HistoryPoint                `json:"cpu"`
	Memory   []HistoryPoint                `json:"memory"`
	Networks map[string][]NetworkHistoryPoint `json:"networks"`
	Disks    map[string][]DiskHistoryPoint    `json:"disks"`
}

// HistoryPoint is a single timestamped value for CPU/memory charts.
type HistoryPoint struct {
	Timestamp int64   `json:"t"`
	Value     float64 `json:"v"`
}

// NetworkHistoryPoint is a single timestamped network sample.
type NetworkHistoryPoint struct {
	Timestamp int64   `json:"t"`
	RxBytes   float64 `json:"rx"`
	TxBytes   float64 `json:"tx"`
}

// DiskHistoryPoint is a single timestamped disk I/O sample.
type DiskHistoryPoint struct {
	Timestamp  int64   `json:"t"`
	ReadBytes  float64 `json:"r"`
	WriteBytes float64 `json:"w"`
}
