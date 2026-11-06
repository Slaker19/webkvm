package monitor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/sysutil"
)

const (
	maxHistorySize     = 60
	externalCmdTimeout = 10 * time.Second
	extendedCacheTTL   = 10 * time.Second
	maxDockerImages    = 100 // Limit images collection to prevent performance issues
)

// HostMonitor collects comprehensive host metrics.
type HostMonitor struct {
	runner sysutil.CommandRunner
	mu     sync.RWMutex

	// CPU sampling state
	lastCPUTotal  uint64
	lastCPUIdle   uint64
	lastCPUTime   time.Time
	cpuPerCore    []cpuCoreState

	// Network sampling state — protected by mu
	lastNetStats map[string]*netCounters
	lastNetTime  time.Time

	// Disk I/O sampling state — protected by mu
	lastDiskStats map[string]*diskCounters
	lastDiskTime  time.Time

	// History buffer
	history     *metricsHistory
	historySize int

	// Extended data cache — "last known good" for GPU/Docker/NPU/Users.
	// CollectFast merges this into every response so the frontend always
	// receives a complete snapshot without a second round-trip.
	extendedMu    sync.RWMutex
	extendedCache *extendedCacheEntry
	refreshLock   sync.Mutex // held while a background refresh is running
}

type extendedCacheEntry struct {
	gpu       *models.GPUInfo
	npu       *models.NPUInfo
	docker    *models.DockerInfo
	users     []models.UserInfo
	updatedAt time.Time
}

type cpuCoreState struct {
	lastTotal uint64
	lastIdle  uint64
}

type netCounters struct {
	rxBytes uint64
	txBytes uint64
}

type diskCounters struct {
	readSectors  uint64
	writeSectors uint64
}

type metricsHistory struct {
	cpu      []models.HistoryPoint
	memory   []models.HistoryPoint
	networks map[string][]models.NetworkHistoryPoint
	disks    map[string][]models.DiskHistoryPoint
}

// NewHostMonitor creates a new host monitor with the default command runner.
func NewHostMonitor() *HostMonitor {
	return NewHostMonitorWithRunner(sysutil.DefaultRunner)
}

// NewHostMonitorWithRunner creates a host monitor with an injected command runner (for testing).
func NewHostMonitorWithRunner(runner sysutil.CommandRunner) *HostMonitor {
	return &HostMonitor{
		runner:        runner,
		lastNetStats:  make(map[string]*netCounters),
		lastDiskStats: make(map[string]*diskCounters),
		history: &metricsHistory{
			networks: make(map[string][]models.NetworkHistoryPoint),
			disks:    make(map[string][]models.DiskHistoryPoint),
		},
	}
}

