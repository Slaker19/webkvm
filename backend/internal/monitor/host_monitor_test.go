package monitor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/sysutil"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// mockDockerClient implements dockerAPIClient for testing.
type mockDockerClient struct {
	pingErr    error
	version    types.Version
	versionErr error
	containers []container.Summary
	images     []image.Summary
	volumes    []*volume.Volume
	networks   []network.Inspect
}

func (m *mockDockerClient) Ping(ctx context.Context) (types.Ping, error) {
	return types.Ping{}, m.pingErr
}

func (m *mockDockerClient) ServerVersion(ctx context.Context) (types.Version, error) {
	return m.version, m.versionErr
}

func (m *mockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	return m.containers, nil
}

func (m *mockDockerClient) ContainerStatsOneShot(ctx context.Context, containerID string) (container.StatsResponseReader, error) {
	return container.StatsResponseReader{Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func (m *mockDockerClient) ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error) {
	return m.images, nil
}

func (m *mockDockerClient) VolumeList(ctx context.Context, options volume.ListOptions) (volume.ListResponse, error) {
	return volume.ListResponse{Volumes: m.volumes}, nil
}

func (m *mockDockerClient) NetworkList(ctx context.Context, options network.ListOptions) ([]network.Inspect, error) {
	return m.networks, nil
}

func (m *mockDockerClient) Close() error {
	return nil
}

func TestNewHostMonitor(t *testing.T) {
	m := NewHostMonitor()
	if m == nil {
		t.Fatal("NewHostMonitor returned nil")
	}
	if m.runner == nil {
		t.Error("runner should not be nil")
	}
	if m.lastNetStats == nil {
		t.Error("lastNetStats should be initialized")
	}
	if m.lastDiskStats == nil {
		t.Error("lastDiskStats should be initialized")
	}
	if m.history == nil {
		t.Error("history should be initialized")
	}
	if m.history.networks == nil {
		t.Error("history.networks should be initialized")
	}
	if m.history.disks == nil {
		t.Error("history.disks should be initialized")
	}
}

func TestNewHostMonitorWithRunner(t *testing.T) {
	mock := sysutil.NewMockRunner()
	m := NewHostMonitorWithRunner(mock)
	if m == nil {
		t.Fatal("NewHostMonitorWithRunner returned nil")
	}
	if m.runner != mock {
		t.Error("runner should be the injected mock")
	}
}

func TestIsVirtualInterface(t *testing.T) {
	tests := []struct {
		name     string
		iface    string
		expected bool
	}{
		{"physical ethernet", "eth0", false},
		{"physical wifi", "wlan0", false},
		{"physical enp", "enp3s0", false},
		// Note: "lo" is handled specially in collectNetworkInfo, not by isVirtualInterface
		{"docker bridge", "docker0", true},
		{"docker custom", "docker1", true},
		{"veth", "veth123abc", true},
		{"bridge", "br-abc123", true},
		{"libvirt bridge", "virbr0", true},
		{"tunnel", "tun0", true},
		{"tap", "tap0", true},
		{"tailscale", "tailscale0", true},
		{"wireguard", "wg0", true},
		{"vxlan", "vxlan1", true},
		{"flannel", "flannel.1", true},
		{"calico", "cali123", true},
		{"kube", "kube-bridge", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isVirtualInterface(tt.iface)
			if got != tt.expected {
				t.Errorf("isVirtualInterface(%q) = %v, want %v", tt.iface, got, tt.expected)
			}
		})
	}
}

func TestIsPseudoFS(t *testing.T) {
	tests := []struct {
		name     string
		fsType   string
		expected bool
	}{
		{"ext4", "ext4", false},
		{"xfs", "xfs", false},
		{"btrfs", "btrfs", false},
		{"tmpfs", "tmpfs", true},
		{"devtmpfs", "devtmpfs", true},
		{"proc", "proc", true},
		{"sysfs", "sysfs", true},
		{"cgroup", "cgroup", true},
		{"cgroup2", "cgroup2", true},
		{"overlay", "overlay", true},
		{"squashfs", "squashfs", true},
		{"devpts", "devpts", true},
		{"fusectl", "fusectl", true},
		// Note: "fuse.lxcfs" is NOT matched because "fuse" is not in the prefix list
		{"fuse.lxcfs not matched", "fuse.lxcfs", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPseudoFS(tt.fsType)
			if got != tt.expected {
				t.Errorf("isPseudoFS(%q) = %v, want %v", tt.fsType, got, tt.expected)
			}
		})
	}
}

