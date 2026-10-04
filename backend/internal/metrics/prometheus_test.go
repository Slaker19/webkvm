package metrics

import (
	"math"
	"strings"
	"testing"

	"webkvm/internal/models"
)

func TestEscapeLabelValue(t *testing.T) {
	input := "foo\"bar\\baz\nqux"
	expected := `foo\"bar\\baz\nqux`
	got := escapeLabelValue(input)
	if got != expected {
		t.Fatalf("escapeLabelValue(%q) = %q; want %q", input, got, expected)
	}
}

func TestPrometheusExporter_WriteSample(t *testing.T) {
	exp := &PrometheusExporter{}
	exp.WriteHeader("test_metric", "A test metric", "gauge")
	exp.WriteSample("test_metric", map[string]string{
		"z_key": "last",
		"a_key": "first",
	}, 42.5)

	out := exp.buf.String()
	wantHeader := "# HELP test_metric A test metric\n# TYPE test_metric gauge\n"
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("header mismatch: %q", out)
	}
	wantSample := "test_metric{a_key=\"first\",z_key=\"last\"} 42.5\n"
	if !strings.HasSuffix(out, wantSample) {
		t.Errorf("sample mismatch: %q; want suffix %q", out, wantSample)
	}
}

func TestPrometheusExporter_NaNAndInf(t *testing.T) {
	exp := &PrometheusExporter{}
	exp.WriteSample("nan_metric", nil, math.NaN())
	exp.WriteSample("inf_metric", nil, math.Inf(1))

	out := exp.buf.String()
	if !strings.Contains(out, "nan_metric 0\n") {
		t.Errorf("expected NaN to be formatted as 0, got %q", out)
	}
	if !strings.Contains(out, "inf_metric 0\n") {
		t.Errorf("expected Inf to be formatted as 0, got %q", out)
	}
}

func TestRenderPrometheus_Full(t *testing.T) {
	data := PrometheusData{
		Version: "0.1.6",
		Host: &HostTelemetry{
			CPUUsage:  15.5,
			UsedRAM:   4000000000,
			TotalRAM:  16000000000,
			UsedDisk:  50000000000,
			TotalDisk: 200000000000,
			NetRx:     1024,
			NetTx:     2048,
			Uptime:    3600,
		},
		VMs: []models.VM{
			{
				ID:        "vm-1",
				Name:      "web-prod",
				Type:      "vm",
				State:     models.VMStateRunning,
				VCPUs:     4,
				RAMMB:     4096,
				RAMUsedMB: 2048,
				DiskGB:    50,
				UptimeSec: 1200,
				CPUUsage:  22.4,
			},
			{
				ID:        "ct-1",
				Name:      "db-test",
				Type:      "container",
				State:     models.VMStateShutoff,
				VCPUs:     2,
				RAMMB:     2048,
				RAMUsedMB: 0,
				DiskGB:    20,
				UptimeSec: 0,
				CPUUsage:  0,
			},
		},
		VMMetrics: map[string]models.VMMetrics{
			"vm-1": {
				VMID: "vm-1",
				DiskRead: models.MetricsSeries{
					Points: []models.MetricsSample{{T: 100, V: 5000}},
				},
				DiskWrite: models.MetricsSeries{
					Points: []models.MetricsSample{{T: 100, V: 3000}},
				},
				NetRx: models.MetricsSeries{
					Points: []models.MetricsSample{{T: 100, V: 15000}},
				},
				NetTx: models.MetricsSeries{
					Points: []models.MetricsSample{{T: 100, V: 25000}},
				},
			},
		},
		Pools: []models.StoragePool{
			{
				Name:      "default",
				Type:      "dir",
				Capacity:  100000000000,
				Allocated: 40000000000,
				Available: 60000000000,
				State:     "running",
			},
		},
	}

	res := RenderPrometheus(data)
	body := string(res)

	checks := []string{
		`webkvm_info{version="0.1.6"} 1`,
		`webkvm_up 1`,
		`webkvm_host_cpu_usage_percent 15.5`,
		`webkvm_host_memory_total_bytes 16000000000`,
		`webkvm_host_uptime_seconds 3600`,
		`webkvm_vms_total 2`,
		`webkvm_vms_state{state="running"} 1`,
		`webkvm_vms_state{state="shutoff"} 1`,
		`webkvm_vm_status{name="web-prod",state="running",type="vm",vm_id="vm-1"} 1`,
		`webkvm_vm_vcpus{name="web-prod",vm_id="vm-1"} 4`,
		`webkvm_vm_cpu_usage_percent{name="web-prod",vm_id="vm-1"} 22.4`,
		`webkvm_vm_disk_read_bytes_per_sec{name="web-prod",vm_id="vm-1"} 5000`,
		`webkvm_storage_pool_capacity_bytes{pool="default",type="dir"} 100000000000`,
		`webkvm_storage_pool_active{pool="default",type="dir"} 1`,
	}

	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("missing expected Prometheus output line: %s\nFull body:\n%s", check, body)
		}
	}
}