// Collect gathers all host metrics using parallel collection for better performance.
// Fast data (CPU, memory, disk, network) is collected from /proc files.
// Slow data (Docker, GPU, NPU) is collected in parallel with timeouts.
func (m *HostMonitor) Collect(ctx context.Context) (*models.HostMetricsResponse, error) {
	resp := &models.HostMetricsResponse{
		SampledAt: time.Now(),
	}

	// Use a wait group to collect all data in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex // Protects resp from concurrent writes

	// Fast collectors (read from /proc, no external commands)
	wg.Add(6)

	go func() {
		defer wg.Done()
		sys := m.collectSystemInfo(ctx)
		mu.Lock()
		resp.System = sys
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		cpu := m.collectCPUInfo()
		mu.Lock()
		resp.CPU = cpu
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		load := m.collectLoadInfo()
		mu.Lock()
		resp.Load = load
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		mem, swap := m.collectMemoryInfo()
		mu.Lock()
		resp.Memory = mem
		resp.Swap = swap
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		networks, summary := m.collectNetworkInfo(ctx)
		mu.Lock()
		resp.Networks = networks
		resp.NetworkSummary = summary
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		disks := m.collectDiskInfo(ctx)
		mu.Lock()
		resp.Disks = disks
		resp.DiskGroups = groupDisksByPhysicalDevice(disks)
		mu.Unlock()
	}()

	// Slow collectors (external commands) - use shorter timeout
	// These run in parallel but with a 3-second timeout each
	slowCtx, slowCancel := context.WithTimeout(ctx, 3*time.Second)
	defer slowCancel()

	wg.Add(4)

	go func() {
		defer wg.Done()
		if gpu := m.collectGPUInfo(slowCtx); gpu != nil {
			mu.Lock()
			resp.GPU = gpu
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		if npu := m.collectNPUInfo(slowCtx); npu != nil {
			mu.Lock()
			resp.NPU = npu
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		if docker := m.collectDockerInfo(slowCtx); docker != nil {
			mu.Lock()
			resp.Docker = docker
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		users := m.collectUserInfo(slowCtx)
		mu.Lock()
		resp.Users = users
		mu.Unlock()
	}()

	// Wait for all collectors to finish
	wg.Wait()

	m.updateHistory(resp)
	resp.History = m.getHistory()

	return resp, nil
}

// Runner returns the command runner used by the host monitor.
// This is exposed for API handlers that need to run Docker commands.
func (m *HostMonitor) Runner() sysutil.CommandRunner {
	return m.runner
}

// CollectFast gathers fast metrics (CPU, memory, disk, network, system info)
// and merges cached extended data (GPU, Docker, NPU, users) into the response.
// This method NEVER blocks on external commands — it returns in ~50ms with
// fast data plus whatever is currently in the extended cache. The cache is
// refreshed asynchronously when stale (> extendedCacheTTL) or empty.
// The first response may lack extended data; it appears on the next poll
// once the background refresh completes.
func (m *HostMonitor) CollectFast(ctx context.Context) (*models.HostMetricsResponse, error) {
	resp := &models.HostMetricsResponse{
		SampledAt: time.Now(),
	}

	// Collect all fast data in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(6)

	go func() {
		defer wg.Done()
		sys := m.collectSystemInfo(ctx)
		mu.Lock()
		resp.System = sys
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		cpu := m.collectCPUInfo()
		mu.Lock()
		resp.CPU = cpu
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		load := m.collectLoadInfo()
		mu.Lock()
		resp.Load = load
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		mem, swap := m.collectMemoryInfo()
		mu.Lock()
		resp.Memory = mem
		resp.Swap = swap
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		networks, summary := m.collectNetworkInfo(ctx)
		mu.Lock()
		resp.Networks = networks
		resp.NetworkSummary = summary
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		disks := m.collectDiskInfo(ctx)
		mu.Lock()
		resp.Disks = disks
		resp.DiskGroups = groupDisksByPhysicalDevice(disks)
		mu.Unlock()
	}()

	wg.Wait()

	// Merge cached extended data
	m.extendedMu.RLock()
	cache := m.extendedCache
	m.extendedMu.RUnlock()

	if cache != nil {
		resp.GPU = cache.gpu
		resp.NPU = cache.npu
		resp.Docker = cache.docker
		resp.Users = cache.users
		// Expose when the extended data was last refreshed so the frontend
		// can show a staleness indicator if needed.
		cachedAt := cache.updatedAt
		resp.ExtendedCachedAt = &cachedAt
	}

	// Refresh extended cache asynchronously when stale or empty.
	// CollectFast NEVER blocks on external commands — the response returns
	// in ~50ms with fast data plus whatever cached extended data exists.
	needsRefresh := cache == nil || time.Since(cache.updatedAt) > extendedCacheTTL
	if needsRefresh && m.refreshLock.TryLock() {
		go func() {
			defer m.refreshLock.Unlock()
			m.refreshExtendedCache(context.Background())
		}()
	}

	// Record this sample in the history buffer and include it in the response
	m.updateHistory(resp)
	resp.History = m.getHistory()

	return resp, nil
}

// CollectExtended gathers slow metrics (Docker, GPU, NPU, users).
// This runs external commands and may take up to 3 seconds.
// Use this after CollectFast to populate additional panels asynchronously.
func (m *HostMonitor) CollectExtended(ctx context.Context) (*models.HostMetricsResponse, error) {
	resp := &models.HostMetricsResponse{
		SampledAt: time.Now(),
	}

	// Use a 3-second timeout for all slow collectors
	slowCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(4)

	go func() {
		defer wg.Done()
		if gpu := m.collectGPUInfo(slowCtx); gpu != nil {
			mu.Lock()
			resp.GPU = gpu
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		if npu := m.collectNPUInfo(slowCtx); npu != nil {
			mu.Lock()
			resp.NPU = npu
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		if docker := m.collectDockerInfo(slowCtx); docker != nil {
			mu.Lock()
			resp.Docker = docker
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		users := m.collectUserInfo(slowCtx)
		mu.Lock()
		resp.Users = users
		mu.Unlock()
	}()

	wg.Wait()

	return resp, nil
}

// refreshExtendedCache collects extended data (GPU, NPU, Docker, users) and
// updates the cache. If collection partially fails, previously cached values
// are preserved for the fields that failed — this prevents transient command
// timeouts from clearing panels on the frontend.
func (m *HostMonitor) refreshExtendedCache(ctx context.Context) {
	slowCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex

	entry := &extendedCacheEntry{
		updatedAt: time.Now(),
	}

	// Carry forward previous cache values as fallback
	m.extendedMu.RLock()
	prev := m.extendedCache
	m.extendedMu.RUnlock()

	wg.Add(4)

	go func() {
		defer wg.Done()
		if gpu := m.collectGPUInfo(slowCtx); gpu != nil {
			mu.Lock()
			entry.gpu = gpu
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		if npu := m.collectNPUInfo(slowCtx); npu != nil {
			mu.Lock()
			entry.npu = npu
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		if docker := m.collectDockerInfo(slowCtx); docker != nil {
			mu.Lock()
			entry.docker = docker
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		users := m.collectUserInfo(slowCtx)
		mu.Lock()
		entry.users = users
		mu.Unlock()
	}()

	wg.Wait()

	// Fill in fallback values for fields that failed to collect
	if prev != nil {
		if entry.gpu == nil {
			entry.gpu = prev.gpu
		}
		if entry.npu == nil {
			entry.npu = prev.npu
		}
		if entry.docker == nil {
			entry.docker = prev.docker
		}
		if entry.users == nil {
			entry.users = prev.users
		}
	}

	m.extendedMu.Lock()
	m.extendedCache = entry
	m.extendedMu.Unlock()
}

func (m *HostMonitor) collectSystemInfo(ctx context.Context) models.SystemInfo {
	info := models.SystemInfo{
		Arch: runtime.GOARCH,
	}

	if hostname, err := os.Hostname(); err == nil {
		info.Hostname = hostname
	}

	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				info.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				break
			}
		}
	}

	cmdCtx, cancel := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel()
	if out, err := m.runner.Run(cmdCtx, "uname", "-r"); err == nil {
		info.Kernel = strings.TrimSpace(string(out))
	}

	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if up, err := strconv.ParseFloat(fields[0], 64); err == nil {
				info.UptimeSec = uint64(up)
			}
		}
	}

	return info
}

func (m *HostMonitor) collectCPUInfo() models.CPUInfo {
	info := models.CPUInfo{
		UsageSource: "warming_up",
	}

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return info
	}
	defer f.Close()

	var cores uint32
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				info.Model = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "processor") {
			cores++
		}
	}
	info.Cores = cores

	cpuCount, threadCount := getCPUTopology()
	if info.Cores == 0 {
		info.Cores = cpuCount
	}
	info.Threads = threadCount

	m.mu.Lock()
	defer m.mu.Unlock()

	usage, perCore, source := m.calculateCPUUsage()
	info.UsagePercent = usage
	info.PerCore = perCore
	info.UsageSource = source

	return info
}

// getCPUTopology returns unique (physicalID, coreID) pairs for accurate core count.
func getCPUTopology() (cores, threads uint32) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0, 0
	}

	type cpuLocation struct {
		physicalID string
		coreID     string
	}
	seen := make(map[cpuLocation]bool)

	var currentPhysical, currentCore string
	hasPhysical, hasCore := false, false

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "physical id") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				currentPhysical = strings.TrimSpace(parts[1])
				hasPhysical = true
			}
		}
		if strings.HasPrefix(line, "core id") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				currentCore = strings.TrimSpace(parts[1])
				hasCore = true
			}
		}
		if strings.HasPrefix(line, "processor") {
			threads++
			if hasPhysical && hasCore {
				loc := cpuLocation{physicalID: currentPhysical, coreID: currentCore}
				seen[loc] = true
			}
			hasPhysical, hasCore = false, false
		}
	}

	cores = uint32(len(seen))
	if cores == 0 {
		cores = threads
	}
	return cores, threads
}

