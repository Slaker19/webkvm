// webkvm-cli is a command-line client for the WebKVM REST API.
// It is intended for scripting and quick administration from SSH.
//
// Usage:
//
//	webkvm-cli [--server URL] [--token <JWT|API_TOKEN>] <command>
//
// The token can also be provided via the WEBKVM_TOKEN environment
// variable and the server via WEBKVM_SERVER (default http://127.0.0.1:8080).
//
// Commands:
//
//	status                     server health
//	info                       host info (CPU/RAM/distro)
//	restart                    restart the webkvm backend service
//
//	vms list                   list VMs
//	vms show <id>              show VM detail
//	vms start <id> | stop <id> | forceoff <id> | reboot <id>
//	vms suspend <id> | resume <id>
//	vms delete <id>            delete a VM
//	vms clone <id>             clone a VM
//	vms autostart <id> <on|off>
//	vms snapshots <id>                 list snapshots
//	vms snapshot <id> create <name>    create a snapshot
//	vms snapshot <id> revert <sid>     revert to a snapshot
//	vms snapshot <id> delete <sid>     delete a snapshot
//
//	storage pools               list storage pools
//	storage volumes             list storage volumes
//	storage isos                list ISO images
//
//	networks list               list virtual networks
//	networks show <id>          show a network
//	networks start <id> | stop <id>
//	networks delete <id>
//
//	users list                  list users
//
//	tokens list                 list API tokens
//	tokens create <name>        create an API token
//
//	backup targets list         list backup targets
//	backup run <target>         trigger a backup now
//	backup jobs                 list recent backup jobs
//	backup schedules list       list backup schedules
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	server := os.Getenv("WEBKVM_SERVER")
	if server == "" {
		server = "http://127.0.0.1:8080"
	}
	token := os.Getenv("WEBKVM_TOKEN")
	insecure := os.Getenv("WEBKVM_INSECURE") == "1"

	args := os.Args[1:]
	var command []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server":
			i++
			if i < len(args) {
				server = args[i]
			}
		case "--token":
			i++
			if i < len(args) {
				token = args[i]
			}
		case "--insecure":
			insecure = true
		case "-h", "--help", "help":
			usage()
			return
		default:
			command = append(command, args[i])
		}
	}
	if len(command) == 0 {
		usage()
		return
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: a token is required (--token or WEBKVM_TOKEN)")
		os.Exit(2)
	}
	server = strings.TrimSuffix(server, "/")

	if err := run(server, token, insecure, command); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`webkvm-cli — WebKVM REST client

Usage: webkvm-cli [--server URL] [--token TOKEN] [--insecure] <command>

  --insecure   skip TLS certificate verification (use with self-signed certs)
  --token      API token or JWT (or WEBKVM_TOKEN)
  --server     server URL (or WEBKVM_SERVER, default http://127.0.0.1:8080)

Commands:
  status
  info
  restart

  vms list
  vms show <id>
  vms start <id> | stop <id> | forceoff <id> | reboot <id>
  vms suspend <id> | resume <id>
  vms delete <id>
  vms clone <id>
  vms autostart <id> <on|off>
  vms snapshots <id>
  vms snapshot <id> create <name>
  vms snapshot <id> revert <sid>
  vms snapshot <id> delete <sid>

  storage pools | storage volumes | storage isos

  networks list | networks show <id> | networks start <id> | networks stop <id> | networks delete <id>

  users list
  users create <username> <password> [--role <admin|operator|viewer>] [--email <x>]
  users update <username> [--role <x>] [--password <x>] [--active <true|false>] [--email <x>]
  users delete <username>

  tokens list | tokens create <name>

  backup targets list | backup run <target> | backup jobs | backup schedules list`)
}

// client is a thin HTTP wrapper for the WebKVM REST API.
type client struct {
	server   string
	token    string
	http     *http.Client
	insecure bool
}

func newClient(server, token string, insecure bool) *client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — explicit --insecure flag
	}
	return &client{server: server, token: token, http: &http.Client{Timeout: 120 * time.Second, Transport: tr}, insecure: insecure}
}

