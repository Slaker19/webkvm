// webkvm-cli is a powerful command-line client for the WebKVM REST API.
// It supports zero-token local administration on the host, remote login,
// JSON output mode for scripting, and comprehensive VM/storage/image management.
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/term"
)

var (
	jsonOutput bool
)

func main() {
	server := os.Getenv("WEBKVM_SERVER")
	if server == "" {
		server = "https://127.0.0.1:8080"
	}
	tokenFlag := os.Getenv("WEBKVM_TOKEN")
	insecure := os.Getenv("WEBKVM_INSECURE") != "0"

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
				tokenFlag = args[i]
			}
		case "--insecure":
			insecure = true
		case "--secure":
			insecure = false
		case "--json":
			jsonOutput = true
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

	server = strings.TrimSuffix(server, "/")

	// Handle local/remote login and logout before requiring resolved token
	if command[0] == "login" {
		if err := runLogin(server, insecure, command[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if command[0] == "logout" {
		_ = os.Remove(tokenConfigPath())
		fmt.Println("logged out (cached token removed)")
		return
	}

	// Resolve token: flag/env -> local jwt.key on host (Zero-Token) -> cached token in ~/.config/webkvm/token
	token := resolveToken(server, tokenFlag)
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: authentication required.")
		fmt.Fprintln(os.Stderr, "  - Run 'webkvm-cli login' to authenticate with username/password, or")
		fmt.Fprintln(os.Stderr, "  - Run on the host as root for zero-token local access, or")
		fmt.Fprintln(os.Stderr, "  - Provide --token <TOKEN> or WEBKVM_TOKEN environment variable.")
		os.Exit(2)
	}

	if err := run(server, token, insecure, command); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`webkvm-cli — WebKVM Management CLI

Usage: webkvm-cli [OPTIONS] <COMMAND> [ARGS...]

Options:
  --server URL    WebKVM server URL (default: https://127.0.0.1:8080 or $WEBKVM_SERVER)
  --token TOKEN   API token or JWT (or $WEBKVM_TOKEN). Auto-detected on localhost!
  --insecure      Skip TLS verification for self-signed certificates (default: true)
  --json          Output raw JSON (ideal for jq and scripting)
  -h, --help      Show this help message

Authentication Commands:
  login [user] [pass]  Log in to remote server and save session token locally
  logout               Clear locally saved session token

System Commands:
  status               Check server status & health
  info                 Show host system hardware & virtualization info
  restart              Restart WebKVM backend service
  settings list        List all server settings and values
  settings get <key>   Get value of a setting
  settings set <k> <v> Update setting value
  audit list [--limit] View system audit log entries

Virtual Machine & Container Commands:
  vms list             List all VMs and Incus containers
  vms show <id>        Show full details of a VM/container
  vms create [flags]   Create a new VM or container (see below)
  vms start <id>       Start a VM/container
  vms stop <id>        Gracefully shutdown a VM/container
  vms forceoff <id>    Force power off (destroy) a VM/container
  vms reboot <id>      Reboot a VM/container
  vms suspend <id>     Suspend/freeze a VM/container
  vms resume <id>      Resume/unfreeze a VM/container
  vms delete <id> [--disks]  Delete a VM/container (add --disks to also erase its disk files)
  vms clone <id>       Clone a VM/container
  vms autostart <id> <on|off>
  vms snapshots <id>   List snapshots
  vms snapshot <id> create <name>
  vms snapshot <id> revert <sid>
  vms snapshot <id> delete <sid>

Image Hub Commands:
  images list          List all cloud base images and container images
  images cloud         List cloud base images (.qcow2)
  images containers    List Incus container images
  images pull-cloud <id>       Download cloud base image (e.g. ubuntu-24.04, alpine-3.24)
  images pull-container <ref>  Download container image (e.g. images:alpine/3.21)
  images delete-cloud <id>     Delete cached cloud base image
  images delete-container <fp> Delete cached container image

Storage Commands:
  storage pools        List storage pools
  storage volumes [p]  List disk volumes
  storage isos         List ISO images
  storage download-iso --url <url> --name <name> --pool <pool>
  storage pool-create <name> [--type dir|zfs|btrfs|lvm] [--purpose disk|iso|container|backup|template] [--path <path>]
  storage pool-delete <name>

Host Disk Commands ("disks" is shorthand for "host disks"):
  host disks list          List physical disks and partitions (size, fstype, mountpoints)
  host disks filesystems   List filesystems this host can format with (ext4/xfs/btrfs/f2fs)
  host disks probe <image> Inspect a disk image: format, size, whether it already has data [--deep]
  host disks wipe <disk>   Erase partition table + signatures [--yes to skip confirmation]
  host disks init --disk <path> --name <volume-name>
      [--mount <path>]                 Mount point (default: /mnt/<name>)
      [--fs ext4|xfs|btrfs|f2fs]       Filesystem for --mode format (default: ext4)
      [--mode format|mount]            format = partition+mkfs (DESTRUCTIVE); mount = keep data
      [--device <dev>]                 Exact partition to mount in --mode mount
      [--purpose disk|iso|container|backup|template]   Storage pool purpose (single value: one pool per purpose)
      [--subfolders isos,discos,contenedores,backups,plantillas]
      [--no-pool]                      Don't register a storage pool
      [--no-backup]                    Don't register backups/ as a backup target
      [--nofail false] [--automount false] [--ro]
      [--opts compress=zstd,noatime]   Extra mount options

Cloud-Init Snippets:
  snippets list        List all Cloud-Init recipes and snippets
  snippets show <id>   Display snippet details and YAML content
  snippets create --name <name> [--file <path>] [--type user-data] [--category custom]
  snippets delete <id> Delete a custom snippet

Network Commands:
  networks list        List virtual networks and bridges
  networks show <id>   Show network details
  networks start <id> | stop <id> | delete <id>
  networks leases <id> List active DHCP leases on network

User & Token Commands:
  users list           List user accounts
  users create <user> <pass> [--role admin|operator|viewer]
  users delete <user>
  tokens list          List API tokens
  tokens create <name> Create new API token
  tokens revoke <id>   Revoke an API token by id (from 'tokens list --json')

Backup Commands:
  backup targets list  List backup storage targets
  backup run <target>  Trigger backup job
  backup jobs          List backup history

Examples:
  webkvm-cli vms list
  webkvm-cli vms create --name dev-box --ram 2048 --vcpus 2 --disk 20 --image debian-12
  webkvm-cli vms create --name alpine-ct --ram 512 --vcpus 1 --type container --image images:alpine/3.21
  webkvm-cli images pull-cloud ubuntu-24.04
  webkvm-cli settings set terminal.idle_timeout_min 0
  webkvm-cli disks list
  webkvm-cli disks init --disk /dev/sdb --name fast-ssd --fs btrfs --opts compress=zstd
  webkvm-cli disks init --disk /dev/sdb --name recovered --mode mount
  webkvm-cli --json vms list | jq .`)
}

func tokenConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "webkvm", "token")
}

// resolveToken implements the Zero-Token local resolution and fallback mechanism.
func resolveToken(server, tokenFlag string) string {
	if tokenFlag != "" {
		return tokenFlag
	}

	// 1. If connecting locally, check if local host jwt.key is available to generate admin JWT on the fly
	isLocal := false
	if u, err := url.Parse(server); err == nil {
		h := u.Hostname()
		if h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "" {
			isLocal = true
		}
	}

	if isLocal {
		dataDir := os.Getenv("DATA_DIR")
		if dataDir == "" {
			dataDir = "/opt/webkvm"
		}
		candidates := []string{
			filepath.Join(dataDir, "jwt.key"),
			"/opt/webkvm/jwt.key",
			"/etc/webkvm/jwt.key",
		}
		for _, path := range candidates {
			if data, err := os.ReadFile(path); err == nil {
				secret := strings.TrimSpace(string(data))
				if len(secret) >= 16 {
					adminEpoch := 0
					if uData, err := os.ReadFile(filepath.Join(filepath.Dir(path), "users.json")); err == nil {
						var userList []struct {
							Username     string `json:"username"`
							SessionEpoch int    `json:"session_epoch"`
						}
						if err := json.Unmarshal(uData, &userList); err == nil {
							for _, u := range userList {
								if u.Username == "admin" {
									adminEpoch = u.SessionEpoch
									break
								}
							}
						}
					}
					claims := jwt.MapClaims{
						"username": "admin",
						"role":     "admin",
						"iss":      "webkvm",
						"mcp":      false,
						"epoch":    adminEpoch,
						"exp":      time.Now().Add(2 * time.Hour).Unix(),
						"iat":      time.Now().Unix(),
					}
					t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
					if signed, err := t.SignedString([]byte(secret)); err == nil {
						return signed
					}
				}
			}
		}
	}

	// 2. Check cached token in ~/.config/webkvm/token
	if data, err := os.ReadFile(tokenConfigPath()); err == nil {
		tk := strings.TrimSpace(string(data))
		if tk != "" {
			return tk
		}
	}

	return ""
}

func runLogin(server string, insecure bool, args []string) error {
	username := ""
	password := ""

	if len(args) >= 1 {
		username = args[0]
	}
	if len(args) >= 2 {
		password = args[1]
	}

	reader := bufio.NewReader(os.Stdin)
	if username == "" {
		fmt.Print("Username: ")
		u, _ := reader.ReadString('\n')
		username = strings.TrimSpace(u)
	}

	if password == "" {
		fmt.Print("Password: ")
		if term.IsTerminal(int(os.Stdin.Fd())) {
			bytePassword, err := term.ReadPassword(int(os.Stdin.Fd()))
			if err != nil {
				return err
			}
			password = string(bytePassword)
			fmt.Println()
		} else {
			p, _ := reader.ReadString('\n')
			password = strings.TrimSpace(p)
		}
	}

	c := newClient(server, "", insecure)
	body := map[string]string{"username": username, "password": password}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", c.server+"/api/auth/login", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	defer resp.Body.Close()
	res, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("login failed (%s): %s", resp.Status, string(res))
	}

	var m map[string]any
	_ = json.Unmarshal(res, &m)

	token, _ := m["token"].(string)
	if token == "" {
		for _, cookie := range resp.Cookies() {
			if (cookie.Name == "webkvm_session" || cookie.Name == "webkvm_token") && cookie.Value != "" {
				token = cookie.Value
				break
			}
		}
	}
	if token == "" {
		if m["require_2fa"] == true {
			return fmt.Errorf("2FA is enabled for this account; please log in via Web UI or use an API token")
		}
		return fmt.Errorf("login response did not contain token: %s", string(res))
	}

	cfgPath := tokenConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err == nil {
		_ = os.WriteFile(cfgPath, []byte(token), 0600)
	}

	fmt.Printf("login successful as %q! Session cached to %s\n", username, cfgPath)
	return nil
}

type client struct {
	server   string
	token    string
	http     *http.Client
	insecure bool
}

func newClient(server, token string, insecure bool) *client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &client{
		server:   server,
		token:    token,
		http:     &http.Client{Timeout: 120 * time.Second, Transport: tr},
		insecure: insecure,
	}
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
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

func (c *client) get(path string) ([]byte, error)            { return c.do("GET", path, nil) }
func (c *client) post(path string, body any) ([]byte, error) { return c.do("POST", path, body) }
func (c *client) put(path string, body any) ([]byte, error)  { return c.do("PUT", path, body) }
func (c *client) del(path string) ([]byte, error)            { return c.do("DELETE", path, nil) }