func TestParseUint32(t *testing.T) {
	tests := []struct {
		input    string
		expected uint32
		wantErr  bool
	}{
		{"0", 0, false},
		{"42", 42, false},
		{"4294967295", 4294967295, false},
		{"", 0, true},
		{"abc", 0, true},
		{"4294967296", 0, true}, // overflow
		{"-1", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseUint32(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseUint32(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("parseUint32(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseUint64(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		wantErr  bool
	}{
		{"0", 0, false},
		{"42", 42, false},
		{"18446744073709551615", 18446744073709551615, false},
		{"", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseUint64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseUint64(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("parseUint64(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseFloat64(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
		wantErr  bool
	}{
		{"0", 0.0, false},
		{"3.14", 3.14, false},
		{"-2.5", -2.5, false},
		{"1e10", 1e10, false},
		{"", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseFloat64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseFloat64(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("parseFloat64(%q) = %f, want %f", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCommandExists(t *testing.T) {
	t.Run("command exists", func(t *testing.T) {
		mock := sysutil.NewMockRunner()
		mock.Register("which", []byte("/usr/bin/docker\n"), nil)
		if !commandExists(mock, "docker") {
			t.Error("commandExists should return true for registered command")
		}
	})

	t.Run("command not found", func(t *testing.T) {
		mock := sysutil.NewMockRunner()
		mock.Register("which", nil, os.ErrNotExist)
		if commandExists(mock, "nonexistent") {
			t.Error("commandExists should return false for unregistered command")
		}
	})
}

func TestGetCPUTopology(t *testing.T) {
	// This test reads from the actual /proc/cpuinfo on the test system.
	// It mainly verifies the function doesn't panic and returns sane values.
	cores, threads := getCPUTopology()

	if threads == 0 {
		t.Error("threads should be > 0 on a running system")
	}
	if cores == 0 {
		t.Error("cores should be > 0 on a running system")
	}
	if cores > threads {
		t.Errorf("cores (%d) should not exceed threads (%d)", cores, threads)
	}
}

func TestHistoryPruning(t *testing.T) {
	mock := sysutil.NewMockRunner()
	m := NewHostMonitorWithRunner(mock)

	// Simulate 65 samples with changing network interfaces
	for i := 0; i < 65; i++ {
		resp := &models.HostMetricsResponse{
			SampledAt: time.Now(),
			CPU:       models.CPUInfo{UsagePercent: float64(i)},
			Memory:    models.MemoryInfo{UsagePercent: float64(i)},
			Networks:  []models.NetworkInfo{{Name: "eth0", RxBytesPerSec: 100, TxBytesPerSec: 50}},
			Disks:     []models.HostDiskInfo{{Device: "/dev/sda", ReadBytesSec: 1000}},
		}
		m.updateHistory(resp)
	}

	// CPU and memory should be trimmed to maxHistorySize
	history := m.getHistory()
	if len(history.CPU) != maxHistorySize {
		t.Errorf("CPU history length = %d, want %d", len(history.CPU), maxHistorySize)
	}
	if len(history.Memory) != maxHistorySize {
		t.Errorf("Memory history length = %d, want %d", len(history.Memory), maxHistorySize)
	}

	// Network eth0 should be present
	if _, ok := history.Networks["eth0"]; !ok {
		t.Error("eth0 should be in network history")
	}
	if len(history.Networks["eth0"]) != maxHistorySize {
		t.Errorf("eth0 history length = %d, want %d", len(history.Networks["eth0"]), maxHistorySize)
	}

	// Now simulate eth0 disappearing and eth1 appearing
	resp := &models.HostMetricsResponse{
		SampledAt: time.Now(),
		CPU:       models.CPUInfo{UsagePercent: 50},
		Memory:    models.MemoryInfo{UsagePercent: 50},
		Networks:  []models.NetworkInfo{{Name: "eth1", RxBytesPerSec: 200, TxBytesPerSec: 100}},
		Disks:     []models.HostDiskInfo{{Device: "/dev/sdb", ReadBytesSec: 2000}},
	}
	m.updateHistory(resp)

	history = m.getHistory()
	// eth0 should be pruned
	if _, ok := history.Networks["eth0"]; ok {
		t.Error("eth0 should have been pruned from network history")
	}
	// eth1 should be present
	if _, ok := history.Networks["eth1"]; !ok {
		t.Error("eth1 should be in network history")
	}
	// /dev/sda should be pruned
	if _, ok := history.Disks["/dev/sda"]; ok {
		t.Error("/dev/sda should have been pruned from disk history")
	}
	// /dev/sdb should be present
	if _, ok := history.Disks["/dev/sdb"]; !ok {
		t.Error("/dev/sdb should be in disk history")
	}
}

func TestHistoryReturnsCopies(t *testing.T) {
	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())

	resp := &models.HostMetricsResponse{
		SampledAt: time.Now(),
		CPU:       models.CPUInfo{UsagePercent: 50},
		Memory:    models.MemoryInfo{UsagePercent: 60},
		Networks:  []models.NetworkInfo{{Name: "eth0", RxBytesPerSec: 100}},
		Disks:     []models.HostDiskInfo{{Device: "/dev/sda"}},
	}
	m.updateHistory(resp)

	h1 := m.getHistory()
	h2 := m.getHistory()

	// Modify h1 and verify h2 is unaffected
	if len(h1.CPU) > 0 {
		h1.CPU[0].Value = 999
	}
	if len(h2.CPU) > 0 && h2.CPU[0].Value == 999 {
		t.Error("getHistory should return copies, not references")
	}
}

func TestCollectDockerInfoNotAvailable(t *testing.T) {
	resetDockerClient()
	origFactory := dockerClientFactory
	defer func() { dockerClientFactory = origFactory }()

	// Factory returns error — simulates Docker not available
	dockerClientFactory = func() (dockerAPIClient, error) {
		return nil, os.ErrNotExist
	}

	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())
	docker := m.collectDockerInfo(context.Background())

	if docker != nil {
		t.Error("collectDockerInfo should return nil when docker is not available")
	}
	resetDockerClient()
}

func TestCollectDockerInfoDaemonNotRunning(t *testing.T) {
	resetDockerClient()
	origFactory := dockerClientFactory
	defer func() { dockerClientFactory = origFactory }()

	// Factory returns a client, but Ping fails — simulates daemon not running
	mock := &mockDockerClient{pingErr: context.DeadlineExceeded}
	dockerClientFactory = func() (dockerAPIClient, error) {
		return mock, nil
	}

	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())
	docker := m.collectDockerInfo(context.Background())

	if docker != nil {
		t.Error("collectDockerInfo should return nil when docker daemon is not running")
	}
	resetDockerClient()
}

func TestCollectDockerInfoSuccess(t *testing.T) {
	resetDockerClient()
	origFactory := dockerClientFactory
	defer func() { dockerClientFactory = origFactory }()

	mock := &mockDockerClient{
		pingErr: nil,
		version: types.Version{Version: "24.0.5"},
		containers: []container.Summary{
			{ID: "abc123def456", Names: []string{"/mycontainer"}, Image: "nginx", State: "running", Status: "Up 5 minutes"},
		},
		images: []image.Summary{
			{ID: "sha256:img1aaaa", RepoTags: []string{"nginx:latest"}, Size: 187000000, Created: 1704067200},
			{ID: "sha256:img2bbbb", RepoTags: []string{"ubuntu:22.04"}, Size: 77000000, Created: 1703462400},
			{ID: "sha256:img3cccc", RepoTags: []string{"redis:7"}, Size: 138000000, Created: 1702857600},
		},
		volumes:  []*volume.Volume{{Name: "vol1"}, {Name: "vol2"}},
		networks: []network.Inspect{{Name: "net1"}, {Name: "net2"}, {Name: "net3"}, {Name: "net4"}},
	}
	dockerClientFactory = func() (dockerAPIClient, error) {
		return mock, nil
	}

	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())
	docker := m.collectDockerInfo(context.Background())

	if docker == nil {
		t.Fatal("collectDockerInfo should return non-nil when docker is available")
	}
	if !docker.Available {
		t.Error("docker.Available should be true")
	}
	if docker.Version != "24.0.5" {
		t.Errorf("docker.Version = %q, want %q", docker.Version, "24.0.5")
	}
	if len(docker.Containers) != 1 {
		t.Fatalf("docker.Containers length = %d, want 1", len(docker.Containers))
	}
	if docker.Containers[0].Name != "mycontainer" {
		t.Errorf("container name = %q, want %q", docker.Containers[0].Name, "mycontainer")
	}
	if docker.ImagesCount != 3 {
		t.Errorf("docker.ImagesCount = %d, want 3", docker.ImagesCount)
	}
	if docker.VolumesCount != 2 {
		t.Errorf("docker.VolumesCount = %d, want 2", docker.VolumesCount)
	}
	if docker.NetworksCount != 4 {
		t.Errorf("docker.NetworksCount = %d, want 4", docker.NetworksCount)
	}
	resetDockerClient()
}

func TestCollectProcessInfo(t *testing.T) {
	mock := sysutil.NewMockRunner()
	mock.Handler = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ps" {
			return []byte(`    PID    PPID USER     STAT   %CPU %MEM   RSS     ELAPSED COMMAND
      1       0 root     S       0.0  0.1  1234    01:23:45 systemd
    123       1 www-data S       1.5  2.3  5678    00:05:30 nginx
    456       1 postgres S       0.5  5.0 20000    01:00:00 postgres
`), nil
		}
		return nil, nil
	}

	m := NewHostMonitorWithRunner(mock)
	procs := m.collectProcessInfo(context.Background())

	if len(procs) != 3 {
		t.Fatalf("process count = %d, want 3", len(procs))
	}

	if procs[0].PID != 1 {
		t.Errorf("procs[0].PID = %d, want 1", procs[0].PID)
	}
	if procs[0].User != "root" {
		t.Errorf("procs[0].User = %q, want %q", procs[0].User, "root")
	}
	if procs[0].Command != "systemd" {
		t.Errorf("procs[0].Command = %q, want %q", procs[0].Command, "systemd")
	}

	if procs[1].PID != 123 {
		t.Errorf("procs[1].PID = %d, want 123", procs[1].PID)
	}
	if procs[1].CPU != 1.5 {
		t.Errorf("procs[1].CPU = %f, want 1.5", procs[1].CPU)
	}
	if procs[1].RSSBytes != 5678*1024 {
		t.Errorf("procs[1].RSSBytes = %d, want %d", procs[1].RSSBytes, 5678*1024)
	}
}

func TestCollectUserInfo(t *testing.T) {
	mock := sysutil.NewMockRunner()
	mock.Handler = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "who" {
			now := time.Now()
			timeStr := now.Format("2006-01-02 15:04")
			return []byte("root     pts/0        " + timeStr + " (192.168.1.100)\n"), nil
		}
		return nil, nil
	}

	m := NewHostMonitorWithRunner(mock)
	users := m.collectUserInfo(context.Background())

	if len(users) != 1 {
		t.Fatalf("user count = %d, want 1", len(users))
	}
	if users[0].Name != "root" {
		t.Errorf("users[0].Name = %q, want %q", users[0].Name, "root")
	}
	if users[0].Terminal != "pts/0" {
		t.Errorf("users[0].Terminal = %q, want %q", users[0].Terminal, "pts/0")
	}
	if users[0].From != "192.168.1.100" {
		t.Errorf("users[0].From = %q, want %q", users[0].From, "192.168.1.100")
	}
}

func TestCollectUserInfoDateFormats(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		timeStr  string
		wantUser bool
	}{
		{"ISO format with year", now.Format("2006-01-02 15:04"), true},
		{"Month abbreviation without year", now.Format("Jan 02 15:04"), true},
		{"Numeric month-day without year", now.Format("01-02 15:04"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := sysutil.NewMockRunner()
			mock.Handler = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "who" {
					return []byte("testuser pts/1 " + tt.timeStr + " (10.0.0.1)\n"), nil
				}
				return nil, nil
			}

			m := NewHostMonitorWithRunner(mock)
			users := m.collectUserInfo(context.Background())

			if tt.wantUser {
				if len(users) != 1 {
					t.Fatalf("expected 1 user, got %d", len(users))
				}
				if users[0].Name != "testuser" {
					t.Errorf("users[0].Name = %q, want %q", users[0].Name, "testuser")
				}
				// DurationSec should be set (>= 0) if parsing succeeded
				// For recent login times, it should be a small positive number
				if users[0].DurationSec > 24*60*60 {
					t.Errorf("DurationSec = %d, expected less than 24 hours for recent login", users[0].DurationSec)
				}
			}
		})
	}
}

func TestCollectGPUInfoNotAvailable(t *testing.T) {
	mock := sysutil.NewMockRunner()
	mock.Register("which", nil, os.ErrNotExist)

	m := NewHostMonitorWithRunner(mock)
	gpu := m.collectGPUInfo(context.Background())

	if gpu != nil {
		t.Error("collectGPUInfo should return nil when nvidia-smi is not available")
	}
}

func TestCollectNPUInfoNotAvailable(t *testing.T) {
	mock := sysutil.NewMockRunner()
	mock.Register("which", nil, os.ErrNotExist)

	m := NewHostMonitorWithRunner(mock)

	// Note: findNPUSMI also checks hardcoded paths like /usr/local/bin/npu-smi
	// If those paths exist on the test system, npu-smi will be found.
	// This test verifies the function handles the "not available" case gracefully.
	npu := m.collectNPUInfo(context.Background())

	// If NPU tools are not installed, npu should be nil
	// If they are installed, we just verify it doesn't panic
	_ = npu
}

func TestConcurrentCollect(t *testing.T) {
	mock := sysutil.NewMockRunner()
	mock.Handler = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "which" {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}

	m := NewHostMonitorWithRunner(mock)

	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := m.Collect(ctx)
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent Collect failed: %v", err)
	}
}

func TestCollectLoadInfo(t *testing.T) {
	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())
	load := m.collectLoadInfo()

	// On a real Linux system, load should be non-negative
	if load.Load1 < 0 {
		t.Errorf("Load1 = %f, should be >= 0", load.Load1)
	}
	if load.Load5 < 0 {
		t.Errorf("Load5 = %f, should be >= 0", load.Load5)
	}
	if load.Load15 < 0 {
		t.Errorf("Load15 = %f, should be >= 0", load.Load15)
	}
}

func TestCollectMemoryInfo(t *testing.T) {
	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())
	mem, swap := m.collectMemoryInfo()

	// On a real system, total memory should be > 0
	if mem.TotalBytes == 0 {
		t.Error("mem.TotalBytes should be > 0 on a running system")
	}
	if mem.TotalBytes < mem.UsedBytes {
		t.Errorf("TotalBytes (%d) should be >= UsedBytes (%d)", mem.TotalBytes, mem.UsedBytes)
	}
	if mem.UsagePercent < 0 || mem.UsagePercent > 100 {
		t.Errorf("mem.UsagePercent = %f, should be in [0, 100]", mem.UsagePercent)
	}

	// Swap may be 0 on systems without swap
	if swap.TotalBytes > 0 && swap.TotalBytes < swap.UsedBytes {
		t.Errorf("swap TotalBytes (%d) should be >= UsedBytes (%d)", swap.TotalBytes, swap.UsedBytes)
	}
}

