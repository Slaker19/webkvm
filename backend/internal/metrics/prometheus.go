package metrics

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"

	"webkvm/internal/models"
)

// HostTelemetry holds the latest host-level readings for Prometheus export.
type HostTelemetry struct {
	CPUUsage  float64
	UsedRAM   int64
	TotalRAM  int64
	UsedDisk  int64
	TotalDisk int64
	NetRx     int64
	NetTx     int64
	Uptime    int64
}

// PrometheusData bundles all data required to render the OpenMetrics / Prometheus scrape.
type PrometheusData struct {
	Version     string
	Host        *HostTelemetry
	VMs         []models.VM
	VMMetrics   map[string]models.VMMetrics
	Pools       []models.StoragePool
}

// PrometheusExporter formats metric series according to the Prometheus text-based format.
type PrometheusExporter struct {
	buf bytes.Buffer
}

// escapeLabelValue escapes special characters in label values according to OpenMetrics spec.
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// WriteHeader outputs the standard # HELP and # TYPE descriptor lines.
func (e *PrometheusExporter) WriteHeader(name, help, metricType string) {
	e.buf.WriteString("# HELP ")
	e.buf.WriteString(name)
	e.buf.WriteByte(' ')
	e.buf.WriteString(help)
	e.buf.WriteByte('\n')
	e.buf.WriteString("# TYPE ")
	e.buf.WriteString(name)
	e.buf.WriteByte(' ')
	e.buf.WriteString(metricType)
	e.buf.WriteByte('\n')
}

// WriteSample outputs a single measurement with optional sorted labels.
func (e *PrometheusExporter) WriteSample(name string, labels map[string]string, val float64) {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		val = 0
	}
	e.buf.WriteString(name)
	if len(labels) > 0 {
		e.buf.WriteByte('{')
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				e.buf.WriteByte(',')
			}
			e.buf.WriteString(k)
			e.buf.WriteString(`="`)
			e.buf.WriteString(escapeLabelValue(labels[k]))
			e.buf.WriteByte('"')
		}
		e.buf.WriteByte('}')
	}
	e.buf.WriteByte(' ')
	e.buf.WriteString(strconv.FormatFloat(val, 'f', -1, 64))
	e.buf.WriteByte('\n')
}