// calculateCPUUsage computes CPU usage from /proc/stat delta. Must be called with mu held.
func (m *HostMonitor) calculateCPUUsage() (float64, []models.CoreUsage, string) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, nil, "warming_up"
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu") {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return 0, nil, "warming_up"
	}

	now := time.Now()

	var total, idle uint64
	fields := strings.Fields(lines[0])
	if len(fields) < 5 {
		return 0, nil, "warming_up"
	}
	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}

	var usage float64
	source := "warming_up"
	if m.lastCPUTotal > 0 && !m.lastCPUTime.IsZero() {
		dTotal := total - m.lastCPUTotal
		dIdle := idle - m.lastCPUIdle
		if dTotal > 0 {
			usage = (1.0 - float64(dIdle)/float64(dTotal)) * 100.0
			if usage < 0 {
				usage = 0
			}
			if usage > 100 {
				usage = 100
			}
			source = "aggregate"
		}
	}

	m.lastCPUTotal = total
	m.lastCPUIdle = idle
	m.lastCPUTime = now

	var perCore []models.CoreUsage
	for i := 1; i < len(lines); i++ {
		fields := strings.Fields(lines[i])
		if len(fields) < 5 {
			continue
		}

		coreID := uint32(i - 1)
		var coreTotal, coreIdle uint64
		for j, v := range fields[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			coreTotal += n
			if j == 3 || j == 4 {
				coreIdle += n
			}
		}

		for len(m.cpuPerCore) <= int(coreID) {
			m.cpuPerCore = append(m.cpuPerCore, cpuCoreState{})
		}

		var coreUsage float64
		prev := &m.cpuPerCore[coreID]
		if prev.lastTotal > 0 {
			dTotal := coreTotal - prev.lastTotal
			dIdle := coreIdle - prev.lastIdle
			if dTotal > 0 {
				coreUsage = (1.0 - float64(dIdle)/float64(dTotal)) * 100.0
				if coreUsage < 0 {
					coreUsage = 0
				}
				if coreUsage > 100 {
					coreUsage = 100
				}
			}
		}
		prev.lastTotal = coreTotal
		prev.lastIdle = coreIdle

		perCore = append(perCore, models.CoreUsage{
			ID:    coreID,
			Usage: coreUsage,
		})
	}

	return usage, perCore, source
}

func (m *HostMonitor) collectLoadInfo() models.LoadInfo {
	info := models.LoadInfo{}

	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return info
	}

	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		info.Load1, _ = strconv.ParseFloat(fields[0], 64)
		info.Load5, _ = strconv.ParseFloat(fields[1], 64)
		info.Load15, _ = strconv.ParseFloat(fields[2], 64)
	}

	return info
}