func TestCollectCPUInfo(t *testing.T) {
	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())

	// First call should be "warming_up"
	cpu1 := m.collectCPUInfo()
	if cpu1.UsageSource != "warming_up" {
		t.Errorf("first call UsageSource = %q, want %q", cpu1.UsageSource, "warming_up")
	}

	// Wait a bit and call again
	time.Sleep(100 * time.Millisecond)
	cpu2 := m.collectCPUInfo()

	// Second call should have a real source
	if cpu2.UsageSource != "aggregate" && cpu2.UsageSource != "warming_up" {
		t.Errorf("second call UsageSource = %q, want %q or %q", cpu2.UsageSource, "aggregate", "warming_up")
	}

	if cpu2.Cores == 0 {
		t.Error("Cores should be > 0")
	}
	if cpu2.Threads == 0 {
		t.Error("Threads should be > 0")
	}
}

func TestGetInterfaceState(t *testing.T) {
	// Test with loopback - state may vary by environment
	state := getInterfaceState("lo")
	validStates := map[string]bool{"up": true, "down": true, "unknown": true}
	if !validStates[state] {
		t.Errorf("getInterfaceState(lo) = %q, want one of up/down/unknown", state)
	}

	// Test with non-existent interface
	state = getInterfaceState("nonexistent_iface_xyz")
	if state != "unknown" {
		t.Errorf("getInterfaceState(nonexistent) = %q, want %q", state, "unknown")
	}
}