func printRawJSON(data []byte) error {
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		fmt.Println(string(data))
		return nil
	}
	fmt.Println(out.String())
	return nil
}

func run(server, token string, insecure bool, cmd []string) error {
	c := newClient(server, token, insecure)

	switch cmd[0] {
	case "status":
		out, err := c.get("/api/health")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
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
		if jsonOutput {
			return printRawJSON(out)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		ramGB := toInt(m["total_ram"]) / (1024 * 1024 * 1024)
		fmt.Printf("hostname=%v arch=%v cpu=%v cores=%v libvirt=%v qemu=%v ram=%d GiB\n",
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

	case "images":
		return runImages(c, cmd)

	case "storage":
		return runStorage(c, cmd)

	// Physical host disks. "host disks ..." is the canonical form (it
	// mirrors the /api/host/disks route); "disks ..." is a shorthand
	// alias so the common case doesn't need the extra word.
	case "host":
		if len(cmd) < 2 {
			return fmt.Errorf("host requires a subcommand (disks)")
		}
		if cmd[1] != "disks" {
			return fmt.Errorf("unknown host subcommand %q (expected: disks)", cmd[1])
		}
		return runHostDisks(c, cmd[2:])

	case "disks":
		return runHostDisks(c, cmd[1:])

	case "networks":
		return runNetworks(c, cmd)

	case "settings":
		return runSettings(c, cmd)

	case "audit":
		return runAudit(c, cmd)

	case "users":
		return runUsers(c, cmd)

	case "tokens":
		return runTokens(c, cmd)

	case "backup":
		return runBackup(c, cmd)

	case "snippets":
		return runSnippets(c, cmd)
	}

	return fmt.Errorf("unknown command %q", cmd[0])
}

func runVMs(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("vms requires a subcommand (list|show|create|start|stop|forceoff|reboot|suspend|resume|delete|clone|autostart|snapshots|snapshot)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/vms")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("vms list: %w", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tSTATE\tVCPU\tRAM\tDISK\tIP")
		for _, v := range arr {
			vmType := "kvm"
			if t, _ := v["type"].(string); t != "" {
				vmType = t
			}
			ip := "-"
			if s, _ := v["ip"].(string); s != "" {
				ip = s
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%d\t%d MB\t%d GB\t%s\n",
				v["id"], v["name"], vmType, v["state"], toInt(v["vcpus"]), toInt(v["ram_mb"]), toInt(v["disk_gb"]), ip)
		}
		return w.Flush()

	case "show":
		if len(cmd) < 3 {
			return fmt.Errorf("vms show requires a VM id")
		}
		out, err := c.get("/api/vms/" + cmd[2])
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			return fmt.Errorf("vms show: %w", err)
		}
		fmt.Printf("ID:          %v\n", v["id"])
		fmt.Printf("Name:        %v\n", v["name"])
		if t, _ := v["type"].(string); t != "" {
			fmt.Printf("Type:        %v\n", t)
		}
		fmt.Printf("State:       %v\n", v["state"])
		fmt.Printf("CPU:         %v vCPU\n", toInt(v["vcpus"]))
		fmt.Printf("RAM:         %v MB\n", toInt(v["ram_mb"]))
		fmt.Printf("Disk:        %v GB\n", toInt(v["disk_gb"]))
		if ip, _ := v["ip"].(string); ip != "" {
			fmt.Printf("IP:          %v\n", ip)
		}
		fmt.Printf("Autostart:   %v\n", v["autostart"])
		if disks, ok := v["disks"].([]any); ok {
			fmt.Printf("Disks:       %d\n", len(disks))
			for _, d := range disks {
				// models.DiskInfo has no "dev" key — it's "target"
				// (vda, sda...); "dev" always evaluated to <nil>.
				dm, _ := d.(map[string]any)
				line := fmt.Sprintf("  - %v (%v", dm["target"], dm["name"])
				if dm["device"] == "cdrom" {
					line += ", cdrom"
				} else if sz := toInt(dm["size_gb"]); sz > 0 {
					line += fmt.Sprintf(", %d GB", sz)
				}
				if pool, _ := dm["pool"].(string); pool != "" {
					line += fmt.Sprintf(", pool: %v", pool)
				}
				fmt.Println(line + ")")
			}
		}
		if nets, ok := v["networks"].([]any); ok {
			fmt.Printf("Networks:    %d\n", len(nets))
			for _, n := range nets {
				nm, _ := n.(map[string]any)
				// models.NetIface.Network is only set for libvirt
				// "network" (NAT) interfaces; a "bridge" interface (the
				// common KVM case, e.g. vmbr0) leaves Network empty and
				// carries the bridge name in Source instead. Printing
				// Network unconditionally showed a blank value for
				// every bridged NIC.
				target, _ := nm["network"].(string)
				if target == "" {
					target, _ = nm["source"].(string)
				}
				fmt.Printf("  - %v (%v: %v)\n", nm["mac"], nm["type"], target)
			}
		}
		return nil

	case "create":
		flags, _ := parseFlags(cmd[2:])
		name := flags["name"]
		if name == "" {
			return fmt.Errorf("vms create requires --name <name>")
		}
		vcpus := 2
		if v, ok := flags["vcpus"]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				vcpus = n
			}
		}
		ramMB := int64(2048)
		if v, ok := flags["ram"]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				ramMB = n
			}
		}
		diskGB := int64(20)
		if v, ok := flags["disk"]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				diskGB = n
			}
		}
		vmType := flags["type"]
		if vmType == "" {
			vmType = "vm"
		}
		reqBody := map[string]any{
			"name":         name,
			"type":         vmType,
			"vcpus":        vcpus,
			"ram_mb":       ramMB,
			"disk_gb":      diskGB,
			"storage_pool": flags["pool"],
			"network":      flags["network"],
			"image":        flags["image"],
			"iso":          flags["iso"],
		}

		if flags["user"] != "" || flags["password"] != "" || flags["ssh-key"] != "" || flags["hostname"] != "" {
			reqBody["cloud_init"] = map[string]any{
				"user":     flags["user"],
				"password": flags["password"],
				"ssh_key":  flags["ssh-key"],
				"hostname": flags["hostname"],
			}
		}

		out, err := c.post("/api/vms", reqBody)
		if err != nil {
			return fmt.Errorf("create instance: %w", err)
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var created map[string]any
		_ = json.Unmarshal(out, &created)
		// POST /api/vms only ever answers {id, name[, password,
		// password_warning]} (see CreateVM) — it never had a "state"
		// key, so this used to print "state: <nil>" on every single
		// successful create.
		fmt.Printf("instance %q created successfully (id: %v)\n", name, created["id"])
		if pw, ok := created["password"].(string); ok && pw != "" {
			fmt.Printf("password: %s\n", pw)
			if w, ok := created["password_warning"].(string); ok && w != "" {
				fmt.Println(w)
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
		// The API defaults to keeping disk files on a bare DELETE (a
		// deliberate data-loss guard — see DeleteVM/?disks=true in the
		// backend and the two-step confirmation in the web UI's
		// DeleteVmDialog). The CLI never exposed that flag, so every
		// "vms delete" left orphaned qcow2/cloud-init ISOs behind on
		// the pool with no way to ask for full cleanup in one command.
		flags, _ := parseFlags(cmd[3:])
		path := "/api/vms/" + cmd[2]
		if flags["disks"] == "true" {
			path += "?disks=true"
		}
		out, err := c.del(path)
		if err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", cmd[2])
		if flags["disks"] == "true" {
			var res struct {
				DisksDeleted []string `json:"disks_deleted"`
				DisksKept    []string `json:"disks_kept"`
			}
			if json.Unmarshal(out, &res) == nil {
				if len(res.DisksDeleted) > 0 {
					fmt.Printf("disks deleted: %s\n", strings.Join(res.DisksDeleted, ", "))
				}
				if len(res.DisksKept) > 0 {
					fmt.Printf("disks kept (still in use or locked): %s\n", strings.Join(res.DisksKept, ", "))
				}
			}
		}
		return nil

	case "clone":
		if len(cmd) < 3 {
			return fmt.Errorf("vms clone requires a VM id")
		}
		out, err := c.post("/api/vms/"+cmd[2]+"/clone", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		if id, _ := m["id"].(string); id != "" {
			fmt.Printf("cloned to %s\n", id)
			return nil
		}
		jobID, _ := m["job"].(string)
		if jobID == "" {
			return fmt.Errorf("clone failed: %s", out)
		}
		for i := 0; i < 600; i++ {
			time.Sleep(500 * time.Millisecond)
			jobOut, jerr := c.get("/api/jobs/" + jobID)
			if jerr != nil {
				return jerr
			}
			var j map[string]any
			_ = json.Unmarshal(jobOut, &j)
			switch j["status"] {
			case "done":
				if res, ok := j["result"].(map[string]any); ok {
					if id, _ := res["id"].(string); id != "" {
						fmt.Printf("cloned to %s\n", id)
					} else {
						fmt.Println("clone ok")
					}
				} else {
					fmt.Println("clone ok")
				}
				return nil
			case "error":
				return fmt.Errorf("clone failed: %s", j["error"])
			}
		}
		return fmt.Errorf("clone timed out waiting for job %s", jobID)

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
		if jsonOutput {
			return printRawJSON(out)
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
			return fmt.Errorf("vms snapshot requires <id> <create|revert|delete> <name/sid>")
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
			if jsonOutput {
				return printRawJSON(out)
			}
			fmt.Println("snapshot created successfully")
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
	}
	return fmt.Errorf("unknown vms subcommand %q", cmd[1])
}

func runImages(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("images requires a subcommand (list|cloud|containers|pull-cloud|pull-container|delete-cloud|delete-container)")
	}
	switch cmd[1] {
	case "list", "cloud":
		out, err := c.get("/api/images/cloud-base")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tLOCAL CACHED\tSIZE")
		for _, img := range arr {
			// API field names (internal/api/image_hub.go CloudBaseImage):
			// is_cached / size_bytes — NOT is_local / size (those belong
			// to the unrelated Incus IncusImageItem shape below). Using
			// the wrong keys here always evaluated to "No" / "-" even
			// for images that were fully downloaded and cached on disk.
			local := "No"
			if img["is_cached"] == true {
				local = "Yes (Cached)"
			}
			sizeStr := "-"
			if sz := toInt(img["size_bytes"]); sz > 0 {
				sizeStr = fmt.Sprintf("%.1f MB", float64(sz)/(1024*1024))
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", img["id"], img["name"], local, sizeStr)
		}
		_ = w.Flush()
		if cmd[1] == "cloud" {
			return nil
		}
		fmt.Println()
		fallthrough

	case "containers":
		out, err := c.get("/api/vms/incus-images")
		if err != nil {
			return nil // Incus might be disabled
		}
		if jsonOutput && cmd[1] == "containers" {
			return printRawJSON(out)
		}
		// GET /api/vms/incus-images returns an ENVELOPE
		// ({images, host_arch, incus_enabled} — models.IncusImagesResponse),
		// not a bare array. Unmarshalling straight into []map[string]any
		// always failed silently (arr stays nil), so every cached
		// container image was hidden and the table printed empty even
		// when local images existed.
		var resp struct {
			Images []map[string]any `json:"images"`
		}
		_ = json.Unmarshal(out, &resp)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "REF\tLABEL\tLOCAL\tARCH")
		for _, img := range resp.Images {
			local := "Remote"
			if img["is_local"] == true {
				local = "Local Cache"
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", img["ref"], img["label"], local, img["arch"])
		}
		return w.Flush()

	case "pull-cloud":
		if len(cmd) < 3 {
			return fmt.Errorf("images pull-cloud requires image id (e.g. ubuntu-24.04, alpine-3.24)")
		}
		out, err := c.post("/api/images/cloud-base/pull", map[string]string{"id": cmd[2]})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		fmt.Printf("download started: job_id=%v\n", res["job_id"])
		return nil

	case "pull-container":
		if len(cmd) < 3 {
			return fmt.Errorf("images pull-container requires image ref (e.g. images:alpine/3.21)")
		}
		out, err := c.post("/api/images/containers/pull", map[string]string{"ref": cmd[2]})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		fmt.Printf("pull started: job_id=%v\n", res["job_id"])
		return nil

	case "delete-cloud":
		if len(cmd) < 3 {
			return fmt.Errorf("images delete-cloud requires image id")
		}
		if _, err := c.del("/api/images/cloud-base/" + cmd[2]); err != nil {
			return err
		}
		fmt.Printf("deleted cloud image %s\n", cmd[2])
		return nil

	case "delete-container":
		if len(cmd) < 3 {
			return fmt.Errorf("images delete-container requires fingerprint")
		}
		if _, err := c.del("/api/images/containers/" + cmd[2]); err != nil {
			return err
		}
		fmt.Printf("deleted container image %s\n", cmd[2])
		return nil
	}

	return fmt.Errorf("unknown images subcommand %q", cmd[1])
}

func runStorage(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("storage requires a subcommand (pools|pool-create|pool-retag|pool-delete|volumes|isos|download-iso)")
	}
	switch cmd[1] {
	case "pools":
		out, err := c.get("/api/storage/pools")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE\tPURPOSE\tSTATE\tAVAILABLE\tPATH")
		for _, p := range arr {
			avail := toInt(p["available"]) / (1024 * 1024 * 1024)
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%d GiB\t%v\n", p["name"], p["type"], p["purpose"], p["state"], avail, p["path"])
		}
		return w.Flush()

	case "volumes":
		pool := ""
		if len(cmd) > 2 {
			pool = cmd[2]
		}
		url := "/api/storage/volumes"
		if pool != "" {
			url += "?pool=" + pool
		}
		out, err := c.get(url)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPOOL\tFORMAT\tCAPACITY\tPATH")
		for _, v := range arr {
			gb := toInt(v["capacity"]) / (1024 * 1024 * 1024)
			fmt.Fprintf(w, "%v\t%v\t%v\t%d GiB\t%v\n", v["name"], v["pool"], v["format"], gb, v["path"])
		}
		return w.Flush()

	case "isos":
		out, err := c.get("/api/storage/isos")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPOOL\tSIZE")
		for _, iso := range arr {
			mb := toInt(iso["size"]) / (1024 * 1024)
			fmt.Fprintf(w, "%v\t%v\t%d MiB\n", iso["name"], iso["pool"], mb)
		}
		return w.Flush()

	case "download-iso":
		flags, _ := parseFlags(cmd[2:])
		url := flags["url"]
		name := flags["name"]
		pool := flags["pool"]
		if url == "" || name == "" {
			return fmt.Errorf("storage download-iso requires --url <url> --name <name> [--pool <pool>]")
		}
		out, err := c.post("/api/storage/isos/download", map[string]string{
			"url":  url,
			"name": name,
			"pool": pool,
		})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		fmt.Printf("ISO download started: job_id=%v\n", res["job_id"])
		return nil

	case "pool-create":
		flags, args := parseFlags(cmd[2:])
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		if name == "" {
			name = flags["name"]
		}
		if name == "" {
			return fmt.Errorf("storage pool-create requires a pool name: webkvm-cli storage pool-create <name> [--type dir|zfs|btrfs|lvm] [--purpose disk|iso|container|backup|template] [--path <path>]")
		}
		poolType := flags["type"]
		if poolType == "" {
			poolType = "dir"
		}
		purpose := flags["purpose"]
		if purpose == "" {
			purpose = "disk"
		}
		reqBody := map[string]string{
			"name":    name,
			"type":    poolType,
			"purpose": purpose,
			"path":    flags["path"],
		}
		out, err := c.post("/api/storage/pools", reqBody)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("storage pool %q created successfully\n", name)
		return nil

	case "pool-retag":
		// Retagging writes pool-purposes.json only: no data moves and
		// the pool keeps running. The server refuses the change when
		// the pool already holds files the new purpose would hide.
		flags, args := parseFlags(cmd[2:])
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		if name == "" {
			name = flags["name"]
		}
		purpose := flags["purpose"]
		if name == "" || purpose == "" {
			return fmt.Errorf("storage pool-retag requires a pool name and a purpose: webkvm-cli storage pool-retag <name> --purpose disk|iso|container|backup|template")
		}
		out, err := c.put("/api/storage/pools/"+url.PathEscape(name),
			map[string]string{"purpose": purpose})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("storage pool %q retagged as %q\n", name, purpose)
		return nil

	case "pool-delete":
		if len(cmd) < 3 {
			return fmt.Errorf("storage pool-delete requires a pool name: webkvm-cli storage pool-delete <name>")
		}
		name := cmd[2]
		out, err := c.del("/api/storage/pools/" + url.PathEscape(name))
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("storage pool %q deleted successfully\n", name)
		return nil
	}

	return fmt.Errorf("unknown storage subcommand %q", cmd[1])
}

func runNetworks(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("networks requires a subcommand (list|show|start|stop|delete|leases)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/networks")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tFORWARD\tCIDR\tACTIVE\tAUTOSTART")
		for _, n := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", n["name"], n["forward"], n["cidr"], n["active"], n["autostart"])
		}
		return w.Flush()

	case "show":
		if len(cmd) < 3 {
			return fmt.Errorf("networks show requires a network id")
		}
		out, err := c.get("/api/networks")
		if err != nil {
			return err
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		want := cmd[2]
		for _, n := range arr {
			if n["name"] == want {
				if jsonOutput {
					b, _ := json.Marshal(n)
					return printRawJSON(b)
				}
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
		fmt.Printf("deleted network %s\n", cmd[2])
		return nil

	case "leases":
		if len(cmd) < 3 {
			return fmt.Errorf("networks leases requires network name")
		}
		out, err := c.get("/api/networks/" + cmd[2] + "/leases")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "IP\tMAC\tHOSTNAME\tEXPIRY")
		for _, l := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", l["ip"], l["mac"], l["hostname"], l["expiry"])
		}
		return w.Flush()
	}

	return fmt.Errorf("unknown networks subcommand %q", cmd[1])
}

func runSettings(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("settings requires a subcommand (list|get|set)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/settings")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res struct {
			Values map[string]any `json:"values"`
		}
		_ = json.Unmarshal(out, &res)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "KEY\tVALUE")
		for k, v := range res.Values {
			fmt.Fprintf(w, "%v\t%v\n", k, v)
		}
		return w.Flush()

	case "get":
		if len(cmd) < 3 {
			return fmt.Errorf("settings get requires a key")
		}
		out, err := c.get("/api/settings")
		if err != nil {
			return err
		}
		var res struct {
			Values map[string]any `json:"values"`
		}
		_ = json.Unmarshal(out, &res)
		if v, ok := res.Values[cmd[2]]; ok {
			if jsonOutput {
				b, _ := json.Marshal(v)
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("%v: %v\n", cmd[2], v)
			return nil
		}
		return fmt.Errorf("setting %q not found", cmd[2])

	case "set":
		if len(cmd) < 4 {
			return fmt.Errorf("settings set requires <key> <value>")
		}
		key := cmd[2]
		rawVal := cmd[3]
		var val any = rawVal
		if rawVal == "true" {
			val = true
		} else if rawVal == "false" {
			val = false
		} else if n, err := strconv.ParseInt(rawVal, 10, 64); err == nil {
			val = n
		}

		out, err := c.put("/api/settings", map[string]any{"values": map[string]any{key: val}})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("setting %s = %v updated successfully\n", key, val)
		return nil
	}
	return fmt.Errorf("unknown settings subcommand %q", cmd[1])
}

