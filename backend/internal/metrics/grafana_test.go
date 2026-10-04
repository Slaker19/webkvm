package metrics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateGrafanaDashboard(t *testing.T) {
	data := GenerateGrafanaDashboard()
	if len(data) == 0 {
		t.Fatal("GenerateGrafanaDashboard returned empty data")
	}

	var d map[string]any
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("GenerateGrafanaDashboard generated invalid JSON: %v", err)
	}

	if d["title"] != "WebKVM - Infrastructure & VMs Overview" {
		t.Errorf("unexpected dashboard title: %v", d["title"])
	}

	panels, ok := d["panels"].([]any)
	if !ok || len(panels) == 0 {
		t.Fatalf("expected non-empty panels array, got: %v", d["panels"])
	}

	// Verify key metrics exist in panels
	jsonStr := string(data)
	expectedMetrics := []string{
		"webkvm_up",
		"webkvm_host_cpu_usage_percent",
		"webkvm_host_memory_used_bytes",
		"webkvm_vms_state",
		"webkvm_vm_cpu_usage_percent",
		"webkvm_storage_pool_allocation_bytes",
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(jsonStr, metric) {
			t.Errorf("dashboard JSON missing expected metric %s", metric)
		}
	}
}

func TestGenerateAlertRules(t *testing.T) {
	data := GenerateAlertRules()
	if len(data) == 0 {
		t.Fatal("GenerateAlertRules returned empty data")
	}

	yamlStr := string(data)
	if !strings.Contains(yamlStr, "groups:") || !strings.Contains(yamlStr, "rules:") {
		t.Fatalf("missing standard alert rules header in yaml: %s", yamlStr)
	}

	expectedAlerts := []string{
		"alert: WebKVMDown",
		"alert: HostHighCPUUsage",
		"alert: HostHighMemoryUsage",
		"alert: HostDiskLowSpace",
		"alert: StoragePoolAlmostFull",
		"alert: InstanceHighCPUUsage",
	}

	for _, alert := range expectedAlerts {
		if !strings.Contains(yamlStr, alert) {
			t.Errorf("alert rules missing %s", alert)
		}
	}
}