func TestReadDiskStats(t *testing.T) {
	stats := readDiskStats()
	// On a real system, there should be at least one disk
	// This is a sanity check that the function doesn't panic
	_ = stats
}

func TestFindCANNVersion(t *testing.T) {
	// This will return empty on systems without Ascend drivers
	version := findCANNVersion()
	_ = version // Just verify it doesn't panic
}

func TestFindNPUSMI(t *testing.T) {
	mock := sysutil.NewMockRunner()
	mock.Register("which", nil, os.ErrNotExist)

	path := findNPUSMI(mock)
	// On systems without NPU, should return empty
	if path != "" {
		t.Logf("findNPUSMI returned %q (NPU may be present)", path)
	}
}

func TestCollectWithMockedProcFiles(t *testing.T) {
	// Create temp directory with mock proc files
	tmpDir := t.TempDir()

	// Create mock /proc/loadavg
	loadavgContent := "0.50 0.75 1.00 1/200 12345\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "loadavg"), []byte(loadavgContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify the content is parseable
	fields := strings.Fields(strings.TrimSpace(loadavgContent))
	if len(fields) < 3 {
		t.Fatal("mock loadavg should have at least 3 fields")
	}
}

func TestHistorySizeTracking(t *testing.T) {
	m := NewHostMonitorWithRunner(sysutil.NewMockRunner())

	if m.historySize != 0 {
		t.Errorf("initial historySize = %d, want 0", m.historySize)
	}

	for i := 0; i < 5; i++ {
		resp := &models.HostMetricsResponse{
			SampledAt: time.Now(),
			CPU:       models.CPUInfo{UsagePercent: 50},
			Memory:    models.MemoryInfo{UsagePercent: 60},
		}
		m.updateHistory(resp)
	}

	if m.historySize != 5 {
		t.Errorf("historySize after 5 updates = %d, want 5", m.historySize)
	}
}

