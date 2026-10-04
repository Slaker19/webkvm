package metrics

import (
	"encoding/json"
	"strings"
)

// GenerateGrafanaDashboard produces a production-ready, dark-themed Grafana
// dashboard JSON (schemaVersion 38) pre-wired with WebKVM Prometheus metrics.
func GenerateGrafanaDashboard() []byte {
	dashboard := map[string]any{
		"annotations": map[string]any{
			"list": []any{},
		},
		"editable":             true,
		"fiscalYearStartMonth": 0,
		"graphTooltip":         1,
		"id":                   nil,
		"links":                []any{},
		"liveNow":              false,
		"refresh":              "10s",
		"schemaVersion":        38,
		"style":                "dark",
		"tags":                 []string{"webkvm", "kvm", "virtualization", "libvirt"},
		"templating": map[string]any{
			"list": []any{},
		},
		"time": map[string]string{
			"from": "now-1h",
			"to":   "now",
		},
		"timepicker": map[string]any{},
		"timezone":   "browser",
		"title":      "WebKVM - Infrastructure & VMs Overview",
		"uid":        "webkvm-overview",
		"version":    1,
		"panels": []map[string]any{
			// Row: Host Status & Health
			{
				"collapsed": false,
				"gridPos":   map[string]int{"h": 1, "w": 24, "x": 0, "y": 0},
				"id":        100,
				"title":     "Host Status & Health",
				"type":      "row",
			},
			// Panel: Daemon Status
			{
				"id":    1,
				"title": "WebKVM Daemon",
				"type":  "stat",
				"gridPos": map[string]int{
					"h": 4, "w": 4, "x": 0, "y": 1,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"color": map[string]string{"mode": "thresholds"},
						"mappings": []map[string]any{
							{
								"type": "value",
								"options": map[string]any{
									"1": map[string]any{"text": "ONLINE", "color": "green"},
									"0": map[string]any{"text": "OFFLINE", "color": "red"},
								},
							},
						},
						"thresholds": map[string]any{
							"mode": "absolute",
							"steps": []map[string]any{
								{"color": "red", "value": nil},
								{"color": "green", "value": 1},
							},
						},
					},
				},
				"options": map[string]any{
					"colorMode":   "background",
					"graphMode":   "none",
					"justifyMode": "center",
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_up",
						"legendFormat": "Status",
						"refId":        "A",
					},
				},
			},
			// Panel: Host CPU %
			{
				"id":    2,
				"title": "Host CPU Usage",
				"type":  "gauge",
				"gridPos": map[string]int{
					"h": 4, "w": 4, "x": 4, "y": 1,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"min":  0,
						"max":  100,
						"unit": "percent",
						"thresholds": map[string]any{
							"mode": "absolute",
							"steps": []map[string]any{
								{"color": "green", "value": nil},
								{"color": "semi-dark-yellow", "value": 75},
								{"color": "red", "value": 90},
							},
						},
					},
				},
				"options": map[string]any{
					"showThresholdLabels":  false,
					"showThresholdMarkers": true,
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_host_cpu_usage_percent",
						"legendFormat": "Host CPU",
						"refId":        "A",
					},
				},
			},
			// Panel: Host RAM %
			{
				"id":    3,
				"title": "Host RAM Usage",
				"type":  "gauge",
				"gridPos": map[string]int{
					"h": 4, "w": 4, "x": 8, "y": 1,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"min":  0,
						"max":  100,
						"unit": "percent",
						"thresholds": map[string]any{
							"mode": "absolute",
							"steps": []map[string]any{
								{"color": "green", "value": nil},
								{"color": "semi-dark-yellow", "value": 80},
								{"color": "red", "value": 90},
							},
						},
					},
				},
				"options": map[string]any{
					"showThresholdLabels":  false,
					"showThresholdMarkers": true,
				},
				"targets": []map[string]any{
					{
						"expr":         "(webkvm_host_memory_used_bytes / webkvm_host_memory_total_bytes) * 100",
						"legendFormat": "RAM Used",
						"refId":        "A",
					},
				},
			},
			// Panel: Host Root Disk %
			{
				"id":    4,
				"title": "Host Disk Usage",
				"type":  "gauge",
				"gridPos": map[string]int{
					"h": 4, "w": 4, "x": 12, "y": 1,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"min":  0,
						"max":  100,
						"unit": "percent",
						"thresholds": map[string]any{
							"mode": "absolute",
							"steps": []map[string]any{
								{"color": "green", "value": nil},
								{"color": "semi-dark-yellow", "value": 75},
								{"color": "red", "value": 85},
							},
						},
					},
				},
				"options": map[string]any{
					"showThresholdLabels":  false,
					"showThresholdMarkers": true,
				},
				"targets": []map[string]any{
					{
						"expr":         "(webkvm_host_disk_used_bytes / webkvm_host_disk_total_bytes) * 100",
						"legendFormat": "Disk Used",
						"refId":        "A",
					},
				},
			},
			// Panel: Running VMs
			{
				"id":    5,
				"title": "Running Instances",
				"type":  "stat",
				"gridPos": map[string]int{
					"h": 4, "w": 4, "x": 16, "y": 1,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"color": map[string]string{"mode": "fixed", "fixedColor": "green"},
					},
				},
				"options": map[string]any{
					"colorMode":   "value",
					"graphMode":   "none",
					"justifyMode": "center",
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_vms_state{state=\"running\"}",
						"legendFormat": "Running",
						"refId":        "A",
					},
				},
			},
			// Panel: Total VMs
			{
				"id":    6,
				"title": "Total Instances",
				"type":  "stat",
				"gridPos": map[string]int{
					"h": 4, "w": 4, "x": 20, "y": 1,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"color": map[string]string{"mode": "fixed", "fixedColor": "blue"},
					},
				},
				"options": map[string]any{
					"colorMode":   "value",
					"graphMode":   "none",
					"justifyMode": "center",
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_vms_total",
						"legendFormat": "Total",
						"refId":        "A",
					},
				},
			},

			// Row: Host Resource History
			{
				"collapsed": false,
				"gridPos":   map[string]int{"h": 1, "w": 24, "x": 0, "y": 5},
				"id":        101,
				"title":     "Host Resource History",
				"type":      "row",
			},
			// Timeseries: Host CPU
			{
				"id":    7,
				"title": "Host CPU Utilization",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 8, "x": 0, "y": 6,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "percent",
						"min":  0,
						"max":  100,
						"custom": map[string]any{
							"fillOpacity": 20,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_host_cpu_usage_percent",
						"legendFormat": "Host CPU %",
						"refId":        "A",
					},
				},
			},
			// Timeseries: Host Memory
			{
				"id":    8,
				"title": "Host Memory Breakdown",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 8, "x": 8, "y": 6,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "bytes",
						"custom": map[string]any{
							"fillOpacity": 20,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_host_memory_used_bytes",
						"legendFormat": "Used RAM",
						"refId":        "A",
					},
					{
						"expr":         "webkvm_host_memory_total_bytes",
						"legendFormat": "Total RAM",
						"refId":        "B",
					},
				},
			},
			// Timeseries: Host Network Bandwidth
			{
				"id":    9,
				"title": "Host Network Throughput",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 8, "x": 16, "y": 6,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "Bps",
						"custom": map[string]any{
							"fillOpacity": 15,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_host_net_rx_bytes_per_sec",
						"legendFormat": "Rx (Receive)",
						"refId":        "A",
					},
					{
						"expr":         "webkvm_host_net_tx_bytes_per_sec",
						"legendFormat": "Tx (Transmit)",
						"refId":        "B",
					},
				},
			},

			// Row: Compute Instances (VMs & Containers)
			{
				"collapsed": false,
				"gridPos":   map[string]int{"h": 1, "w": 24, "x": 0, "y": 13},
				"id":        102,
				"title":     "Compute Instances (VMs & Containers)",
				"type":      "row",
			},
			// Timeseries: VM CPU Utilization
			{
				"id":    10,
				"title": "Instance CPU Usage (%)",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 12, "x": 0, "y": 14,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "percent",
						"min":  0,
						"custom": map[string]any{
							"fillOpacity": 10,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_vm_cpu_usage_percent",
						"legendFormat": "{{name}}",
						"refId":        "A",
					},
				},
			},
			// Timeseries: VM Memory Used
			{
				"id":    11,
				"title": "Instance Memory (RAM)",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 12, "x": 12, "y": 14,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "bytes",
						"custom": map[string]any{
							"fillOpacity": 10,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_vm_memory_used_bytes",
						"legendFormat": "{{name}} (used)",
						"refId":        "A",
					},
					{
						"expr":         "webkvm_vm_memory_total_bytes",
						"legendFormat": "{{name}} (allocated)",
						"refId":        "B",
					},
				},
			},
			// Timeseries: VM Disk I/O
			{
				"id":    12,
				"title": "Instance Disk I/O Throughput",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 12, "x": 0, "y": 21,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "Bps",
						"custom": map[string]any{
							"fillOpacity": 10,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_vm_disk_read_bytes_per_sec",
						"legendFormat": "{{name}} - Read",
						"refId":        "A",
					},
					{
						"expr":         "webkvm_vm_disk_write_bytes_per_sec",
						"legendFormat": "{{name}} - Write",
						"refId":        "B",
					},
				},
			},
			// Timeseries: VM Network Bandwidth
			{
				"id":    13,
				"title": "Instance Network Bandwidth",
				"type":  "timeseries",
				"gridPos": map[string]int{
					"h": 7, "w": 12, "x": 12, "y": 21,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "Bps",
						"custom": map[string]any{
							"fillOpacity": 10,
							"lineWidth":   2,
						},
					},
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_vm_net_rx_bytes_per_sec",
						"legendFormat": "{{name}} - Rx",
						"refId":        "A",
					},
					{
						"expr":         "webkvm_vm_net_tx_bytes_per_sec",
						"legendFormat": "{{name}} - Tx",
						"refId":        "B",
					},
				},
			},

			// Row: Storage Pools
			{
				"collapsed": false,
				"gridPos":   map[string]int{"h": 1, "w": 24, "x": 0, "y": 28},
				"id":        103,
				"title":     "Storage Pools Utilization",
				"type":      "row",
			},
			// Panel: Storage Pools Bar Gauge
			{
				"id":    14,
				"title": "Pool Allocation vs Capacity",
				"type":  "bargauge",
				"gridPos": map[string]int{
					"h": 6, "w": 24, "x": 0, "y": 29,
				},
				"fieldConfig": map[string]any{
					"defaults": map[string]any{
						"unit": "bytes",
						"thresholds": map[string]any{
							"mode": "percentage",
							"steps": []map[string]any{
								{"color": "green", "value": nil},
								{"color": "semi-dark-yellow", "value": 75},
								{"color": "red", "value": 85},
							},
						},
					},
				},
				"options": map[string]any{
					"displayMode":  "gradient",
					"orientation":  "horizontal",
					"showUnfilled": true,
				},
				"targets": []map[string]any{
					{
						"expr":         "webkvm_storage_pool_allocation_bytes",
						"legendFormat": "Allocation - {{pool}}",
						"refId":        "A",
					},
					{
						"expr":         "webkvm_storage_pool_capacity_bytes",
						"legendFormat": "Capacity - {{pool}}",
						"refId":        "B",
					},
				},
			},
		},
	}

	data, err := json.MarshalIndent(dashboard, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return data
}