func (m *HostMonitor) collectMemoryInfo() (models.MemoryInfo, models.SwapInfo) {
	mem := models.MemoryInfo{}
	swap := models.SwapInfo{}

	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return mem, swap
	}
	defer f.Close()

	var memTotal, memFree, memAvail, buffers, cached, sReclaimable uint64
	var swapTotal, swapFree uint64

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := parts[0]
		val, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		val *= 1024 // Convert from kB to bytes

		switch key {
		case "MemTotal:":
			memTotal = val
		case "MemFree:":
			memFree = val
		case "MemAvailable:":
			memAvail = val
		case "Buffers:":
			buffers = val
		case "Cached:":
			cached = val
		case "SReclaimable:":
			sReclaimable = val
		case "SwapTotal:":
			swapTotal = val
		case "SwapFree:":
			swapFree = val
		}
	}

	if memAvail == 0 {
		memAvail = memFree
	}

	mem.TotalBytes = memTotal
	mem.AvailableBytes = memAvail
	mem.UsedBytes = memTotal - memAvail
	mem.BuffersBytes = buffers
	mem.CachedBytes = cached + sReclaimable
	if memTotal > 0 {
		mem.UsagePercent = float64(mem.UsedBytes) * 100.0 / float64(memTotal)
	}

	swap.TotalBytes = swapTotal
	swap.UsedBytes = swapTotal - swapFree
	if swapTotal > 0 {
		swap.UsagePercent = float64(swap.UsedBytes) * 100.0 / float64(swapTotal)
	}

	return mem, swap
}

// collectNetworkInfo gathers network stats with proper locking to avoid races.
func (m *HostMonitor) collectNetworkInfo(ctx context.Context) ([]models.NetworkInfo, models.NetworkSummary) {
	var networks []models.NetworkInfo
	summary := models.NetworkSummary{}

	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return networks, summary
	}
	defer f.Close()

	now := time.Now()
	currentStats := make(map[string]*netCounters)

	// Batch fetch all interface IPs once (performance optimization)
	allIPs := getAllInterfaceIPs(ctx)

	scanner := bufio.NewScanner(f)
	scanner.Scan() // Skip header
	scanner.Scan()

	// First pass: collect current stats and build network list
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}

		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" {
			continue // Skip loopback
		}

		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}

		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)

		currentStats[iface] = &netCounters{rxBytes: rxBytes, txBytes: txBytes}

		state := getInterfaceState(iface)
		ipAddr := allIPs[iface] // Use batch-fetched IP
		macAddr := getInterfaceMAC(iface)
		isVirtual := isVirtualInterface(iface)
		networks = append(networks, models.NetworkInfo{
			Name:         iface,
			State:        state,
			IPAddress:    ipAddr,
			MACAddress:   macAddr,
			Virtual:      isVirtual,
			RxTotalBytes: rxBytes,
			TxTotalBytes: txBytes,
		})
	}

	// Second pass: calculate rates under a single lock hold
	m.mu.Lock()
	prevStats := m.lastNetStats
	prevTime := m.lastNetTime

	for i := range networks {
		net := &networks[i]
		if prev, ok := prevStats[net.Name]; ok && !prevTime.IsZero() {
			elapsed := now.Sub(prevTime).Seconds()
			if elapsed > 0 {
				curr := currentStats[net.Name]
				rxRate := float64(curr.rxBytes-prev.rxBytes) / elapsed
				txRate := float64(curr.txBytes-prev.txBytes) / elapsed
				if rxRate < 0 {
					rxRate = 0
				}
				if txRate < 0 {
					txRate = 0
				}
				net.RxBytesPerSec = rxRate
				net.TxBytesPerSec = txRate
				summary.RxBytesPerSec += rxRate
				summary.TxBytesPerSec += txRate
			}
		}
	}

	m.lastNetStats = currentStats
	m.lastNetTime = now
	m.mu.Unlock()

	return networks, summary
}