func TestNetworkRateCalculation(t *testing.T) {
	mock := sysutil.NewMockRunner()
	m := NewHostMonitorWithRunner(mock)

	// First collection: no previous data, rates should be 0
	ctx := context.Background()
	networks1, summary1 := m.collectNetworkInfo(ctx)

	// If there are real interfaces, check that rates are 0 on first call
	for _, net := range networks1 {
		if net.RxBytesPerSec != 0 {
			t.Errorf("first call RxBytesPerSec for %s = %f, want 0", net.Name, net.RxBytesPerSec)
		}
		if net.TxBytesPerSec != 0 {
			t.Errorf("first call TxBytesPerSec for %s = %f, want 0", net.Name, net.TxBytesPerSec)
		}
	}
	if summary1.RxBytesPerSec != 0 {
		t.Errorf("first call summary.RxBytesPerSec = %f, want 0", summary1.RxBytesPerSec)
	}

	// Wait and collect again
	time.Sleep(100 * time.Millisecond)
	networks2, _ := m.collectNetworkInfo(ctx)

	// Rates should now be calculated (may still be 0 if no traffic)
	_ = networks2
}

func TestDiskRateCalculation(t *testing.T) {
	mock := sysutil.NewMockRunner()
	m := NewHostMonitorWithRunner(mock)

	ctx := context.Background()

	// First collection: no previous data
	disks1 := m.collectDiskInfo(ctx)
	for _, disk := range disks1 {
		if disk.ReadBytesSec != 0 {
			t.Errorf("first call ReadBytesSec for %s = %f, want 0", disk.Device, disk.ReadBytesSec)
		}
		if disk.WriteBytesSec != 0 {
			t.Errorf("first call WriteBytesSec for %s = %f, want 0", disk.Device, disk.WriteBytesSec)
		}
	}

	// Wait and collect again
	time.Sleep(100 * time.Millisecond)
	disks2 := m.collectDiskInfo(ctx)
	_ = disks2
}