// RenderPrometheus serializes telemetry data into a complete Prometheus text response.
func RenderPrometheus(d PrometheusData) []byte {
	exp := &PrometheusExporter{}

	// 1. Process Info & Up
	exp.WriteHeader("webkvm_info", "WebKVM build and release metadata", "gauge")
	version := d.Version
	if version == "" {
		version = "unknown"
	}
	exp.WriteSample("webkvm_info", map[string]string{"version": version}, 1)

	exp.WriteHeader("webkvm_up", "Reports 1 if the WebKVM daemon is operational", "gauge")
	exp.WriteSample("webkvm_up", nil, 1)

	// 2. Host Telemetry
	if d.Host != nil {
		exp.WriteHeader("webkvm_host_cpu_usage_percent", "System-wide host CPU utilization in percent (0-100)", "gauge")
		exp.WriteSample("webkvm_host_cpu_usage_percent", nil, d.Host.CPUUsage)

		exp.WriteHeader("webkvm_host_memory_total_bytes", "Total host physical RAM in bytes", "gauge")
		exp.WriteSample("webkvm_host_memory_total_bytes", nil, float64(d.Host.TotalRAM))

		exp.WriteHeader("webkvm_host_memory_used_bytes", "Currently utilized host RAM in bytes", "gauge")
		exp.WriteSample("webkvm_host_memory_used_bytes", nil, float64(d.Host.UsedRAM))

		exp.WriteHeader("webkvm_host_memory_free_bytes", "Available/free host RAM in bytes", "gauge")
		freeRAM := d.Host.TotalRAM - d.Host.UsedRAM
		if freeRAM < 0 {
			freeRAM = 0
		}
		exp.WriteSample("webkvm_host_memory_free_bytes", nil, float64(freeRAM))

		if d.Host.TotalDisk > 0 {
			exp.WriteHeader("webkvm_host_disk_total_bytes", "Total root disk capacity in bytes", "gauge")
			exp.WriteSample("webkvm_host_disk_total_bytes", nil, float64(d.Host.TotalDisk))

			exp.WriteHeader("webkvm_host_disk_used_bytes", "Utilized root disk capacity in bytes", "gauge")
			exp.WriteSample("webkvm_host_disk_used_bytes", nil, float64(d.Host.UsedDisk))
		}

		exp.WriteHeader("webkvm_host_net_rx_bytes_per_sec", "Aggregate incoming network bandwidth in bytes per second", "gauge")
		exp.WriteSample("webkvm_host_net_rx_bytes_per_sec", nil, float64(d.Host.NetRx))

		exp.WriteHeader("webkvm_host_net_tx_bytes_per_sec", "Aggregate outgoing network bandwidth in bytes per second", "gauge")
		exp.WriteSample("webkvm_host_net_tx_bytes_per_sec", nil, float64(d.Host.NetTx))

		if d.Host.Uptime > 0 {
			exp.WriteHeader("webkvm_host_uptime_seconds", "Total uptime of the host system in seconds", "counter")
			exp.WriteSample("webkvm_host_uptime_seconds", nil, float64(d.Host.Uptime))
		}
	}

	// 3. VM Aggregates
	exp.WriteHeader("webkvm_vms_total", "Total registered compute instances (VMs and containers)", "gauge")
	exp.WriteSample("webkvm_vms_total", nil, float64(len(d.VMs)))

	counts := map[string]int{"running": 0, "shutoff": 0, "paused": 0, "other": 0}
	for _, vm := range d.VMs {
		state := strings.ToLower(string(vm.State))
		if _, ok := counts[state]; ok {
			counts[state]++
		} else {
			counts["other"]++
		}
	}

	exp.WriteHeader("webkvm_vms_state", "Number of instances grouped by execution state", "gauge")
	for state, cnt := range counts {
		exp.WriteSample("webkvm_vms_state", map[string]string{"state": state}, float64(cnt))
	}

	// 4. Per-VM Detailed Metrics
	if len(d.VMs) > 0 {
		exp.WriteHeader("webkvm_vm_status", "Active status indicator per instance (1 if listed)", "gauge")
		for _, vm := range d.VMs {
			kind := vm.Type
			if kind == "" {
				kind = "vm"
			}
			exp.WriteSample("webkvm_vm_status", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
				"type":  kind,
				"state": strings.ToLower(string(vm.State)),
			}, 1)
		}

		exp.WriteHeader("webkvm_vm_vcpus", "Configured virtual CPU count per instance", "gauge")
		for _, vm := range d.VMs {
			exp.WriteSample("webkvm_vm_vcpus", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
			}, float64(vm.VCPUs))
		}

		exp.WriteHeader("webkvm_vm_cpu_usage_percent", "Current CPU utilization percentage for the instance", "gauge")
		for _, vm := range d.VMs {
			exp.WriteSample("webkvm_vm_cpu_usage_percent", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
			}, vm.CPUUsage)
		}

		exp.WriteHeader("webkvm_vm_memory_allocated_bytes", "Allocated RAM for the instance in bytes", "gauge")
		for _, vm := range d.VMs {
			exp.WriteSample("webkvm_vm_memory_allocated_bytes", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
			}, float64(vm.RAMMB*1024*1024))
		}

		exp.WriteHeader("webkvm_vm_memory_used_bytes", "Estimated or guest-reported memory usage in bytes", "gauge")
		for _, vm := range d.VMs {
			usedBytes := vm.RAMUsedMB * 1024 * 1024
			exp.WriteSample("webkvm_vm_memory_used_bytes", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
			}, float64(usedBytes))
		}

		exp.WriteHeader("webkvm_vm_disk_size_bytes", "Total disk capacity assigned to the instance in bytes", "gauge")
		for _, vm := range d.VMs {
			exp.WriteSample("webkvm_vm_disk_size_bytes", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
			}, float64(vm.DiskGB*1024*1024*1024))
		}

		exp.WriteHeader("webkvm_vm_uptime_seconds", "Total runtime of the instance in seconds", "gauge")
		for _, vm := range d.VMs {
			exp.WriteSample("webkvm_vm_uptime_seconds", map[string]string{
				"vm_id": vm.ID,
				"name":  vm.Name,
			}, float64(vm.UptimeSec))
		}

		// Live I/O rates from MetricsCollector if available
		if len(d.VMMetrics) > 0 {
			exp.WriteHeader("webkvm_vm_disk_read_bytes_per_sec", "Disk read rate in bytes per second", "gauge")
			for _, vm := range d.VMs {
				if m, ok := d.VMMetrics[vm.ID]; ok && len(m.DiskRead.Points) > 0 {
					lastVal := m.DiskRead.Points[len(m.DiskRead.Points)-1].V
					exp.WriteSample("webkvm_vm_disk_read_bytes_per_sec", map[string]string{"vm_id": vm.ID, "name": vm.Name}, lastVal)
				}
			}

			exp.WriteHeader("webkvm_vm_disk_write_bytes_per_sec", "Disk write rate in bytes per second", "gauge")
			for _, vm := range d.VMs {
				if m, ok := d.VMMetrics[vm.ID]; ok && len(m.DiskWrite.Points) > 0 {
					lastVal := m.DiskWrite.Points[len(m.DiskWrite.Points)-1].V
					exp.WriteSample("webkvm_vm_disk_write_bytes_per_sec", map[string]string{"vm_id": vm.ID, "name": vm.Name}, lastVal)
				}
			}

			exp.WriteHeader("webkvm_vm_net_rx_bytes_per_sec", "Network receive rate in bytes per second", "gauge")
			for _, vm := range d.VMs {
				if m, ok := d.VMMetrics[vm.ID]; ok && len(m.NetRx.Points) > 0 {
					lastVal := m.NetRx.Points[len(m.NetRx.Points)-1].V
					exp.WriteSample("webkvm_vm_net_rx_bytes_per_sec", map[string]string{"vm_id": vm.ID, "name": vm.Name}, lastVal)
				}
			}

			exp.WriteHeader("webkvm_vm_net_tx_bytes_per_sec", "Network transmit rate in bytes per second", "gauge")
			for _, vm := range d.VMs {
				if m, ok := d.VMMetrics[vm.ID]; ok && len(m.NetTx.Points) > 0 {
					lastVal := m.NetTx.Points[len(m.NetTx.Points)-1].V
					exp.WriteSample("webkvm_vm_net_tx_bytes_per_sec", map[string]string{"vm_id": vm.ID, "name": vm.Name}, lastVal)
				}
			}
		}
	}

	// 5. Storage Pools
	if len(d.Pools) > 0 {
		exp.WriteHeader("webkvm_storage_pool_capacity_bytes", "Storage pool total capacity in bytes", "gauge")
		for _, p := range d.Pools {
			exp.WriteSample("webkvm_storage_pool_capacity_bytes", map[string]string{
				"pool": p.Name,
				"type": p.Type,
			}, float64(p.Capacity))
		}

		exp.WriteHeader("webkvm_storage_pool_allocation_bytes", "Storage pool allocated space in bytes", "gauge")
		for _, p := range d.Pools {
			exp.WriteSample("webkvm_storage_pool_allocation_bytes", map[string]string{
				"pool": p.Name,
				"type": p.Type,
			}, float64(p.Allocated))
		}

		exp.WriteHeader("webkvm_storage_pool_available_bytes", "Storage pool remaining free space in bytes", "gauge")
		for _, p := range d.Pools {
			exp.WriteSample("webkvm_storage_pool_available_bytes", map[string]string{
				"pool": p.Name,
				"type": p.Type,
			}, float64(p.Available))
		}

		exp.WriteHeader("webkvm_storage_pool_active", "Reports 1 if the storage pool is active, 0 otherwise", "gauge")
		for _, p := range d.Pools {
			active := 0.0
			if strings.EqualFold(p.State, "running") || strings.EqualFold(p.State, "active") {
				active = 1.0
			}
			exp.WriteSample("webkvm_storage_pool_active", map[string]string{
				"pool": p.Name,
				"type": p.Type,
			}, active)
		}
	}

	return exp.buf.Bytes()
}