func (c *client) do(method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.server+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, msg)
	}
	return data, nil
}

func (c *client) get(path string) ([]byte, error) { return c.do("GET", path, nil) }
func (c *client) post(path string, body any) ([]byte, error) { return c.do("POST", path, body) }
func (c *client) del(path string) ([]byte, error) { return c.do("DELETE", path, nil) }

func run(server, token string, insecure bool, cmd []string) error {
	c := newClient(server, token, insecure)

	switch cmd[0] {
	case "status":
		out, err := c.get("/api/health")
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		fmt.Printf("status=%s data_dir=%v libvirt=%v\n", m["status"], m["data_dir"], m["libvirt"])
		return nil

	case "info":
		out, err := c.get("/api/host")
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		ramGB := toInt(m["total_ram"]) / (1024 * 1024 * 1024)
		fmt.Printf("hostname=%v arch=%v cpu=%v cores x%v threads libvirt=%v qemu=%v ram=%d GiB\n",
			m["hostname"], m["architecture"], m["cpu_model"], m["cpu_cores"],
			m["libvirt_version"], m["qemu_version"], ramGB)
		return nil

	case "restart":
		if _, err := c.post("/api/system/restart", nil); err != nil {
			return err
		}
		fmt.Println("restart requested")
		return nil

	case "vms":
		return runVMs(c, cmd)

	case "storage":
		return runStorage(c, cmd)

	case "networks":
		return runNetworks(c, cmd)

	case "users":
		return runUsers(c, cmd)

	case "tokens":
		return runTokens(c, cmd)

	case "backup":
		return runBackup(c, cmd)

	case "schedules":
		// legacy alias: schedules list
		return runBackup(c, append([]string{"schedules"}, cmd[1:]...))
	}
	return fmt.Errorf("unknown command %q", cmd[0])
}