// GenerateAlertRules produces standard Prometheus / Alertmanager alerting rules
// in YAML format for WebKVM instances and host health.
func GenerateAlertRules() []byte {
	yamlStr := strings.TrimSpace(`
# WebKVM Alerting Rules for Prometheus & Alertmanager
# Learn more at https://github.com/slaker1908/webkvm
groups:
  - name: webkvm_alerts
    rules:
      - alert: WebKVMDown
        expr: webkvm_up == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "WebKVM daemon is offline"
          description: "The WebKVM management daemon has been unreachable or down for more than 1 minute."

      - alert: HostHighCPUUsage
        expr: webkvm_host_cpu_usage_percent > 90
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Host CPU utilization is critical (>90%)"
          description: "Host system CPU utilization is sustained above 90% (currently {{ $value | printf \"%.1f\" }}%)."

      - alert: HostHighMemoryUsage
        expr: (webkvm_host_memory_used_bytes / webkvm_host_memory_total_bytes) * 100 > 90
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Host RAM utilization is critical (>90%)"
          description: "Host physical memory usage is sustained above 90% (currently {{ $value | printf \"%.1f\" }}%)."

      - alert: HostDiskLowSpace
        expr: (webkvm_host_disk_used_bytes / webkvm_host_disk_total_bytes) * 100 > 85
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Host root disk is almost full (>85%)"
          description: "Host root filesystem usage has exceeded 85% capacity."

      - alert: StoragePoolAlmostFull
        expr: (webkvm_storage_pool_allocation_bytes / webkvm_storage_pool_capacity_bytes) * 100 > 85
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Storage pool {{ $labels.pool }} is almost full (>85%)"
          description: "Storage pool '{{ $labels.pool }}' allocation exceeds 85% of total capacity."

      - alert: InstanceHighCPUUsage
        expr: webkvm_vm_cpu_usage_percent > 95
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Instance '{{ $labels.name }}' sustained high CPU (>95%)"
          description: "Virtual machine or container '{{ $labels.name }}' has sustained CPU utilization above 95% for 10 minutes."
`) + "\n"

	return []byte(yamlStr)
}
