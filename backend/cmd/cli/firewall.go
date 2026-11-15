package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"webkvm/internal/firewall"
)

const (
	defaultClusterConfirmPath  = "/run/webkvm/cluster-fw.confirm"
	defaultClusterRollbackPath = "/run/webkvm/cluster-fw.rollback"
)

// runFirewallCluster implements `webkvm-cli firewall cluster ...`. It is a
// local, root-only command: the same profile must be present on every node,
// where it renders a byte-identical ruleset.
func runFirewallCluster(args []string) error {
	if len(args) < 1 || args[0] != "cluster" {
		return fmt.Errorf("firewall requires the 'cluster' subcommand")
	}
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	profilePath := firewall.CanonicalClusterProfilePath()
	confirmPath := defaultClusterConfirmPath
	rollbackPath := defaultClusterRollbackPath
	timeout := 30 * time.Second
	force := false
	forceRollback := false
	jsonOut := false
	clusterCIDR := ""
	managementCIDRs := []string{}
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--profile":
			if i+1 < len(args) {
				i++
				profilePath = args[i]
			}
		case "--confirm":
			if i+1 < len(args) {
				i++
				confirmPath = args[i]
			}
		case "--rollback":
			if i+1 < len(args) {
				i++
				rollbackPath = args[i]
			}
		case "--timeout":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					return fmt.Errorf("invalid --timeout: %w", err)
				}
				timeout = d
			}
		case "--force":
			force = true
		case "--force-rollback":
			forceRollback = true
		case "--json":
			jsonOut = true
		case "--cluster-cidr":
			if i+1 < len(args) {
				i++
				clusterCIDR = args[i]
			}
		case "--management-cidr":
			if i+1 < len(args) {
				i++
				managementCIDRs = append(managementCIDRs, args[i])
			}
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	m, err := firewall.NewClusterManager(profilePath, nil)
	if err != nil {
		return err
	}

	switch sub {
	case "init":
		if !force {
			if _, err := os.Stat(profilePath); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", profilePath)
			}
		}
		p := firewall.DefaultClusterProfile()
		p.ClusterCIDR = clusterCIDR
		p.ManagementCIDRs = managementCIDRs
		file, err := firewall.SaveClusterProfile(profilePath, p)
		if err != nil {
			return err
		}
		fmt.Printf("wrote %s (version=%d, enabled=false)\n", profilePath, file.Version)
		return nil
	case "render":
		rs, err := m.Preview(m.Profile())
		if err != nil {
			return err
		}
		fmt.Print(rs)
		return nil
	case "status":
		p, deadline, pending, hash := m.Status()
		version, profileHash := m.FileMeta()
		state := firewall.LoadSyncState(firewall.SyncStatePath(profilePath))
		fmt.Printf("profile=%s enabled=%v cluster_cidr=%s web_port=%d ssh_port=%d version=%d\n",
			profilePath, p.Enabled, p.ClusterCIDR, p.WebPort, p.SSHPort, version)
		fmt.Printf("profile_sha256=%s applied_version=%d applied_sha256=%s applied_at=%s\n",
			profileHash, state.Version, state.SHA256, state.AppliedAt.Format(time.RFC3339))
		fmt.Printf("table_present=%v confirmed_hash=%s pending=%v",
			m.TableApplied(), hash, pending)
		if pending {
			fmt.Printf(" deadline=%s", deadline.Format(time.RFC3339))
		}
		if state.LastError != "" {
			fmt.Printf(" last_error=%s", state.LastError)
		}
		fmt.Println()
		return nil
	case "enable", "disable":
		p := m.Profile()
		p.Enabled = sub == "enable"
		if err := m.SaveProfile(p); err != nil {
			return err
		}
		fmt.Printf("cluster firewall enabled=%v in %s\n", p.Enabled, profilePath)
		return nil
	case "apply":
		if os.Geteuid() != 0 {
			return fmt.Errorf("firewall apply requires root")
		}
		p := m.Profile()
		if !p.Enabled {
			return fmt.Errorf("cluster firewall is disabled; run 'webkvm-cli firewall cluster enable' first")
		}
		if err := os.MkdirAll(filepath.Dir(confirmPath), 0o755); err != nil {
			return err
		}
		fmt.Printf("applying %s; run 'webkvm-cli firewall cluster confirm' within %s to keep it\n", profilePath, timeout)
		err := m.ApplyInteractive(p, confirmPath, rollbackPath, timeout)
		if err != nil {
			return err
		}
		fmt.Println("cluster firewall confirmed")
		return nil
	case "confirm":
		if err := os.MkdirAll(filepath.Dir(confirmPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(confirmPath, []byte("confirm\n"), 0o600); err != nil {
			return err
		}
		fmt.Printf("confirm signal written to %s\n", confirmPath)
		return nil
	case "rollback":
		if err := os.MkdirAll(filepath.Dir(rollbackPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(rollbackPath, []byte("rollback\n"), 0o600); err != nil {
			return err
		}
		fmt.Printf("rollback signal written to %s\n", rollbackPath)
		return nil
	case "sync":
		if os.Geteuid() != 0 {
			return fmt.Errorf("firewall sync requires root")
		}
		state, err := firewall.SyncClusterProfile(profilePath, forceRollback, firewall.DefaultSyncHooks())
		if jsonOut {
			printJSONState(state)
		} else {
			fmt.Printf("version=%d sha256=%s applied_at=%s\n", state.Version, state.SHA256, state.AppliedAt.Format(time.RFC3339))
			if state.LastError != "" {
				fmt.Printf("last_error=%s\n", state.LastError)
			}
		}
		return err
	case "receive":
		// Forced command from authorized_keys: the profile arrives on stdin.
		// Always print the state as JSON: the active node parses it to show
		// the per-node version/hash/error. The forced command carries no
		// flags, so --json cannot be relied on here.
		state, err := firewall.ReceiveClusterProfile(profilePath, os.Stdin, firewall.DefaultSyncHooks())
		printJSONState(state)
		return err
	default:
		return fmt.Errorf("firewall cluster requires a subcommand (init|render|status|enable|disable|apply|confirm|rollback|sync|receive)")
	}
}

func printJSONState(state firewall.SyncState) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	fmt.Println(string(data))
}