func runAudit(c *client, cmd []string) error {
	limit := 20
	flags, _ := parseFlags(cmd[1:])
	if l, ok := flags["limit"]; ok {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	out, err := c.get(fmt.Sprintf("/api/audit?limit=%d", limit))
	if err != nil {
		return err
	}
	if jsonOutput {
		return printRawJSON(out)
	}
	var res struct {
		Entries []map[string]any `json:"entries"`
		Total   int              `json:"total"`
	}
	_ = json.Unmarshal(out, &res)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tUSER\tROLE\tACTION\tRESOURCE\tIP")
	for _, e := range res.Entries {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n", e["time"], e["user"], e["role"], e["action"], e["resource"], e["ip"])
	}
	return w.Flush()
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
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "USERNAME\tROLE\tACTIVE\tEMAIL")
		for _, u := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", u["username"], u["role"], u["active"], u["email"])
		}
		return w.Flush()

	case "create":
		if len(cmd) < 4 {
			return fmt.Errorf("users create requires <username> <password> [--role <r>]")
		}
		flags, _ := parseFlags(cmd[4:])
		role := flags["role"]
		if role == "" {
			role = "operator"
		}
		out, err := c.post("/api/users", map[string]any{
			"username": cmd[2],
			"password": cmd[3],
			"role":     role,
			"email":    flags["email"],
		})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("user %s created ok\n", cmd[2])
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