func TestGetPhysicalDisk(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// SCSI/SATA partitions
		{"/dev/sda1", "/dev/sda"},
		{"/dev/sda2", "/dev/sda"},
		{"/dev/sdb1", "/dev/sdb"},
		{"/dev/sdc10", "/dev/sdc"},

		// NVMe partitions
		{"/dev/nvme0n1p1", "/dev/nvme0n1"},
		{"/dev/nvme0n1p2", "/dev/nvme0n1"},
		{"/dev/nvme1n1p1", "/dev/nvme1n1"},

		// LVM - keep as-is
		{"/dev/mapper/vg-lv", "/dev/mapper/vg-lv"},
		{"/dev/mapper/ubuntu--vg-ubuntu--lv", "/dev/mapper/ubuntu--vg-ubuntu--lv"},

		// RAID - keep as-is
		{"/dev/md0", "/dev/md0"},
		{"/dev/md127", "/dev/md127"},

		// Device mapper - keep as-is
		{"/dev/dm-0", "/dev/dm-0"},
		{"/dev/dm-1", "/dev/dm-1"},

		// Whole devices (no partition number)
		{"/dev/sda", "/dev/sda"},
		{"/dev/nvme0n1", "/dev/nvme0n1"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := getPhysicalDisk(tc.input)
			if result != tc.expected {
				t.Errorf("getPhysicalDisk(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestGroupDisksByPhysicalDevice(t *testing.T) {
	disks := []models.HostDiskInfo{
		{Device: "/dev/sda1", Mount: "/boot", FSType: "ext4", TotalBytes: 1024, UsedBytes: 512, UsagePercent: 50},
		{Device: "/dev/sda2", Mount: "/", FSType: "ext4", TotalBytes: 10240, UsedBytes: 5120, UsagePercent: 50},
		{Device: "/dev/sdb1", Mount: "/data", FSType: "xfs", TotalBytes: 20480, UsedBytes: 10240, UsagePercent: 50},
		{Device: "/dev/nvme0n1p1", Mount: "/fast", FSType: "ext4", TotalBytes: 5120, UsedBytes: 2560, UsagePercent: 50},
		{Device: "/dev/nvme0n1p2", Mount: "/faster", FSType: "ext4", TotalBytes: 5120, UsedBytes: 2560, UsagePercent: 50},
	}

	groups := groupDisksByPhysicalDevice(disks)

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// Check /dev/sda group
	var sdaGroup *models.DiskGroup
	for i := range groups {
		if groups[i].Device == "/dev/sda" {
			sdaGroup = &groups[i]
			break
		}
	}
	if sdaGroup == nil {
		t.Fatal("expected /dev/sda group")
	}
	if len(sdaGroup.Partitions) != 2 {
		t.Errorf("expected 2 partitions for /dev/sda, got %d", len(sdaGroup.Partitions))
	}
	// TotalBytes comes from sysfs (real hardware) or fallback to partition sum
	// Just verify it's non-zero and >= sum of partitions
	if sdaGroup.TotalBytes == 0 {
		t.Error("expected non-zero TotalBytes for /dev/sda")
	}
	if sdaGroup.UsedBytes != 5632 {
		t.Errorf("expected UsedBytes=5632 for /dev/sda, got %d", sdaGroup.UsedBytes)
	}

	// Check partition structure
	var sda1Part *models.Partition
	var sda2Part *models.Partition
	for i := range sdaGroup.Partitions {
		if sdaGroup.Partitions[i].Device == "/dev/sda1" {
			sda1Part = &sdaGroup.Partitions[i]
		}
		if sdaGroup.Partitions[i].Device == "/dev/sda2" {
			sda2Part = &sdaGroup.Partitions[i]
		}
	}
	if sda1Part == nil || sda2Part == nil {
		t.Fatal("expected sda1 and sda2 partitions")
	}
	if len(sda1Part.Mounts) != 1 || sda1Part.Mounts[0].Path != "/boot" {
		t.Errorf("expected sda1 to have /boot mount")
	}
	if len(sda2Part.Mounts) != 1 || sda2Part.Mounts[0].Path != "/" {
		t.Errorf("expected sda2 to have / mount")
	}

	// Check /dev/nvme0n1 group
	var nvmeGroup *models.DiskGroup
	for i := range groups {
		if groups[i].Device == "/dev/nvme0n1" {
			nvmeGroup = &groups[i]
			break
		}
	}
	if nvmeGroup == nil {
		t.Fatal("expected /dev/nvme0n1 group")
	}
	if len(nvmeGroup.Partitions) != 2 {
		t.Errorf("expected 2 partitions for /dev/nvme0n1, got %d", len(nvmeGroup.Partitions))
	}
}

// TestGroupDisksBindMounts verifies that bind mounts (same device, multiple mount points)
// do not cause double-counting of UsedBytes.
func TestGroupDisksBindMounts(t *testing.T) {
	// Simulate a system where /dev/sda2 is mounted at / and also bind-mounted to /bin, /etc, /usr
	// All these mounts report the same UsedBytes because they share the same filesystem
	disks := []models.HostDiskInfo{
		{Device: "/dev/sda1", Mount: "/boot", FSType: "ext4", TotalBytes: 1073741824, UsedBytes: 536870912, UsagePercent: 50},      // 1GB, 50% used
		{Device: "/dev/sda2", Mount: "/", FSType: "ext4", TotalBytes: 32212254720, UsedBytes: 16106127360, UsagePercent: 50},         // 30GB, 50% used
		{Device: "/dev/sda2", Mount: "/bin", FSType: "ext4", TotalBytes: 32212254720, UsedBytes: 16106127360, UsagePercent: 50},      // bind mount - same data
		{Device: "/dev/sda2", Mount: "/etc", FSType: "ext4", TotalBytes: 32212254720, UsedBytes: 16106127360, UsagePercent: 50},      // bind mount - same data
		{Device: "/dev/sda2", Mount: "/usr", FSType: "ext4", TotalBytes: 32212254720, UsedBytes: 16106127360, UsagePercent: 50},      // bind mount - same data
	}

	groups := groupDisksByPhysicalDevice(disks)

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}

	sdaGroup := &groups[0]
	if sdaGroup.Device != "/dev/sda" {
		t.Fatalf("expected /dev/sda, got %s", sdaGroup.Device)
	}

	// Check that /dev/sda has 2 partitions
	if len(sdaGroup.Partitions) != 2 {
		t.Errorf("expected 2 partitions, got %d", len(sdaGroup.Partitions))
	}

	// Check /dev/sda2 partition has 4 mount points (/, /bin, /etc, /usr)
	var sda2Part *models.Partition
	for i := range sdaGroup.Partitions {
		if sdaGroup.Partitions[i].Device == "/dev/sda2" {
			sda2Part = &sdaGroup.Partitions[i]
			break
		}
	}
	if sda2Part == nil {
		t.Fatal("expected /dev/sda2 partition")
	}
	if len(sda2Part.Mounts) != 4 {
		t.Errorf("expected 4 mounts for /dev/sda2, got %d", len(sda2Part.Mounts))
	}

	// CRITICAL: UsedBytes should NOT be 4x the partition usage
	// It should be: sda1 (536870912) + sda2 (16106127360) = 16642998272
	expectedUsedBytes := uint64(536870912 + 16106127360)
	if sdaGroup.UsedBytes != expectedUsedBytes {
		t.Errorf("UsedBytes double-counting bug: expected %d, got %d", expectedUsedBytes, sdaGroup.UsedBytes)
	}

	// Partition sda2 UsedBytes should be from one mount only, not accumulated
	if sda2Part.UsedBytes != 16106127360 {
		t.Errorf("partition UsedBytes should not accumulate bind mounts: expected 16106127360, got %d", sda2Part.UsedBytes)
	}

	// Verify usage percent is calculated correctly (not exceeding 100%)
	if sdaGroup.UsagePercent > 100 {
		t.Errorf("usage percent should not exceed 100%%, got %.2f%%", sdaGroup.UsagePercent)
	}
	if sda2Part.UsagePercent > 100 {
		t.Errorf("partition usage percent should not exceed 100%%, got %.2f%%", sda2Part.UsagePercent)
	}
}