func runVMs(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("vms requires a subcommand (list|show|start|stop|forceoff|reboot|suspend|resume|delete|clone|autostart|snapshot)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/vms")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("vms list: %w", err)
		}
		for _, v := range arr {
			fmt.Printf("%-36s %-12s %-10s %3d vCPU %6d MB\n", v["id"], v["name"], v["state"], toInt(v["vcpus"]), toInt(v["ram_mb"]))
		}
		return nil

	case "show":
		if len(cmd) < 3 {
			return fmt.Errorf("vms show requires a VM id")
		}
		out, err := c.get("/api/vms/" + cmd[2])
		if err != nil {
			return err
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			return fmt.Errorf("vms show: %w", err)
		}
		fmt.Printf("ID:          %v\n", v["id"])
		fmt.Printf("Name:        %v\n", v["name"])
		if a, _ := v["alias"].(string); a != "" {
			fmt.Printf("Alias:       %v\n", a)
		}
		fmt.Printf("State:       %v\n", v["state"])
		fmt.Printf("CPU:         %v vCPU\n", toInt(v["vcpus"]))
		fmt.Printf("RAM:         %v MB\n", toInt(v["ram_mb"]))
		fmt.Printf("Disk:        %v GB\n", toInt(v["disk_gb"]))
		if ip, _ := v["ip"].(string); ip != "" {
			fmt.Printf("IP:          %v\n", ip)
		}
		if fs, _ := v["firmware"].(string); fs != "" {
			fmt.Printf("Firmware:    %v\n", fs)
		}
		fmt.Printf("Autostart:   %v\n", v["autostart"])
		if disks, ok := v["disks"].([]any); ok {
			fmt.Printf("Disks:       %d\n", len(disks))
			for _, d := range disks {
				dm, _ := d.(map[string]any)
				fmt.Printf("  - %v (%v, %v)\n", dm["dev"], dm["size_gb"], dm["pool"])
			}
		}
		if nets, ok := v["networks"].([]any); ok {
			fmt.Printf("Networks:    %d\n", len(nets))
			for _, n := range nets {
				nm, _ := n.(map[string]any)
				fmt.Printf("  - %v (%v)\n", nm["mac"], nm["network"])
			}
		}
		return nil

	case "start", "stop", "forceoff", "reboot", "suspend", "resume":
		if len(cmd) < 3 {
			return fmt.Errorf("vms %s requires a VM id", cmd[1])
		}
		action := map[string]string{
			"start": "start", "stop": "shutdown", "forceoff": "forceoff",
			"reboot": "reboot", "suspend": "suspend", "resume": "resume",
		}[cmd[1]]
		if _, err := c.post("/api/vms/"+cmd[2]+"/"+action, nil); err != nil {
			return err
		}
		fmt.Printf("%s %s ok\n", cmd[1], cmd[2])
		return nil

	case "delete":
		if len(cmd) < 3 {
			return fmt.Errorf("vms delete requires a VM id")
		}
		if _, err := c.del("/api/vms/" + cmd[2]); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", cmd[2])
		return nil

	case "clone":
		if len(cmd) < 3 {
			return fmt.Errorf("vms clone requires a VM id")
		}
		out, err := c.post("/api/vms/"+cmd[2]+"/clone", nil)
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		if id, _ := m["id"].(string); id != "" {
			fmt.Printf("cloned to %s\n", id)
		} else {
			fmt.Println("clone ok")
		}
		return nil

	case "autostart":
		if len(cmd) < 4 {
			return fmt.Errorf("vms autostart requires <id> <on|off>")
		}
		on := cmd[3] == "on" || cmd[3] == "true" || cmd[3] == "1"
		if _, err := c.post("/api/vms/"+cmd[2]+"/autostart", map[string]any{"autostart": on}); err != nil {
			return err
		}
		fmt.Printf("autostart %s = %v\n", cmd[2], on)
		return nil

	case "snapshots":
		if len(cmd) < 3 {
			return fmt.Errorf("vms snapshots requires a VM id")
		}
		out, err := c.get("/api/vms/" + cmd[2] + "/snapshots")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("snapshots: %w", err)
		}
		for _, s := range arr {
			fmt.Printf("%-36s %-16s parent=%v\n", s["id"], s["name"], s["parent"])
		}
		return nil

	case "snapshot":
		if len(cmd) < 4 {
			return fmt.Errorf("vms snapshot requires <id> <create|revert|delete> <arg>")
		}
		id := cmd[2]
		switch cmd[3] {
		case "create":
			name := ""
			if len(cmd) > 4 {
				name = cmd[4]
			}
			out, err := c.post("/api/vms/"+id+"/snapshots", map[string]any{"name": name})
			if err != nil {
				return err
			}
			var m map[string]any
			_ = json.Unmarshal(out, &m)
			if sid, _ := m["id"].(string); sid != "" {
				fmt.Printf("snapshot created: %s\n", sid)
			} else {
				fmt.Println("snapshot created")
			}
			return nil
		case "revert":
			if len(cmd) < 5 {
				return fmt.Errorf("vms snapshot revert requires a snapshot id")
			}
			if _, err := c.post("/api/vms/"+id+"/snapshots/"+cmd[4]+"/revert", nil); err != nil {
				return err
			}
			fmt.Printf("reverted to %s\n", cmd[4])
			return nil
		case "delete":
			if len(cmd) < 5 {
				return fmt.Errorf("vms snapshot delete requires a snapshot id")
			}
			if _, err := c.del("/api/vms/" + id + "/snapshots/" + cmd[4]); err != nil {
				return err
			}
			fmt.Printf("deleted snapshot %s\n", cmd[4])
			return nil
		}
		return fmt.Errorf("unknown snapshot subcommand %q", cmd[3])
	}
	return fmt.Errorf("unknown vms subcommand %q", cmd[1])
}