func isVirtualInterface(name string) bool {
	prefixes := []string{
		"docker", "veth", "br-", "virbr", "flannel", "cali", "tun", "tap",
		"vnet", "kube", "cni", "zt", "tailscale", "wg", "dummy", "ifb",
		"sit", "gre", "gretap", "ip6tnl", "vxlan", "geneve", "erspan",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func getInterfaceState(name string) string {
	path := fmt.Sprintf("/sys/class/net/%s/operstate", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	state := strings.TrimSpace(string(data))
	if state == "up" {
		return "up"
	}
	return "down"
}

func getInterfaceMAC(name string) string {
	path := fmt.Sprintf("/sys/class/net/%s/address", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// getAllInterfaceIPs batch-fetches IP addresses for all interfaces in one command.
// This is a performance optimization over calling getInterfaceIP() per interface.
// Returns a map of interface name -> IPv4 address.
func getAllInterfaceIPs(ctx context.Context) map[string]string {
	result := make(map[string]string)
	
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	
	runner := &sysutil.OSCommandRunner{}
	// Use -o (oneline) for easier parsing
	out, err := runner.Run(ctx, "ip", "-o", "addr", "show")
	if err != nil {
		return result
	}
	
	// Parse output: "2: eth0    inet 192.168.1.100/24 ..."
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		
		// Find "inet" field (IPv4)
		for i, field := range fields {
			if field == "inet" && i+1 < len(fields) {
				ifaceName := fields[1]
				// Remove trailing colon from interface name if present
				ifaceName = strings.TrimSuffix(ifaceName, ":")
				
				ipWithCIDR := fields[i+1]
				// Remove CIDR suffix
				if idx := strings.Index(ipWithCIDR, "/"); idx > 0 {
					result[ifaceName] = ipWithCIDR[:idx]
				} else {
					result[ifaceName] = ipWithCIDR
				}
				break // Only take first IPv4 per interface
			}
		}
	}
	
	return result
}

// collectDiskInfo gathers disk stats with proper locking to avoid races.
func (m *HostMonitor) collectDiskInfo(ctx context.Context) []models.HostDiskInfo {
	var disks []models.HostDiskInfo

	f, err := os.Open("/proc/mounts")
	if err != nil {
		return disks
	}
	defer f.Close()

	now := time.Now()
	diskIOStats := readDiskStats()
	currentDiskStats := make(map[string]*diskCounters)

	scanner := bufio.NewScanner(f)
	seen := make(map[string]bool)

	// First pass: collect disk info
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}

		device := fields[0]
		mount := fields[1]
		fsType := fields[2]

		if !strings.HasPrefix(device, "/dev/") {
			continue
		}
		if isPseudoFS(fsType) {
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true

		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}

		totalBytes := uint64(st.Blocks) * uint64(st.Bsize)
		availBytes := uint64(st.Bfree) * uint64(st.Bsize)
		usedBytes := totalBytes - availBytes

		devName := strings.TrimPrefix(device, "/dev/")
		disk := models.HostDiskInfo{
			Device:     device,
			Mount:      mount,
			FSType:     fsType,
			TotalBytes: totalBytes,
			UsedBytes:  usedBytes,
			AvailBytes: availBytes,
		}
		if totalBytes > 0 {
			disk.UsagePercent = float64(usedBytes) * 100.0 / float64(totalBytes)
		}

		if stats, ok := diskIOStats[devName]; ok {
			currentDiskStats[devName] = stats
		}

		disks = append(disks, disk)
	}

	// Second pass: calculate rates under a single lock hold
	m.mu.Lock()
	prevStats := m.lastDiskStats
	prevTime := m.lastDiskTime

	for i := range disks {
		disk := &disks[i]
		devName := strings.TrimPrefix(disk.Device, "/dev/")
		if stats, ok := currentDiskStats[devName]; ok {
			if prev, ok := prevStats[devName]; ok && !prevTime.IsZero() {
				elapsed := now.Sub(prevTime).Seconds()
				if elapsed > 0 {
					readRate := float64(stats.readSectors-prev.readSectors) * 512.0 / elapsed
					writeRate := float64(stats.writeSectors-prev.writeSectors) * 512.0 / elapsed
					if readRate < 0 {
						readRate = 0
					}
					if writeRate < 0 {
						writeRate = 0
					}
					disk.ReadBytesSec = readRate
					disk.WriteBytesSec = writeRate
				}
			}
		}
	}

	m.lastDiskStats = currentDiskStats
	m.lastDiskTime = now
	m.mu.Unlock()

	return disks
}

// getPhysicalDisk extracts the parent physical disk name from a partition device.
// Examples:
//   - /dev/sda1 → /dev/sda
//   - /dev/sda2 → /dev/sda
//   - /dev/nvme0n1p1 → /dev/nvme0n1
//   - /dev/nvme0n1p2 → /dev/nvme0n1
//   - /dev/mapper/vg-lv → /dev/mapper/vg-lv (LVM, keep as-is)
//   - /dev/md0 → /dev/md0 (RAID, keep as-is)
func getPhysicalDisk(device string) string {
	dev := strings.TrimPrefix(device, "/dev/")

	// Handle LVM, RAID, dm-* devices - keep as-is
	if strings.HasPrefix(dev, "mapper/") || strings.HasPrefix(dev, "md") || strings.HasPrefix(dev, "dm-") {
		return device
	}

	// Handle NVMe: nvme0n1p1 → nvme0n1
	if strings.HasPrefix(dev, "nvme") {
		if idx := strings.LastIndex(dev, "p"); idx > 0 {
			// Check if it's a partition suffix (p followed by digits at end)
			suffix := dev[idx+1:]
			isPartition := len(suffix) > 0
			for _, c := range suffix {
				if c < '0' || c > '9' {
					isPartition = false
					break
				}
			}
			if isPartition {
				return "/dev/" + dev[:idx]
			}
		}
		return device
	}

	// Handle SCSI/SATA: sda1 → sda, sdb2 → sdb
	// Pattern: letters followed by digits at the end
	for i := len(dev) - 1; i >= 0; i-- {
		if dev[i] < '0' || dev[i] > '9' {
			// Found non-digit, check if we have digits after it
			if i < len(dev)-1 {
				return "/dev/" + dev[:i+1]
			}
			break
		}
	}

	return device
}

// getPhysicalDiskSize reads the actual physical disk size from sysfs.
// Returns size in bytes. Falls back to 0 if unable to read.
func getPhysicalDiskSize(device string) uint64 {
	// Extract disk name from device path: /dev/sda → sda, /dev/nvme0n1 → nvme0n1
	diskName := strings.TrimPrefix(device, "/dev/")

	// Read size from /sys/block/{disk}/size (in 512-byte sectors)
	path := fmt.Sprintf("/sys/block/%s/size", diskName)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	sectors, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}

	return sectors * 512 // Convert sectors to bytes
}

// groupDisksByPhysicalDevice groups partition-level disk info by physical device.
// Structure: Physical Disk → Partitions → Mount Points
// Important: Bind mounts (same device, multiple paths) share the same underlying storage,
// so we only count UsedBytes once per unique partition device.
func groupDisksByPhysicalDevice(disks []models.HostDiskInfo) []models.DiskGroup {
	type partitionKey struct {
		physDev string
		partDev string
	}

	partMap := make(map[partitionKey]*models.Partition)
	groupMap := make(map[string]*models.DiskGroup)
	var groupOrder []string
	var partOrder []partitionKey

	// Track which partition devices we've already counted for usage
	countedPartitions := make(map[string]bool)

	for _, disk := range disks {
		physDev := getPhysicalDisk(disk.Device)
		key := partitionKey{physDev: physDev, partDev: disk.Device}

		// Create physical disk group if needed
		if _, exists := groupMap[physDev]; !exists {
			physSize := getPhysicalDiskSize(physDev)
			groupMap[physDev] = &models.DiskGroup{
				Device:     physDev,
				TotalBytes: physSize,
				Partitions: []models.Partition{},
			}
			groupOrder = append(groupOrder, physDev)
		}

		// Create partition if needed
		if _, exists := partMap[key]; !exists {
			partMap[key] = &models.Partition{
				Device: disk.Device,
				Mounts: []models.MountPoint{},
			}
			partOrder = append(partOrder, key)
		}

		// Add mount point to partition (all mounts are listed)
		partMap[key].Mounts = append(partMap[key].Mounts, models.MountPoint{
			Path:         disk.Mount,
			FSType:       disk.FSType,
			TotalBytes:   disk.TotalBytes,
			UsedBytes:    disk.UsedBytes,
			UsagePercent: disk.UsagePercent,
		})

		// Only count partition usage ONCE (bind mounts share the same filesystem)
		if !countedPartitions[disk.Device] {
			countedPartitions[disk.Device] = true

			// Set partition totals from the first (representative) mount
			partMap[key].TotalBytes = disk.TotalBytes
			partMap[key].UsedBytes = disk.UsedBytes
			partMap[key].ReadBytesSec = disk.ReadBytesSec
			partMap[key].WriteBytesSec = disk.WriteBytesSec

			// Add to physical disk totals
			groupMap[physDev].UsedBytes += disk.UsedBytes
			groupMap[physDev].ReadBytesSec += disk.ReadBytesSec
			groupMap[physDev].WriteBytesSec += disk.WriteBytesSec
		}
	}

	// Build result with proper ordering
	for _, physDev := range groupOrder {
		group := groupMap[physDev]

		// If sysfs read failed, calculate TotalBytes from unique partitions
		if group.TotalBytes == 0 {
			for _, part := range partMap {
				if getPhysicalDisk(part.Device) == physDev {
					group.TotalBytes += part.TotalBytes
				}
			}
		}

		if group.TotalBytes > 0 {
			group.UsagePercent = float64(group.UsedBytes) * 100.0 / float64(group.TotalBytes)
		}

		// Add partitions in order
		for _, key := range partOrder {
			if key.physDev != physDev {
				continue
			}
			part := partMap[key]
			if part.TotalBytes > 0 {
				part.UsagePercent = float64(part.UsedBytes) * 100 / float64(part.TotalBytes)
			}
			group.Partitions = append(group.Partitions, *part)
		}

		groupMap[physDev] = group
	}

	var result []models.DiskGroup
	for _, physDev := range groupOrder {
		result = append(result, *groupMap[physDev])
	}

	return result
}

func isPseudoFS(fsType string) bool {
	pseudoFS := []string{
		"tmpfs", "devtmpfs", "squashfs", "overlay", "proc", "sysfs",
		"cgroup", "cgroup2", "devpts", "securityfs", "pstore", "bpf",
		"tracefs", "debugfs", "mqueue", "hugetlbfs", "fusectl",
		"configfs", "autofs", "ramfs", "binfmt_misc", "nsfs", "efivarfs",
	}
	for _, p := range pseudoFS {
		if fsType == p || strings.HasPrefix(fsType, p) {
			return true
		}
	}
	return false
}

func readDiskStats() map[string]*diskCounters {
	stats := make(map[string]*diskCounters)

	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return stats
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}

		name := fields[2]
		// Skip partitions (only keep whole disks)
		if strings.HasPrefix(name, "sd") && len(name) > 3 {
			continue
		}
		if strings.HasPrefix(name, "nvme") && strings.Contains(name, "p") {
			continue
		}

		readSectors, _ := strconv.ParseUint(fields[5], 10, 64)
		writeSectors, _ := strconv.ParseUint(fields[9], 10, 64)

		stats[name] = &diskCounters{
			readSectors:  readSectors,
			writeSectors: writeSectors,
		}
	}

	return stats
}