// runHostDisks implements the physical-disk management commands that
// mirror the /api/host/disks* routes used by the Storage > Host Disks
// page. cmd is already stripped of the "host disks" / "disks" prefix,
// so cmd[0] is the subcommand.
func runHostDisks(c *client, cmd []string) error {
	if len(cmd) < 1 {
		return fmt.Errorf("disks requires a subcommand (list|filesystems|wipe|init)")
	}
	switch cmd[0] {
	case "list":
		out, err := c.get("/api/host/disks")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("disks list: %w", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tSIZE\tTYPE\tFSTYPE\tROLE\tMODEL\tMOUNTPOINTS")
		for _, d := range arr {
			printDiskRow(w, d, false)
			for _, rawChild := range asSlice(d["children"]) {
				child, ok := rawChild.(map[string]any)
				if !ok {
					continue
				}
				printDiskRow(w, child, true)
			}
		}
		return w.Flush()

	case "probe":
		flags, pos := parseFlags(cmd[1:])
		src := ""
		if len(pos) > 0 {
			src = pos[0]
		}
		if src == "" {
			src = flags["source"]
		}
		if src == "" {
			return fmt.Errorf("disks probe requires a disk image path: webkvm-cli disks probe <path> [--deep]")
		}
		_, deep := flags["deep"]
		out, err := c.post("/api/storage/probe-disk", map[string]any{"path": src, "deep": deep})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res map[string]any
		if err := json.Unmarshal(out, &res); err != nil {
			return fmt.Errorf("disks probe: %w", err)
		}
		// A deep probe returns 202 + a job; tell the caller how to poll it.
		if jobID, ok := res["id"].(string); ok && res["status"] == "running" {
			fmt.Printf("Deep inspection started (job %s). Poll with: webkvm-cli jobs show %s\n", jobID, jobID)
			return nil
		}
		hasData := "no"
		if b, _ := res["has_data"].(bool); b {
			hasData = "yes"
		}
		fmt.Printf("Path:        %v\n", res["path"])
		fmt.Printf("Format:      %v\n", res["format"])
		fmt.Printf("VirtualSize: %v bytes\n", toInt(res["virtual_size"]))
		fmt.Printf("Allocated:   %v bytes\n", toInt(res["allocated"]))
		fmt.Printf("HasData:     %s\n", hasData)
		if bf, _ := res["backing_file"].(string); bf != "" {
			fmt.Printf("BackingFile: %s\n", bf)
		}
		if osn, _ := res["os"].(string); osn != "" {
			fmt.Printf("OS:          %s\n", osn)
		}
		if w, _ := res["warning"].(string); w != "" {
			fmt.Printf("Warning:     %s\n", w)
		}
		return nil

	case "filesystems", "fs":
		out, err := c.get("/api/host/disks/filesystems")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		if err := json.Unmarshal(out, &arr); err != nil {
			return fmt.Errorf("disks filesystems: %w", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tLABEL\tAVAILABLE")
		for _, fs := range arr {
			avail := "no"
			if b, _ := fs["available"].(bool); b {
				avail = "yes"
			}
			fmt.Fprintf(w, "%v\t%v\t%s\n", fs["id"], fs["label"], avail)
		}
		return w.Flush()

	case "wipe":
		flags, pos := parseFlags(cmd[1:])
		disk := ""
		if len(pos) > 0 {
			disk = pos[0]
		}
		if disk == "" {
			disk = flags["disk"]
		}
		if disk == "" {
			return fmt.Errorf("disks wipe requires a disk path: webkvm-cli disks wipe /dev/sdb [--yes]")
		}
		// Destructive: require an explicit confirmation unless --yes.
		// The backend refuses to wipe a mounted disk, but a wipe of an
		// unmounted data disk is still irreversible.
		if flags["yes"] != "true" && flags["force"] != "true" {
			fmt.Printf("This ERASES the partition table and all filesystem signatures on %s.\n", disk)
			fmt.Printf("Type the disk path again to confirm: ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(answer) != disk {
				return fmt.Errorf("confirmation did not match; aborted")
			}
		}
		out, err := c.post("/api/host/disks/wipe", map[string]string{"disk_path": disk})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("disk %s wiped\n", disk)
		return nil

	case "init":
		flags, _ := parseFlags(cmd[1:])
		disk := flags["disk"]
		name := flags["name"]
		if disk == "" || name == "" {
			return fmt.Errorf("disks init requires --disk <path> --name <volume-name>\n" +
				"  optional: --mount <path> --fs <ext4|xfs|btrfs|f2fs> --mode <format|mount> --device <dev>\n" +
				"            --purpose <disk|iso|container|backup|template> --subfolders <isos,discos,contenedores,backups,plantillas>\n" +
				"            --no-pool --no-backup --nofail --automount --ro --opts <csv>")
		}
		mode := flags["mode"]
		if mode == "" {
			mode = "format"
		}
		body := map[string]any{
			"disk_path":   disk,
			"volume_name": name,
			"mode":        mode,
			// Default to registering a pool (matches the UI); --no-pool opts out.
			"create_pool": flags["no-pool"] != "true",
			// Boot-resilience defaults mirror the Storage dialog: a data
			// disk must never stall the host's boot.
			"nofail":    flags["nofail"] != "false",
			"automount": flags["automount"] != "false",
			"read_only": flags["ro"] == "true",
		}
		if v := flags["mount"]; v != "" {
			body["mount_point"] = v
		}
		if v := flags["fs"]; v != "" && mode == "format" {
			body["filesystem"] = v
		}
		if v := flags["device"]; v != "" {
			body["device"] = v
		}
		if v := flags["purpose"]; v != "" {
			body["pool_purpose"] = v
		}
		subfolders := splitCSV(flags["subfolders"])
		if len(subfolders) > 0 {
			body["subfolders"] = subfolders
			body["register_backup_target"] = flags["no-backup"] != "true" && containsStr(subfolders, "backups")
		}
		if opts := splitCSV(flags["opts"]); len(opts) > 0 {
			body["mount_options"] = opts
		}
		out, err := c.post("/api/host/disks/initialize-directory", body)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		fmt.Printf("disk %v initialized: device=%v fs=%v mounted at %v (mode=%v)\n",
			res["disk"], res["device"], res["filesystem"], res["mount_point"], res["mode"])
		if pools, ok := res["pools"].([]any); ok && len(pools) > 0 {
			names := make([]string, 0, len(pools))
			for _, p := range pools {
				if m, ok := p.(map[string]any); ok {
					names = append(names, fmt.Sprintf("%v(%v)", m["name"], m["status"]))
				}
			}
			fmt.Printf("pools: %s\n", strings.Join(names, ", "))
		}
		return nil
	}
	return fmt.Errorf("unknown disks subcommand %q (expected: list|filesystems|wipe|init)", cmd[0])
}

// printDiskRow renders one lsblk-style row. Children (partitions) are
// indented so the disk/partition hierarchy stays readable in a flat table.
func printDiskRow(w *tabwriter.Writer, d map[string]any, child bool) {
	path, _ := d["path"].(string)
	if child {
		path = "  └─ " + path
	}
	role := "data"
	if b, _ := d["is_system"].(bool); b {
		role = "SYSTEM"
	}
	mounts := strings.Join(toStrings(d["mountpoints"]), ",")
	if mounts == "" {
		mounts = "-"
	}
	fmt.Fprintf(w, "%s\t%v\t%v\t%v\t%s\t%v\t%s\n",
		path, dashIfEmpty(d["size_human"]), dashIfEmpty(d["type"]), dashIfEmpty(d["fstype"]),
		role, dashIfEmpty(d["model"]), mounts)
}

func dashIfEmpty(v any) string {
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func toStrings(v any) []string {
	var out []string
	for _, item := range asSlice(v) {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// splitCSV parses a comma-separated flag value into a trimmed,
// empty-free slice ("a, b ,," -> ["a","b"]).
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" || s == "true" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
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
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPREFIX\tCREATED\tLAST USED")
		for _, t := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", t["name"], t["prefix"], t["created_at"], t["last_used_at"])
		}
		return w.Flush()

	case "create":
		if len(cmd) < 3 {
			return fmt.Errorf("tokens create requires a name")
		}
		out, err := c.post("/api/tokens", map[string]any{"name": cmd[2]})
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		fmt.Printf("token created: %v\n", res["token"])
		return nil

	case "revoke":
		if len(cmd) < 3 {
			return fmt.Errorf("tokens revoke requires a token id (see 'webkvm-cli tokens list')")
		}
		id := cmd[2]
		out, err := c.del("/api/tokens/" + url.PathEscape(id))
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("token %s revoked\n", id)
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
		out, err := c.get("/api/backup/targets")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE\tLOCATION\tENABLED")
		for _, t := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", t["name"], t["type"], t["location"], t["enabled"])
		}
		return w.Flush()

	case "run":
		if len(cmd) < 3 {
			return fmt.Errorf("backup run requires target name")
		}
		out, err := c.post("/api/backup/targets/"+cmd[2]+"/run", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Println("backup job triggered")
		return nil

	case "jobs":
		out, err := c.get("/api/backup/jobs")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTARGET\tSTATUS\tSTARTED\tSIZE")
		for _, j := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", j["id"], j["target"], j["status"], j["started_at"], j["size_bytes"])
		}
		return w.Flush()

	case "schedules":
		out, err := c.get("/api/backup/schedules")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTARGET\tCRON\tENABLED")
		for _, s := range arr {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", s["id"], s["name"], s["target"], s["cron"], s["enabled"])
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown backup subcommand %q", cmd[1])
}

func runSnippets(c *client, cmd []string) error {
	if len(cmd) < 2 {
		return fmt.Errorf("snippets requires a subcommand (list|show|create|delete)")
	}
	switch cmd[1] {
	case "list":
		out, err := c.get("/api/cloudinit/snippets")
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var arr []map[string]any
		_ = json.Unmarshal(out, &arr)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCATEGORY\tTYPE\tPRESET")
		for _, s := range arr {
			isPreset := "Custom"
			if s["is_preset"] == true {
				isPreset = "Preset ✓"
			}
			cat, _ := s["category"].(string)
			if cat == "" {
				cat = "general"
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", s["id"], s["name"], cat, s["type"], isPreset)
		}
		return w.Flush()

	case "show":
		if len(cmd) < 3 {
			return fmt.Errorf("snippets show requires a snippet id: webkvm-cli snippets show <id>")
		}
		id := cmd[2]
		out, err := c.get("/api/cloudinit/snippets/" + url.PathEscape(id))
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var s map[string]any
		_ = json.Unmarshal(out, &s)
		fmt.Printf("ID:          %v\n", s["id"])
		fmt.Printf("Name:        %v\n", s["name"])
		fmt.Printf("Category:    %v\n", s["category"])
		fmt.Printf("Type:        %v\n", s["type"])
		fmt.Printf("Is Preset:   %v\n", s["is_preset"])
		fmt.Printf("Description: %v\n", s["description"])
		fmt.Println("--- YAML Content ---")
		fmt.Println(s["content"])
		return nil

	case "create":
		flags, _ := parseFlags(cmd[2:])
		name := flags["name"]
		filePath := flags["file"]
		if name == "" {
			return fmt.Errorf("snippets create requires --name <name>: webkvm-cli snippets create --name <name> [--file <path>] [--type user-data] [--category custom]")
		}
		content := "#cloud-config\n"
		if filePath != "" {
			b, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			content = string(b)
		}
		snType := flags["type"]
		if snType == "" {
			snType = "user-data"
		}
		cat := flags["category"]
		if cat == "" {
			cat = "custom"
		}
		desc := flags["desc"]
		body := map[string]string{
			"name":        name,
			"description": desc,
			"category":    cat,
			"type":        snType,
			"content":     content,
		}
		out, err := c.post("/api/cloudinit/snippets", body)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		var created map[string]any
		_ = json.Unmarshal(out, &created)
		fmt.Printf("snippet %q created successfully (id=%v)\n", name, created["id"])
		return nil

	case "delete":
		if len(cmd) < 3 {
			return fmt.Errorf("snippets delete requires a snippet id: webkvm-cli snippets delete <id>")
		}
		id := cmd[2]
		out, err := c.del("/api/cloudinit/snippets/" + url.PathEscape(id))
		if err != nil {
			return err
		}
		if jsonOutput {
			return printRawJSON(out)
		}
		fmt.Printf("snippet %q deleted successfully\n", id)
		return nil
	}
	return fmt.Errorf("unknown snippets subcommand %q", cmd[1])
}

func isBoolFlag(f string) bool {
	switch f {
	case "deep", "yes", "force", "no-pool", "no-backup", "ro", "json", "insecure", "secure", "help", "h", "enabled":
		return true
	default:
		return false
	}
}

func parseFlags(args []string) (map[string]string, []string) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			key := strings.TrimPrefix(args[i], "--")
			if eq := strings.Index(key, "="); eq != -1 {
				flags[key[:eq]] = key[eq+1:]
			} else if isBoolFlag(key) {
				if i+1 < len(args) && (args[i+1] == "true" || args[i+1] == "false") {
					flags[key] = args[i+1]
					i++
				} else {
					flags[key] = "true"
				}
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				flags[key] = args[i+1]
				i++
			} else {
				flags[key] = "true"
			}
		} else {
			pos = append(pos, args[i])
		}
	}
	return flags, pos
}

func toInt(v any) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}
