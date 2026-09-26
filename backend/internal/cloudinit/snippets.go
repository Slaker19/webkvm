package cloudinit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"
)

// Snippet is a reusable Cloud-Init template snippet.
type Snippet struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category,omitempty"` // containers, kubernetes, security, monitoring, networking, web, devops
	Type        string    `json:"type"`               // user-data | meta-data | network-config
	Content     string    `json:"content"`
	IsPreset    bool      `json:"is_preset"`
	Owner       string    `json:"owner,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TemplateVars is the context passed to Snippet text/template evaluation.
type TemplateVars struct {
	VM struct {
		Name     string
		Hostname string
		User     string
		IP       string
		Gateway  string
		SSHKey   string
	}
	Host struct {
		Hostname string
		DNS      string
	}
	Extra map[string]interface{}
}

// SnippetStore manages custom and preset Cloud-Init snippets.
type SnippetStore struct {
	mu       sync.RWMutex
	path     string
	snippets map[string]Snippet
}

// Built-in production presets.
var builtInPresets = []Snippet{
	{
		ID:          "preset-docker",
		Name:        "Docker CE & Compose Ready",
		Description: "Installs Docker Engine, Docker Compose plugin, and configures the daemon on first boot.",
		Category:    "containers",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Docker CE & Compose Deployment
package_update: true
packages:
  - apt-transport-https
  - ca-certificates
  - curl
  - gnupg
  - lsb-release

runcmd:
  - install -m 0755 -d /etc/apt/keyrings
  - curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  - chmod a+r /etc/apt/keyrings/docker.asc
  - echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
  - apt-get update
  - apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  - systemctl enable --now docker
  - usermod -aG docker {{ .VM.User }}
`,
	},
	{
		ID:          "preset-k3s",
		Name:        "K3s Lightweight Kubernetes",
		Description: "Installs single-node K3s cluster runtime with embedded containerd and kubeconfig export.",
		Category:    "kubernetes",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# K3s Lightweight Kubernetes Cluster
package_update: true
packages:
  - curl
  - ca-certificates

runcmd:
  - curl -sfL https://get.k3s.io | sh -s - --disable traefik --write-kubeconfig-mode 644
  - mkdir -p /home/{{ .VM.User }}/.kube
  - cp /etc/rancher/k3s/k3s.yaml /home/{{ .VM.User }}/.kube/config
  - chown -R {{ .VM.User }}:{{ .VM.User }} /home/{{ .VM.User }}/.kube
`,
	},
	{
		ID:          "preset-k8s",
		Name:        "Kubernetes Node (Containerd)",
		Description: "Configures kernel modules, sysctl for bridging, and installs containerd runtime.",
		Category:    "kubernetes",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Kubernetes Node Prerequisites
bootcmd:
  - swapoff -a
  - sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

write_files:
  - path: /etc/modules-load.d/k8s.conf
    content: |
      overlay
      br_netfilter
  - path: /etc/sysctl.d/k8s.conf
    content: |
      net.bridge.bridge-nf-call-iptables  = 1
      net.bridge.bridge-nf-call-ip6tables = 1
      net.ipv4.ip_forward                 = 1

package_update: true
packages:
  - containerd
  - apt-transport-https
  - curl

runcmd:
  - modprobe overlay
  - modprobe br_netfilter
  - sysctl --system
  - mkdir -p /etc/containerd && containerd config default > /etc/containerd/config.toml
  - sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
  - systemctl restart containerd
`,
	},
	{
		ID:          "preset-hardened",
		Name:        "Linux Security Hardened",
		Description: "Disables SSH password auth, sets up UFW and installs Fail2ban.",
		Category:    "security",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Linux Hardening Policy
package_update: true
packages:
  - fail2ban
  - ufw

write_files:
  - path: /etc/ssh/sshd_config.d/99-hardened.conf
    content: |
      PasswordAuthentication no
      PermitRootLogin no
      X11Forwarding no
      MaxAuthTries 3

runcmd:
  - systemctl restart ssh || systemctl restart sshd
  - ufw default deny incoming
  - ufw default allow outgoing
  - ufw allow 22/tcp
  - ufw --force enable
  - systemctl enable --now fail2ban
`,
	},
	{
		ID:          "preset-tailscale",
		Name:        "Tailscale Mesh VPN Node",
		Description: "Installs Tailscale client and enables IPv4/IPv6 packet forwarding for exit nodes.",
		Category:    "networking",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Tailscale Mesh VPN Node
write_files:
  - path: /etc/sysctl.d/99-tailscale.conf
    content: |
      net.ipv4.ip_forward = 1
      net.ipv6.conf.all.forwarding = 1

package_update: true
packages:
  - curl
  - iptables

runcmd:
  - sysctl --system
  - curl -fsSL https://tailscale.com/install.sh | sh
  - systemctl enable --now tailscaled
`,
	},
	{
		ID:          "preset-wireguard",
		Name:        "WireGuard VPN Tools",
		Description: "Installs WireGuard kernel tools and enables IP forwarding.",
		Category:    "networking",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# WireGuard VPN Core
write_files:
  - path: /etc/sysctl.d/99-wireguard.conf
    content: |
      net.ipv4.ip_forward=1
      net.ipv6.conf.all.forwarding=1

package_update: true
packages:
  - wireguard
  - wireguard-tools
  - resolvconf

runcmd:
  - sysctl --system
`,
	},
	{
		ID:          "preset-qemu-agent",
		Name:        "QEMU Guest Agent & Auto-Grow",
		Description: "Ensures QEMU guest agent is running and disk partitions auto-expand to fill disk.",
		Category:    "devops",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# QEMU Guest Agent & Partition Growth
package_update: true
packages:
  - qemu-guest-agent
  - cloud-guest-utils
  - cloud-initramfs-growpart

growpart:
  mode: auto
  devices: ['/']
  ignore_growroot_disabled: false

runcmd:
  - systemctl enable --now qemu-guest-agent
`,
	},
	{
		ID:          "preset-node-exporter",
		Name:        "Prometheus Node Exporter",
		Description: "Installs and enables Prometheus node_exporter metrics daemon on port 9100.",
		Category:    "monitoring",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Prometheus Node Exporter Daemon
package_update: true
packages:
  - prometheus-node-exporter

runcmd:
  - systemctl enable --now prometheus-node-exporter
`,
	},
	{
		ID:          "preset-beszel-agent",
		Name:        "Beszel Monitoring Agent",
		Description: "Installs Beszel Agent for lightweight system resource metrics.",
		Category:    "monitoring",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Beszel Monitoring Agent
package_update: true
packages:
  - curl
  - tar

runcmd:
  - curl -fsSL https://raw.githubusercontent.com/henrygd/beszel/main/supplemental/scripts/install-agent.sh | bash
`,
	},
	{
		ID:          "preset-cockpit",
		Name:        "Cockpit Linux Web Admin",
		Description: "Installs Cockpit web-based graphical management console on port 9090.",
		Category:    "web",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Cockpit Web Console
package_update: true
packages:
  - cockpit
  - cockpit-system

runcmd:
  - systemctl enable --now cockpit.socket
`,
	},
	{
		ID:          "preset-nginx",
		Name:        "Nginx Web Server",
		Description: "Installs Nginx HTTP/HTTPS server and enables default systemd service.",
		Category:    "web",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Nginx Web Server Deployment
package_update: true
packages:
  - nginx
  - curl

write_files:
  - path: /var/www/html/index.html
    content: |
      <!DOCTYPE html>
      <html>
      <head><title>WebKVM Node {{ .VM.Hostname }}</title></head>
      <body style="font-family:sans-serif;text-align:center;padding:50px;">
        <h1>Welcome to {{ .VM.Hostname }}</h1>
        <p>Provisioned automatically by WebKVM Cloud-Init Studio.</p>
      </body>
      </html>
    permissions: '0644'

runcmd:
  - systemctl enable --now nginx
`,
	},
	{
		ID:          "preset-dev-tools",
		Name:        "Developer Essentials",
		Description: "Git, Build-essential, Htop, Tmux, Curl, Jq, and useful CLI utilities.",
		Category:    "devops",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Developer Tools Essentials
package_update: true
packages:
  - build-essential
  - git
  - curl
  - wget
  - htop
  - tmux
  - jq
  - tree
  - zsh
`,
	},
	{
		ID:          "preset-ansible",
		Name:        "Ansible Automation Bootstrap",
		Description: "Configures Python 3 environment and passwordless sudo for automation.",
		Category:    "devops",
		Type:        "user-data",
		IsPreset:    true,
		Content: `#cloud-config
# Ansible Bootstrap
package_update: true
packages:
  - python3
  - python3-pip
  - python3-apt
  - sudo

write_files:
  - path: /etc/sudoers.d/automation
    content: "{{ .VM.User }} ALL=(ALL) NOPASSWD:ALL\n"
    permissions: "0440"
`,
	},
}

// NewSnippetStore initializes the snippet store in dataDir.
func NewSnippetStore(dataDir string) (*SnippetStore, error) {
	s := &SnippetStore{
		path:     filepath.Join(dataDir, "snippets.json"),
		snippets: make(map[string]Snippet),
	}
	// Load presets
	for _, p := range builtInPresets {
		p.CreatedAt = time.Unix(1726000000, 0).UTC()
		p.UpdatedAt = p.CreatedAt
		s.snippets[p.ID] = p
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SnippetStore) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var custom []Snippet
	if err := json.Unmarshal(data, &custom); err != nil {
		return fmt.Errorf("parse snippets.json: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sn := range custom {
		if !sn.IsPreset {
			s.snippets[sn.ID] = sn
		}
	}
	return nil
}

func (s *SnippetStore) save() error {
	var custom []Snippet
	for _, sn := range s.snippets {
		if !sn.IsPreset {
			custom = append(custom, sn)
		}
	}
	data, err := json.MarshalIndent(custom, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns all presets and custom snippets.
func (s *SnippetStore) List() []Snippet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Snippet, 0, len(s.snippets))
	for _, sn := range s.snippets {
		out = append(out, sn)
	}
	return out
}

// Get returns a snippet by ID.
func (s *SnippetStore) Get(id string) (Snippet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sn, ok := s.snippets[id]
	return sn, ok
}

// Create registers a new custom snippet.
func (s *SnippetStore) Create(sn Snippet) (Snippet, error) {
	if sn.Name == "" {
		return Snippet{}, errors.New("snippet name is required")
	}
	if sn.Content == "" {
		return Snippet{}, errors.New("snippet content is required")
	}
	if sn.Type == "" {
		sn.Type = "user-data"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if sn.ID == "" {
		sn.ID = fmt.Sprintf("snip-%d", time.Now().UnixNano())
	}
	if _, exists := s.snippets[sn.ID]; exists {
		return Snippet{}, fmt.Errorf("snippet ID %q already exists", sn.ID)
	}
	now := time.Now().UTC()
	sn.IsPreset = false
	sn.CreatedAt = now
	sn.UpdatedAt = now
	s.snippets[sn.ID] = sn
	if err := s.save(); err != nil {
		delete(s.snippets, sn.ID)
		return Snippet{}, err
	}
	return sn, nil
}

// Update mutates an existing custom snippet (presets are immutable).
func (s *SnippetStore) Update(sn Snippet) (Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.snippets[sn.ID]
	if !ok {
		return Snippet{}, errors.New("snippet not found")
	}
	if existing.IsPreset {
		return Snippet{}, errors.New("cannot edit built-in preset snippets")
	}
	if sn.Name != "" {
		existing.Name = sn.Name
	}
	if sn.Description != "" {
		existing.Description = sn.Description
	}
	if sn.Content != "" {
		existing.Content = sn.Content
	}
	if sn.Type != "" {
		existing.Type = sn.Type
	}
	if existing.Owner == "" && sn.Owner != "" {
		existing.Owner = sn.Owner
	}
	existing.UpdatedAt = time.Now().UTC()
	s.snippets[sn.ID] = existing
	if err := s.save(); err != nil {
		return Snippet{}, err
	}
	return existing, nil
}

// Delete removes a custom snippet.
func (s *SnippetStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.snippets[id]
	if !ok {
		return errors.New("snippet not found")
	}
	if existing.IsPreset {
		return errors.New("cannot delete built-in preset snippets")
	}
	delete(s.snippets, id)
	return s.save()
}

// ExpandTemplate executes text/template substitution on snippet content.
func ExpandTemplate(content string, vars TemplateVars) (string, error) {
	if content == "" {
		return "", nil
	}
	tmpl, err := template.New("cloudinit").Option("missingkey=zero").Parse(content)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("template execution error: %w", err)
	}
	return buf.String(), nil
}
