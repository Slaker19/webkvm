package nodes

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// LocalCapacityFunc reads this host's CPUs and free/total RAM (bytes). It
// must be a cheap local read (no libvirt, no network): it runs at startup,
// on every local probe and on every cluster summary.
type LocalCapacityFunc func() (cpus int, ramFree, ramTotal int64, err error)

// ReadLocalCapacity is the production LocalCapacityFunc: CPUs usable by this
// process and RAM from /proc/meminfo (MemAvailable, else MemFree).
func ReadLocalCapacity() (int, int64, int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, err
	}
	free, total, err := parseMeminfo(string(data))
	if err != nil {
		return 0, 0, 0, err
	}
	return runtime.NumCPU(), free, total, nil
}

// parseMeminfo returns free (MemAvailable, falling back to MemFree) and
// total RAM in bytes from /proc/meminfo content.
func parseMeminfo(content string) (free, total int64, err error) {
	var memFree, avail int64 = -1, -1
	for _, line := range strings.Split(content, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, perr := strconv.ParseInt(fields[0], 10, 64)
		if perr != nil {
			continue
		}
		v *= 1024 // values are in kB
		switch strings.TrimSpace(key) {
		case "MemTotal":
			total = v
		case "MemFree":
			memFree = v
		case "MemAvailable":
			avail = v
		}
	}
	if total <= 0 {
		return 0, 0, errors.New("meminfo: no MemTotal")
	}
	free = avail
	if free < 0 {
		free = memFree
	}
	if free < 0 {
		free = 0
	}
	return free, total, nil
}

// SetLocalCapacityReader wires the local capacity reader and refreshes the
// local entry immediately, so the first listing after startup (or after a
// control-plane failover) already shows this host's hardware.
func (r *Registry) SetLocalCapacityReader(fn LocalCapacityFunc) {
	r.mu.Lock()
	r.localCapacity = fn
	r.mu.Unlock()
	r.RefreshLocalCapacity()
}

// RefreshLocalCapacity re-reads this host's CPUs/RAM into the local entry.
// A failed read keeps the last good values. No-op without a reader.
func (r *Registry) RefreshLocalCapacity() {
	r.mu.RLock()
	fn := r.localCapacity
	r.mu.RUnlock()
	if fn == nil {
		return
	}
	cpus, free, total, err := fn()
	if err != nil || total <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nodes {
		if n.IsLocal() {
			n.CPUs, n.RAMFree, n.RAMTotal = cpus, free, total
		}
	}
}