// collectGPUInfo gathers NVIDIA GPU info using the command runner with timeouts.
func (m *HostMonitor) collectGPUInfo(ctx context.Context) *models.GPUInfo {
	if !commandExists(m.runner, "nvidia-smi") {
		return nil
	}

	gpu := &models.GPUInfo{Available: true}

	// Single call for device info
	cmdCtx, cancel := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel()

	out, err := m.runner.Run(cmdCtx, "nvidia-smi",
		"--query-gpu=index,uuid,name,driver_version,temperature.gpu,utilization.gpu,utilization.memory,memory.total,memory.used,memory.free,power.draw,power.limit,fan.speed,pstate",
		"--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) < 14 {
			continue
		}

		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}

		device := models.GPDevice{}
		device.Index, _ = parseUint32(fields[0])
		device.UUID = fields[1]
		device.Name = fields[2]
		if gpu.DriverVersion == "" {
			gpu.DriverVersion = fields[3]
		}

		if fields[4] != "[N/A]" && fields[4] != "" {
			if v, err := parseFloat64(fields[4]); err == nil {
				device.TemperatureC = &v
			}
		}
		if fields[5] != "[N/A]" && fields[5] != "" {
			if v, err := parseFloat64(fields[5]); err == nil {
				device.UtilGPU = &v
			}
		}
		if fields[6] != "[N/A]" && fields[6] != "" {
			if v, err := parseFloat64(fields[6]); err == nil {
				device.UtilMemory = &v
			}
		}

		device.MemoryTotalMB, _ = parseUint64(fields[7])
		device.MemoryUsedMB, _ = parseUint64(fields[8])
		device.MemoryFreeMB, _ = parseUint64(fields[9])

		if fields[10] != "[N/A]" && fields[10] != "" {
			if v, err := parseFloat64(fields[10]); err == nil {
				device.PowerDrawW = &v
			}
		}
		if fields[11] != "[N/A]" && fields[11] != "" {
			if v, err := parseFloat64(fields[11]); err == nil {
				device.PowerLimitW = &v
			}
		}
		if fields[12] != "[N/A]" && fields[12] != "" {
			if v, err := parseFloat64(fields[12]); err == nil {
				device.FanSpeedPercent = &v
			}
		}

		device.PState = fields[13]
		gpu.GPUs = append(gpu.GPUs, device)
	}

	// Get CUDA version from full output
	cmdCtx2, cancel2 := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel2()

	if out, err := m.runner.Run(cmdCtx2, "nvidia-smi"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "CUDA Version") {
				parts := strings.Split(line, ":")
				if len(parts) >= 2 {
					v := strings.TrimSpace(parts[1])
					v = strings.Fields(v)[0]
					gpu.CudaVersion = v
				}
				break
			}
		}
	}

	// Get GPU processes
	cmdCtx3, cancel3 := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel3()

	if out, err := m.runner.Run(cmdCtx3, "nvidia-smi",
		"--query-compute-apps=gpu_uuid,pid,used_gpu_memory,process_name",
		"--format=csv,noheader,nounits"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Split(line, ",")
			if len(fields) < 4 {
				continue
			}
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}

			proc := models.GPUProcess{
				Name: fields[3],
			}
			proc.PID, _ = parseUint32(fields[1])
			proc.UsedMemoryMB, _ = parseUint64(fields[2])

			for _, g := range gpu.GPUs {
				if g.UUID == fields[0] {
					proc.GPUIndex = g.Index
					break
				}
			}

			gpu.Processes = append(gpu.Processes, proc)
		}
	}

	return gpu
}