func runStorage(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("storage requires a subcommand (pools|volumes|isos)")
	}
	switch cmd[1] {
	case "pools":
		out, err := c.get("/api/storage/pools")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("storage pools: %w", err)
		}
		for _, p := range arr {
			avail := toInt(p["available"]) / (1024 * 1024 * 1024)
			fmt.Printf("%-20s %-6s %-6s %-16s state=%v avail=%d GiB\n", p["name"], p["type"], p["purpose"], p["path"], p["state"], avail)
		}
		return nil
	case "volumes":
		out, err := c.get("/api/storage/volumes")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("storage volumes: %w", err)
		}
		for _, v := range arr {
			gb := toInt(v["capacity"]) / (1024 * 1024 * 1024)
			fmt.Printf("%-24s pool=%-16s fmt=%-8s %d GiB\n", v["name"], v["pool"], v["format"], gb)
		}
		return nil
	case "isos":
		out, err := c.get("/api/storage/isos")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("storage isos: %w", err)
		}
		for _, iso := range arr {
			mb := toInt(iso["size"]) / (1024 * 1024)
			fmt.Printf("%-40s pool=%-16s %d MiB\n", iso["name"], iso["pool"], mb)
		}
		return nil
	}
	return fmt.Errorf("unknown storage subcommand %q", cmd[1])
}

func runNetworks(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("networks requires a subcommand (list|show|start|stop|delete)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/networks")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("networks list: %w", err)
		}
		for _, n := range arr {
			fmt.Printf("%-20s %-8s %-16s active=%v autostart=%v\n", n["name"], n["forward"], n["cidr"], n["active"], n["autostart"])
		}
		return nil
	case "show":
		if len(cmd) < 3 {
			return fmt.Errorf("networks show requires a network id")
		}
		out, err := c.get("/api/networks")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("networks show: %w", err)
		}
		want := cmd[2]
		for _, n := range arr {
			if n["name"] == want {
				fmt.Printf("Name:      %v\n", n["name"])
				fmt.Printf("Forward:   %v\n", n["forward"])
				fmt.Printf("CIDR:      %v\n", n["cidr"])
				fmt.Printf("Gateway:   %v\n", n["gateway"])
				fmt.Printf("DHCP:      %v\n", n["dhcp"])
				fmt.Printf("Active:    %v\n", n["active"])
				fmt.Printf("Autostart: %v\n", n["autostart"])
				return nil
			}
		}
		return fmt.Errorf("network %q not found", want)
	case "start", "stop":
		if len(cmd) < 3 {
			return fmt.Errorf("networks %s requires a network id", cmd[1])
		}
		if _, err := c.post("/api/networks/"+cmd[2]+"/"+cmd[1], nil); err != nil {
			return err
		}
		fmt.Printf("%s %s ok\n", cmd[1], cmd[2])
		return nil
	case "delete":
		if len(cmd) < 3 {
			return fmt.Errorf("networks delete requires a network id")
		}
		if _, err := c.del("/api/networks/" + cmd[2]); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", cmd[2])
		return nil
	}
	return fmt.Errorf("unknown networks subcommand %q", cmd[1])
}

// parseKeyVal parses "--key value" style flags from args[1:] into a map.
// Returns the non-flag positional args.
func parseFlags(args []string) (map[string]string, []string) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			key := strings.TrimPrefix(args[i], "--")
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				flags[key] = args[i+1]
				i++
			} else {
				flags[key] = "true"
			}
			continue
		}
		pos = append(pos, args[i])
	}
	return flags, pos
}

