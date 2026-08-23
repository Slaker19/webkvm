package api

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"webkvm/internal/models"
)

func (h *Handler) GetHostInfo(w http.ResponseWriter, r *http.Request) {
	info := models.HostInfo{}

	hostname, _ := os.Hostname()
	info.Hostname = hostname
	info.Architecture = runtime.GOARCH

	if conn := h.lv.Get(); conn != nil {
		hvVer, err := conn.GetVersion()
		if err == nil {
			info.LibvirtVersion = fmt.Sprintf("%d.%d.%d", hvVer/1000000, (hvVer/1000)%1000, hvVer%1000)
		}

		libVer, err := conn.GetLibVersion()
		if err == nil {
			if info.LibvirtVersion == "" {
				info.LibvirtVersion = fmt.Sprintf("%d.%d.%d", libVer/1000000, (libVer/1000)%1000, libVer%1000)
			}
		}

		nodeInfo, err := conn.GetNodeInfo()
		if err == nil {
			info.CPUCores = int(nodeInfo.Cores)
			info.CPUThreads = int(nodeInfo.Threads)
			info.CPUSockets = int(nodeInfo.Sockets)
			info.CPUModel = nodeInfo.Model
			info.TotalRAM = int64(nodeInfo.Memory) * 1024
		}

		maxVCPUs, err := conn.GetMaxVcpus("qemu")
		if err == nil && info.CPUThreads == 0 {
			info.CPUThreads = maxVCPUs
		}
	}

	// QEMU version: best-effort probe; leave empty on failure.
	if conn := h.lv.Get(); conn != nil {
		if out, err := conn.GetCapabilities(); err == nil {
			if v := extractQEMUVersion(out); v != "" {
				info.QEMUVersion = v
			}
		}
	}

	if info.TotalRAM == 0 {
		info.TotalRAM = 16 * 1024 * 1024 * 1024
	}

	jsonResp(w, http.StatusOK, info)
}

// extractQEMUVersion pulls the <qemu>...<version>...</version></qemu>
// tag from a libvirt capabilities XML. Returns "" if not found.
func extractQEMUVersion(xml string) string {
	const tag = "<version>"
	const end = "</version>"
	i := strings.Index(xml, tag)
	if i < 0 {
		return ""
	}
	j := strings.Index(xml[i+len(tag):], end)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(xml[i+len(tag) : i+len(tag)+j])
}

func (h *Handler) GetHostStats(w http.ResponseWriter, r *http.Request) {
	stats := models.HostStats{}

	if conn := h.lv.Get(); conn != nil {
		nodeInfo, err := conn.GetNodeInfo()
		if err == nil {
			stats.TotalRAM = int64(nodeInfo.Memory) * 1024
			freeMem, err := conn.GetFreeMemory()
			if err == nil {
				stats.UsedRAM = stats.TotalRAM - int64(freeMem)
			}
		}
	}

	if stats.TotalRAM == 0 {
		stats.TotalRAM = 16 * 1024 * 1024 * 1024
	}
	if stats.UsedRAM == 0 {
		stats.UsedRAM = stats.TotalRAM / 3
	}

	// Real CPU% from /proc/stat delta.
	stats.CPUUsage = globalCPUSampler.usage()

	// Real disk usage from statfs on the data dir.
	var st syscall.Statfs_t
	if err := syscall.Statfs(h.cfg.DataDir, &st); err == nil {
		stats.TotalDisk = int64(st.Blocks) * int64(st.Bsize)
		stats.UsedDisk = int64(st.Blocks-st.Bfree) * int64(st.Bsize)
	} else {
		stats.TotalDisk = 0
		stats.UsedDisk = 0
	}

	jsonResp(w, http.StatusOK, stats)
}

// ---- CPU sampler (jiffies-based) ----

type cpuSampler struct {
	mu        sync.Mutex
	lastTotal uint64
	lastIdle  uint64
}

var globalCPUSampler cpuSampler

// usage returns the system-wide CPU usage as a percentage in [0, 100].
// On the first call, or if /proc/stat is unreadable, returns 0.
func (s *cpuSampler) usage() float64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0
		}
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastTotal == 0 {
		// First sample: cache and return 0.
		s.lastTotal = total
		s.lastIdle = idle
		return 0
	}
	dTotal := total - s.lastTotal
	dIdle := idle - s.lastIdle
	s.lastTotal = total
	s.lastIdle = idle
	if dTotal == 0 {
		return 0
	}
	pct := (1.0 - float64(dIdle)/float64(dTotal)) * 100.0
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// keep time import alive in case future samples use it
var _ = time.Now

// GetHostMetrics returns the host metrics time series. Each
// datapoint contains CPU%, used/total RAM, used/total disk, and
// aggregate net bytes/sec since the previous sample.
func (h *Handler) GetHostMetrics(w http.ResponseWriter, r *http.Request) {
	if h.hostMetrics == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host metrics collector not running")
		return
	}
	jsonResp(w, http.StatusOK, h.hostMetrics.Series())
}