func (m *HostMonitor) collectNPUInfo(ctx context.Context) *models.NPUInfo {
	npuSmiPath := findNPUSMI(m.runner)
	if npuSmiPath == "" {
		return nil
	}

	npu := &models.NPUInfo{Available: true}

	cmdCtx, cancel := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel()

	out, err := m.runner.Run(cmdCtx, npuSmiPath, "info")
	if err != nil {
		return nil
	}

	output := string(out)
	if strings.Contains(output, "NPU ID") {
		npu.DriverVersion = "unknown"
		npu.CannVersion = findCANNVersion()
	}

	return npu
}

func findNPUSMI(runner sysutil.CommandRunner) string {
	paths := []string{
		"/usr/local/bin/npu-smi",
		"/usr/local/Ascend/driver/tools/npu-smi",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	if commandExists(runner, "npu-smi") {
		return "npu-smi"
	}

	return ""
}

func findCANNVersion() string {
	paths := []string{
		"/usr/local/Ascend",
		os.Getenv("HOME") + "/Ascend",
		os.Getenv("ASCEND_HOME_PATH"),
	}

	for _, base := range paths {
		if base == "" {
			continue
		}
		for _, infoFile := range []string{"ascend_toolkit_install.info", "ascend_nnae_install.info", "ascend_nnrt_install.info"} {
			if data, err := os.ReadFile(base + "/" + infoFile); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "version=") {
						return strings.TrimPrefix(line, "version=")
					}
				}
			}
		}
	}

	return ""
}

// collectDockerInfo gathers Docker information using the Go SDK for better performance.
// Falls back to nil if Docker is not available.
func (m *HostMonitor) collectDockerInfo(ctx context.Context) *models.DockerInfo {
	return m.collectDockerInfoSDK(ctx)
}

func (m *HostMonitor) collectUserInfo(ctx context.Context) []models.UserInfo {
	var users []models.UserInfo

	cmdCtx, cancel := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel()

	out, err := m.runner.Run(cmdCtx, "who")
	if err != nil {
		return users
	}

	now := time.Now()

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		user := models.UserInfo{
			Name:     fields[0],
			Terminal: fields[1],
		}

		if len(fields) >= 4 {
			timeStr := fields[2] + " " + fields[3]
			user.LoginTime = timeStr

			// Try parsing with different formats that `who` may output.
			// Use ParseInLocation to ensure the parsed time is in the same
			// location as `now`, avoiding timezone mismatches that would
			// cause incorrect duration calculations.
			var loginTime time.Time
			var parseErr error

			// Format: "2024-01-15 10:30" (ISO-like with year)
			if loginTime, parseErr = time.ParseInLocation("2006-01-02 15:04", timeStr, now.Location()); parseErr != nil {
				// Format: "Jan 15 10:30" (month abbreviation, no year - recent login)
				if loginTime, parseErr = time.ParseInLocation("Jan 02 15:04", timeStr, now.Location()); parseErr != nil {
					// Format: "01-15 10:30" (numeric month-day, no year)
					if loginTime, parseErr = time.ParseInLocation("01-02 15:04", timeStr, now.Location()); parseErr == nil {
						// Set the year to current year since it's not in the format
						loginTime = time.Date(now.Year(), loginTime.Month(), loginTime.Day(),
							loginTime.Hour(), loginTime.Minute(), 0, 0, now.Location())
					}
				} else {
					// "Jan 02" format succeeded - set the year to current year
					loginTime = time.Date(now.Year(), loginTime.Month(), loginTime.Day(),
						loginTime.Hour(), loginTime.Minute(), 0, 0, now.Location())
				}
			}

			if parseErr == nil {
				user.DurationSec = uint64(now.Sub(loginTime).Seconds())
			}
		}

		if len(fields) >= 5 && strings.HasPrefix(fields[4], "(") {
			user.From = strings.Trim(fields[4], "()")
		}

		users = append(users, user)
	}

	return users
}