func runUsers(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("users requires a subcommand (list|create|update|delete)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/users")
		if err != nil {
			return err
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("users list: %w", err)
		}
		for _, u := range arr {
			email, _ := u["email"].(string)
			fmt.Printf("%-20s %-10s active=%v email=%v\n", u["username"], u["role"], u["active"], email)
		}
		return nil

	case "create":
		if len(cmd) < 4 {
			return fmt.Errorf("users create requires <username> <password> [--role <admin|operator|viewer>] [--email <x>]")
		}
		username, password := cmd[2], cmd[3]
		flags, _ := parseFlags(cmd[4:])
		role := flags["role"]
		if role == "" {
			role = "viewer"
		}
		body := map[string]any{"username": username, "password": password, "role": role}
		if email := flags["email"]; email != "" {
			body["email"] = email
		}
		if _, err := c.post("/api/users", body); err != nil {
			return err
		}
		fmt.Printf("created user %s (role=%s)\n", username, role)
		return nil

	case "update":
		if len(cmd) < 3 {
			return fmt.Errorf("users update requires <username> [--role <x>] [--password <x>] [--active <true|false>] [--email <x>]")
		}
		username := cmd[2]
		flags, _ := parseFlags(cmd[3:])
		body := map[string]any{}
		if v := flags["role"]; v != "" {
			body["role"] = v
		}
		if v := flags["password"]; v != "" {
			body["password"] = v
		}
		if v := flags["email"]; v != "" {
			body["email"] = v
		}
		if v, ok := flags["active"]; ok {
			body["active"] = v == "true" || v == "1" || v == "yes"
		}
		if len(body) == 0 {
			return fmt.Errorf("users update requires at least one field to change")
		}
		if _, err := c.do("PUT", "/api/users/"+username, body); err != nil {
			return err
		}
		fmt.Printf("updated user %s\n", username)
		return nil

	case "delete":
		if len(cmd) < 3 {
			return fmt.Errorf("users delete requires a username")
		}
		if _, err := c.del("/api/users/" + cmd[2]); err != nil {
			return err
		}
		fmt.Printf("deleted user %s\n", cmd[2])
		return nil
	}
	return fmt.Errorf("unknown users subcommand %q", cmd[1])
}

func runTokens(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("tokens requires a subcommand (list|create)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/tokens")
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			return fmt.Errorf("tokens list: %w", err)
		}
		arr, _ := m["tokens"].([]any)
		for _, t := range arr {
			tm, _ := t.(map[string]any)
			fmt.Printf("%-20s %-8s expires=%v\n", tm["name"], tm["scopes"], tm["expires_at"])
		}
		return nil
	case "create":
		if len(cmd) < 3 {
			return fmt.Errorf("tokens create requires a name")
		}
		out, err := c.post("/api/tokens", map[string]any{"name": cmd[2]})
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		if plain, _ := m["plain"].(string); plain != "" {
			fmt.Printf("token created (copy now, shown once):\n%s\n", plain)
		} else {
			fmt.Println("token created")
		}
		return nil
	}
	return fmt.Errorf("unknown tokens subcommand %q", cmd[1])
}

func runBackup(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("backup requires a subcommand (targets|run|jobs|schedules)")
	}
	switch cmd[1] {
	case "targets":
		if len(cmd) < 3 || cmd[2] != "list" {
			return fmt.Errorf("backup targets requires 'list'")
		}
		out, err := c.get("/api/backup/targets")
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			return fmt.Errorf("backup targets: %w", err)
		}
		arr, _ := m["targets"].([]any)
		for _, t := range arr {
			tm, _ := t.(map[string]any)
			fmt.Printf("%-22s %-20s enabled=%v\n", tm["id"], tm["name"], tm["enabled"])
		}
		return nil
	case "run":
		if len(cmd) < 3 {
			return fmt.Errorf("backup run requires a target id")
		}
		out, err := c.post("/api/backup/targets/"+cmd[2]+"/run", nil)
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		if j, ok := m["job"].(map[string]any); ok {
			fmt.Printf("backup started: job=%v status=%v\n", j["id"], j["status"])
		} else {
			fmt.Println("backup started")
		}
		return nil
	case "jobs":
		out, err := c.get("/api/backup/jobs")
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		jobs, _ := m["jobs"].([]any)
		for _, j := range jobs {
			jm, _ := j.(map[string]any)
			fmt.Printf("%-22s %-9s %3d%% %s\n", jm["id"], jm["status"], toInt(jm["progress"]), jm["stage"])
		}
		return nil
	case "schedules":
		if len(cmd) < 3 || cmd[2] != "list" {
			return fmt.Errorf("backup schedules requires 'list'")
		}
		out, err := c.get("/api/backup/schedules")
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		scheds, _ := m["schedules"].([]any)
		for _, s := range scheds {
			sm, _ := s.(map[string]any)
			fmt.Printf("%-22s %-16s %-10s target=%v\n", sm["id"], sm["name"], sm["enabled"], sm["target_id"])
		}
		return nil
	}
	return fmt.Errorf("unknown backup subcommand %q", cmd[1])
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}
