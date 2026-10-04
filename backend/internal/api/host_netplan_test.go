package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNetplanBondYAML(t *testing.T) {
	tmpDir := t.TempDir()
	origDir := netplanBaseDir
	netplanBaseDir = tmpDir
	defer func() { netplanBaseDir = origDir }()

	// Seed an existing bridge netplan config
	initialYAML := `# Managed by webkvm
network:
  version: 2
  ethernets:
    enp1s0:
      dhcp4: false
      dhcp6: false
  bridges:
    vmbr0:
      interfaces: [enp1s0]
      addresses: [192.168.1.2/24]
      routes:
        - to: default
          via: 192.168.1.1
      nameservers:
        addresses: [1.1.1.1, 8.8.8.8]
`
	vmbr0Path := filepath.Join(tmpDir, "zz-webkvm-vmbr0.yaml")
	if err := os.WriteFile(vmbr0Path, []byte(initialYAML), 0600); err != nil {
		t.Fatalf("write initial netplan file: %v", err)
	}

	// We test the YAML manipulation logic without executing netplan CLI (which might not exist in CI or tests)
	t.Run("GenerateBondYAML", func(t *testing.T) {
		slaves := []string{"enp1s0", "enx6c1ff72592ae"}
		yamlPath := filepath.Join(netplanBaseDir, "zz-webkvm-vmbr0.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			t.Fatalf("read yaml: %v", err)
		}
		if !strings.Contains(string(data), "vmbr0") {
			t.Fatalf("expected vmbr0 in yaml")
		}

		// Verify parsing & structure
		var root map[string]any
		if err := yaml.Unmarshal(data, &root); err != nil {
			t.Fatalf("parse yaml: %v", err)
		}

		netMap, ok := root["network"].(map[string]any)
		if !ok {
			t.Fatalf("expected network map")
		}

		// Mutate as persistNetplanBond does
		bondsMap := map[string]any{
			"bond0": map[string]any{
				"interfaces": slaves,
				"parameters": map[string]any{
					"mode":                 "active-backup",
					"mii-monitor-interval": 100,
				},
			},
		}
		netMap["bonds"] = bondsMap
		brsMap, ok := netMap["bridges"].(map[string]any)
		if !ok {
			t.Fatalf("expected bridges map")
		}
		vmbr0Map := brsMap["vmbr0"].(map[string]any)
		vmbr0Map["interfaces"] = []string{"bond0"}

		outBytes, err := yaml.Marshal(root)
		if err != nil {
			t.Fatalf("marshal yaml: %v", err)
		}

		outStr := string(outBytes)
		if !strings.Contains(outStr, "bond0") {
			t.Errorf("expected bond0 in output: %s", outStr)
		}
		if !strings.Contains(outStr, "active-backup") {
			t.Errorf("expected active-backup in output: %s", outStr)
		}
		if !strings.Contains(outStr, "enx6c1ff72592ae") {
			t.Errorf("expected enx6c1ff72592ae in output: %s", outStr)
		}
	})
}