// CollectProcesses returns the list of running processes.
func (m *HostMonitor) CollectProcesses(ctx context.Context) []models.ProcessInfo {
	return m.collectProcessInfo(ctx)
}

func (m *HostMonitor) collectProcessInfo(ctx context.Context) []models.ProcessInfo {
	var procs []models.ProcessInfo

	cmdCtx, cancel := context.WithTimeout(ctx, externalCmdTimeout)
	defer cancel()

	out, err := m.runner.Run(cmdCtx, "ps", "-eo", "pid,ppid,user,stat,%cpu,%mem,rss,etime,comm")
	if err != nil {
		return procs
	}

	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i == 0 || line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		proc := models.ProcessInfo{
			User:    fields[2],
			State:   fields[3],
			Command: fields[8],
			Elapsed: fields[7],
		}

		proc.PID, _ = parseUint32(fields[0])
		proc.PPID, _ = parseUint32(fields[1])
		proc.CPU, _ = strconv.ParseFloat(fields[4], 64)
		proc.Memory, _ = strconv.ParseFloat(fields[5], 64)

		rssKB, _ := strconv.ParseUint(fields[6], 10, 64)
		proc.RSSBytes = rssKB * 1024

		procs = append(procs, proc)
	}

	return procs
}

// updateHistory adds current samples to history and prunes stale entries.
func (m *HostMonitor) updateHistory(resp *models.HostMetricsResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := resp.SampledAt.Unix()

	m.history.cpu = append(m.history.cpu, models.HistoryPoint{
		Timestamp: now,
		Value:     resp.CPU.UsagePercent,
	})

	m.history.memory = append(m.history.memory, models.HistoryPoint{
		Timestamp: now,
		Value:     resp.Memory.UsagePercent,
	})

	// Build set of current network/disk names for pruning
	currentNets := make(map[string]bool)
	for _, net := range resp.Networks {
		currentNets[net.Name] = true
		m.history.networks[net.Name] = append(m.history.networks[net.Name], models.NetworkHistoryPoint{
			Timestamp: now,
			RxBytes:   net.RxBytesPerSec,
			TxBytes:   net.TxBytesPerSec,
		})
	}

	currentDisks := make(map[string]bool)
	for _, disk := range resp.Disks {
		currentDisks[disk.Device] = true
		m.history.disks[disk.Device] = append(m.history.disks[disk.Device], models.DiskHistoryPoint{
			Timestamp:  now,
			ReadBytes:  disk.ReadBytesSec,
			WriteBytes: disk.WriteBytesSec,
		})
	}

	// Trim to max size
	m.historySize++
	if len(m.history.cpu) > maxHistorySize {
		m.history.cpu = m.history.cpu[len(m.history.cpu)-maxHistorySize:]
	}
	if len(m.history.memory) > maxHistorySize {
		m.history.memory = m.history.memory[len(m.history.memory)-maxHistorySize:]
	}

	// Trim and prune network history
	for k, v := range m.history.networks {
		if !currentNets[k] {
			delete(m.history.networks, k)
			continue
		}
		if len(v) > maxHistorySize {
			m.history.networks[k] = v[len(v)-maxHistorySize:]
		}
	}

	// Trim and prune disk history
	for k, v := range m.history.disks {
		if !currentDisks[k] {
			delete(m.history.disks, k)
			continue
		}
		if len(v) > maxHistorySize {
			m.history.disks[k] = v[len(v)-maxHistorySize:]
		}
	}
}

func (m *HostMonitor) getHistory() models.MetricsHistory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return copies to avoid data races
	cpu := make([]models.HistoryPoint, len(m.history.cpu))
	copy(cpu, m.history.cpu)

	mem := make([]models.HistoryPoint, len(m.history.memory))
	copy(mem, m.history.memory)

	nets := make(map[string][]models.NetworkHistoryPoint)
	for k, v := range m.history.networks {
		nets[k] = make([]models.NetworkHistoryPoint, len(v))
		copy(nets[k], v)
	}

	disks := make(map[string][]models.DiskHistoryPoint)
	for k, v := range m.history.disks {
		disks[k] = make([]models.DiskHistoryPoint, len(v))
		copy(disks[k], v)
	}

	return models.MetricsHistory{
		CPU:      cpu,
		Memory:   mem,
		Networks: nets,
		Disks:    disks,
	}
}

// Helper functions

func parseUint32(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err
}

func parseUint64(s string) (uint64, error) {
	return strconv.ParseUint(s, 10, 64)
}

func parseFloat64(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// commandExists checks if a command is available using which.
func commandExists(runner sysutil.CommandRunner, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := runner.Run(ctx, "which", name)
	return err == nil
}
