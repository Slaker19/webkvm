package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// netplanBaseDir allows overriding in tests.
var netplanBaseDir = "/etc/netplan"

var netplanBridgeRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)

func cleanNetplanPath(bridge string) (string, error) {
	if !netplanBridgeRE.MatchString(bridge) || filepath.Base(bridge) != bridge || strings.Contains(bridge, "..") {
		return "", fmt.Errorf("invalid bridge name %q", bridge)
	}
	cleanBase := filepath.Clean(netplanBaseDir)
	cleanPath := filepath.Clean(filepath.Join(cleanBase, fmt.Sprintf("zz-webkvm-%s.yaml", bridge)))
	if !strings.HasPrefix(cleanPath, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path traversal for bridge %q", bridge)
	}
	return cleanPath, nil
}

// hasNetplan checks if netplan is installed and configuration directory exists.
func hasNetplan() bool {
	if _, err := os.Stat(netplanBaseDir); err != nil {
		return false
	}
	_, err := exec.LookPath("netplan")
	return err == nil
}

// persistNetplanBond updates or creates Netplan YAML to persist a bond attached to a bridge.
func persistNetplanBond(bridge string, bondName string, bondMode string, slaves []string) error {
	if !hasNetplan() {
		return nil
	}

	yamlPath, err := cleanNetplanPath(bridge)
	if err != nil {
		return err
	}
	var root map[string]any

	if data, err := os.ReadFile(yamlPath); err == nil {
		if err := yaml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("parse existing netplan yaml: %w", err)
		}
	} else {
		root = map[string]any{}
	}

	netAny, ok := root["network"]
	var netMap map[string]any
	if ok && netAny != nil {
		if m, ok := netAny.(map[string]any); ok {
			netMap = m
		} else {
			netMap = map[string]any{"version": 2}
		}
	} else {
		netMap = map[string]any{"version": 2}
	}
	root["network"] = netMap

	// Ensure ethernets map exists and disables dhcp on slaves
	ethAny, ok := netMap["ethernets"]
	var ethMap map[string]any
	if ok && ethAny != nil {
		if m, ok := ethAny.(map[string]any); ok {
			ethMap = m
		} else {
			ethMap = map[string]any{}
		}
	} else {
		ethMap = map[string]any{}
	}
	for _, s := range slaves {
		ethMap[s] = map[string]any{
			"dhcp4": false,
			"dhcp6": false,
		}
	}
	netMap["ethernets"] = ethMap

	// Ensure bonds map exists and configures the bond
	bondsAny, ok := netMap["bonds"]
	var bondsMap map[string]any
	if ok && bondsAny != nil {
		if m, ok := bondsAny.(map[string]any); ok {
			bondsMap = m
		} else {
			bondsMap = map[string]any{}
		}
	} else {
		bondsMap = map[string]any{}
	}
	bondsMap[bondName] = map[string]any{
		"interfaces": slaves,
		"parameters": map[string]any{
			"mode":                 bondMode,
			"mii-monitor-interval": 100,
		},
	}
	netMap["bonds"] = bondsMap

	// Update bridge to enslave the bond instead of individual NICs
	if bridge != "" {
		brsAny, ok := netMap["bridges"]
		var brsMap map[string]any
		if ok && brsAny != nil {
			if m, ok := brsAny.(map[string]any); ok {
				brsMap = m
			} else {
				brsMap = map[string]any{}
			}
		} else {
			brsMap = map[string]any{}
		}
		if brAny, ok := brsMap[bridge]; ok && brAny != nil {
			if brSpec, ok := brAny.(map[string]any); ok {
				brSpec["interfaces"] = []string{bondName}
				brsMap[bridge] = brSpec
			}
		}
		netMap["bridges"] = brsMap
	}

	cleanBase := filepath.Clean(netplanBaseDir)
	bakPath := filepath.Clean(yamlPath + ".bak-webkvm")
	if !strings.HasPrefix(bakPath, cleanBase+string(filepath.Separator)) {
		return fmt.Errorf("invalid backup path")
	}
	if _, err := os.Stat(yamlPath); err == nil {
		_ = os.Rename(yamlPath, bakPath)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		if _, berr := os.Stat(bakPath); berr == nil {
			_ = os.Rename(bakPath, yamlPath)
		}
		return fmt.Errorf("marshal netplan yaml: %w", err)
	}

	header := "# Managed by webkvm\n"
	if err := os.WriteFile(yamlPath, append([]byte(header), out...), 0600); err != nil {
		if _, berr := os.Stat(bakPath); berr == nil {
			_ = os.Rename(bakPath, yamlPath)
		}
		return fmt.Errorf("write netplan yaml: %w", err)
	}

	// Validate with netplan generate
	if genOut, err := exec.Command("netplan", "generate").CombinedOutput(); err != nil {
		if _, berr := os.Stat(bakPath); berr == nil {
			_ = os.Rename(bakPath, yamlPath)
		}
		return fmt.Errorf("netplan generate failed: %v (%s)", err, strings.TrimSpace(string(genOut)))
	}

	// Apply netplan
	if applyOut, err := exec.Command("netplan", "apply").CombinedOutput(); err != nil {
		if _, berr := os.Stat(bakPath); berr == nil {
			_ = os.Rename(bakPath, yamlPath)
		}
		return fmt.Errorf("netplan apply failed: %v (%s)", err, strings.TrimSpace(string(applyOut)))
	}

	// Clean up backup on success
	_ = os.Remove(bakPath)
	return nil
}

// revertNetplanBond reverts Netplan YAML when a bond is deleted.
func revertNetplanBond(bridge string, bondName string, restoreSlave string) error {
	if !hasNetplan() {
		return nil
	}

	yamlPath, err := cleanNetplanPath(bridge)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil
	}

	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse existing netplan yaml: %w", err)
	}

	netAny, ok := root["network"]
	if !ok || netAny == nil {
		return nil
	}
	netMap, ok := netAny.(map[string]any)
	if !ok {
		return nil
	}

	// Remove bond from bonds map
	if bondsAny, ok := netMap["bonds"]; ok && bondsAny != nil {
		if bondsMap, ok := bondsAny.(map[string]any); ok {
			delete(bondsMap, bondName)
			if len(bondsMap) == 0 {
				delete(netMap, "bonds")
			}
		}
	}

	// Restore slave on bridge
	if bridge != "" && restoreSlave != "" {
		if brsAny, ok := netMap["bridges"]; ok && brsAny != nil {
			if brsMap, ok := brsAny.(map[string]any); ok {
				if brAny, ok := brsMap[bridge]; ok && brAny != nil {
					if brSpec, ok := brAny.(map[string]any); ok {
						brSpec["interfaces"] = []string{restoreSlave}
					}
				}
			}
		}
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal netplan yaml: %w", err)
	}

	header := "# Managed by webkvm\n"
	if err := os.WriteFile(yamlPath, append([]byte(header), out...), 0600); err != nil {
		return fmt.Errorf("write netplan yaml: %w", err)
	}

	_ = exec.Command("netplan", "generate").Run()
	_ = exec.Command("netplan", "apply").Run()
	return nil
}
