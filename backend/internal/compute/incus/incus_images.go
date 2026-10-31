package incus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// ListIncusProfiles returns the profile names available on the Incus
// backend (empty for KVM-only hosts, where the API returns []).
func (b *IncusBackend) ListIncusProfiles() ([]string, error) {
	return b.client.GetProfileNames()
}

// defaultIncusCatalog returns the curated official Incus/LXC container image templates.
func defaultIncusCatalog() []models.IncusImageItem {
	return []models.IncusImageItem{
		// Populares / Servidor
		{
			Ref:         "images:ubuntu/resolute",
			Label:       "Ubuntu 26.04 (Resolute)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "Ubuntu 26.04 Resolute LTS (desarrollo / compilación más reciente).",
			Arch:        "x86_64 / arm64",
			Badge:       "26.04",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			// NOTE: intentionally "images:ubuntu/24.04", NOT the legacy
			// "ubuntu:24.04" (cloud-images.ubuntu.com) remote. That
			// stream only ships old-style "lxd.tar.xz" image metadata,
			// which the vendored Incus client (v7.4.0) never recognizes
			// as a valid container image — every pull against it fails
			// with "Alias ... doesn't exist" regardless of the alias
			// requested. images.linuxcontainers.org mirrors the same
			// Ubuntu releases in the modern format and actually works.
			Ref:         "images:ubuntu/24.04",
			Label:       "Ubuntu 24.04 LTS (Noble)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "LTS más reciente de Ubuntu con soporte oficial hasta 2029.",
			Arch:        "x86_64 / arm64",
			Badge:       "LTS",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:ubuntu/22.04",
			Label:       "Ubuntu 22.04 LTS (Jammy)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "Versión LTS clásica y ampliamente compatible para producción.",
			Arch:        "x86_64 / arm64",
			Badge:       "LTS",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:debian/forky",
			Label:       "Debian 14 (Forky)",
			Category:    "dev",
			Distro:      "debian",
			Description: "Siguiente generación de Debian 14 con paquetería de última hornada.",
			Arch:        "x86_64 / arm64",
			Badge:       "Debian 14",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:debian/13",
			Label:       "Debian 13 (Trixie)",
			Category:    "popular",
			Distro:      "debian",
			Description: "Siguiente generación de Debian con paquetería moderna.",
			Arch:        "x86_64 / arm64",
			Badge:       "Testing",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:debian/12",
			Label:       "Debian 12 (Bookworm)",
			Category:    "popular",
			Distro:      "debian",
			Description: "Debian estable, ideal para servidores ligeros y bases de datos.",
			Arch:        "x86_64 / arm64",
			Badge:       "Estable",
			RecVCPUs:    2,
			RecRAMMB:    1024,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:debian/11",
			Label:       "Debian 11 (Bullseye)",
			Category:    "popular",
			Distro:      "debian",
			Description: "Debian oldstable optimizada para servicios heredados.",
			Arch:        "x86_64 / arm64",
			Badge:       "Oldstable",
			RecVCPUs:    1,
			RecRAMMB:    1024,
			RecDiskGB:   10,
		},
		{
			Ref:         "images:debian/sid",
			Label:       "Debian Sid (Unstable)",
			Category:    "dev",
			Distro:      "debian",
			Description: "Rama inestable de Debian para desarrolladores y bleeding-edge.",
			Arch:        "x86_64 / arm64",
			Badge:       "Unstable",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		// Ligeras / Minimalistas
		{
			Ref:         "images:alpine/3.24",
			Label:       "Alpine Linux 3.24",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Alpine Linux versión más reciente.",
			Arch:        "x86_64 / arm64",
			Badge:       "3.24",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.23",
			Label:       "Alpine Linux 3.23",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Alpine Linux versión reciente estable.",
			Arch:        "x86_64 / arm64",
			Badge:       "3.23",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.22",
			Label:       "Alpine Linux 3.22",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Alpine Linux versión estable.",
			Arch:        "x86_64 / arm64",
			Badge:       "3.22",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.21",
			Label:       "Alpine Linux 3.21",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Ultraligera basada en musl y BusyBox (~5MB). Máximo rendimiento y bajo consumo.",
			Arch:        "x86_64 / arm64",
			Badge:       "5 MB",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.20",
			Label:       "Alpine Linux 3.20",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Versión estable consolidada de Alpine Linux.",
			Arch:        "x86_64 / arm64",
			Badge:       "5 MB",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/edge",
			Label:       "Alpine Linux Edge",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Rama rolling de vanguardia para pruebas de paquetes y bleeding-edge.",
			Arch:        "x86_64 / arm64",
			Badge:       "Edge",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:voidlinux",
			Label:       "Void Linux (glibc)",
			Category:    "minimal",
			Distro:      "void",
			Description: "Distribución independiente ligera con gestor XBPS y runit.",
			Arch:        "x86_64",
			Badge:       "Lightweight",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   10,
		},
		{
			Ref:         "images:voidlinux/musl",
			Label:       "Void Linux (musl)",
			Category:    "minimal",
			Distro:      "void",
			Description: "Variante ultra compacta de Void Linux compilada contra musl libc.",
			Arch:        "x86_64",
			Badge:       "musl",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   10,
		},
		{
			Ref:         "images:openwrt/25.12",
			Label:       "OpenWrt 25.12",
			Category:    "minimal",
			Distro:      "openwrt",
			Description: "Sistema operativo embebido ultraligero para redes y servicios.",
			Arch:        "x86_64 / arm64",
			Badge:       "Router",
			RecVCPUs:    1,
			RecRAMMB:    256,
			RecDiskGB:   2,
		},
		{
			Ref:         "images:openwrt/23.05",
			Label:       "OpenWrt 23.05",
			Category:    "minimal",
			Distro:      "openwrt",
			Description: "Sistema operativo embebido ultraligero ideal para enrutamiento y micro-servicios.",
			Arch:        "x86_64 / arm64",
			Badge:       "Router",
			RecVCPUs:    1,
			RecRAMMB:    256,
			RecDiskGB:   2,
		},
		{
			Ref:         "images:devuan/daedalus",
			Label:       "Devuan 5 (Daedalus)",
			Category:    "minimal",
			Distro:      "devuan",
			Description: "Bifurcación de Debian sin systemd (usa SysVinit / OpenRC / runit).",
			Arch:        "x86_64 / arm64",
			Badge:       "No-systemd",
			RecVCPUs:    1,
			RecRAMMB:    1024,
			RecDiskGB:   10,
		},

		// Enterprise / RHEL
		{
			Ref:         "images:rockylinux/10",
			Label:       "Rocky Linux 10",
			Category:    "enterprise",
			Distro:      "rocky",
			Description: "Siguiente generación compatible con Red Hat Enterprise Linux 10.",
			Arch:        "x86_64 / arm64",
			Badge:       "RHEL 10",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:rockylinux/9",
			Label:       "Rocky Linux 9 (images:)",
			Category:    "enterprise",
			Distro:      "rocky",
			Description: "100% compatible binario con Red Hat Enterprise Linux 9 desde remote images:.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:rockylinux/8",
			Label:       "Rocky Linux 8",
			Category:    "enterprise",
			Distro:      "rocky",
			Description: "Versión empresarial con compatibilidad RHEL 8.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:almalinux/9",
			Label:       "AlmaLinux 9 (images:)",
			Category:    "enterprise",
			Distro:      "almalinux",
			Description: "Enterprise Linux comunitaria compatible 1:1 con RHEL 9.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:almalinux/8",
			Label:       "AlmaLinux 8",
			Category:    "enterprise",
			Distro:      "almalinux",
			Description: "Enterprise Linux comunitaria compatible 1:1 con RHEL 8.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:centos/10-Stream",
			Label:       "CentOS Stream 10",
			Category:    "enterprise",
			Distro:      "centos",
			Description: "Rama upstream más reciente de desarrollo para RHEL 10.",
			Arch:        "x86_64 / arm64",
			Badge:       "Stream 10",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:centos/9-Stream",
			Label:       "CentOS Stream 9",
			Category:    "enterprise",
			Distro:      "centos",
			Description: "Rama de desarrollo upstream de Red Hat Enterprise Linux 9.",
			Arch:        "x86_64 / arm64",
			Badge:       "Upstream",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:oracle/10",
			Label:       "Oracle Linux 10",
			Category:    "enterprise",
			Distro:      "oracle",
			Description: "Siguiente generación Enterprise optimizada para bases de datos.",
			Arch:        "x86_64 / arm64",
			Badge:       "Oracle 10",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:oracle/9",
			Label:       "Oracle Linux 9",
			Category:    "enterprise",
			Distro:      "oracle",
			Description: "Distribución Enterprise optimizada para bases de datos y cargas pesadas.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:amazonlinux/2023",
			Label:       "Amazon Linux 2023",
			Category:    "enterprise",
			Distro:      "amazon",
			Description: "Distribución orientada a la nube de AWS optimizada para contenedores.",
			Arch:        "x86_64 / arm64",
			Badge:       "Cloud",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:opensuse/16.0",
			Label:       "openSUSE 16.0",
			Category:    "enterprise",
			Distro:      "opensuse",
			Description: "Siguiente generación de openSUSE Leap.",
			Arch:        "x86_64 / arm64",
			Badge:       "Leap 16",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:opensuse/15.6",
			Label:       "openSUSE Leap 15.6",
			Category:    "enterprise",
			Distro:      "opensuse",
			Description: "Distribución estable orientada a servidores empresariales con YaST.",
			Arch:        "x86_64 / arm64",
			Badge:       "Leap",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},

		// Desarrollo / Rolling / Seguridad
		{
			Ref:         "images:archlinux",
			Label:       "Arch Linux",
			Category:    "dev",
			Distro:      "arch",
			Description: "Distribución Rolling Release siempre al día con pacman.",
			Arch:        "x86_64",
			Badge:       "Rolling",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:fedora/44",
			Label:       "Fedora 44 (Rawhide/Next)",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Siguiente generación de Fedora con las tecnologías más punteras.",
			Arch:        "x86_64 / arm64",
			Badge:       "Fedora 44",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:fedora/43",
			Label:       "Fedora 43",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Versión de vanguardia de Fedora con paquetería actualizada.",
			Arch:        "x86_64 / arm64",
			Badge:       "Fedora 43",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:fedora/41",
			Label:       "Fedora 41",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Última versión estable de Fedora con las últimas novedades de GNU/Linux.",
			Arch:        "x86_64 / arm64",
			Badge:       "Latest",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:fedora/40",
			Label:       "Fedora 40",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Versión probada y estable de Fedora para desarrollo.",
			Arch:        "x86_64 / arm64",
			Badge:       "Stable",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:kali",
			Label:       "Kali Linux",
			Category:    "dev",
			Distro:      "kali",
			Description: "Distribución especializada en auditoría de seguridad y pentesting.",
			Arch:        "x86_64 / arm64",
			Badge:       "SecTools",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:nixos/26.05",
			Label:       "NixOS 26.05",
			Category:    "dev",
			Distro:      "nixos",
			Description: "Distribución declarativa y reproducible basada en el gestor de paquetes Nix.",
			Arch:        "x86_64 / arm64",
			Badge:       "Declarative",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:opensuse/tumbleweed",
			Label:       "openSUSE Tumbleweed",
			Category:    "dev",
			Distro:      "opensuse",
			Description: "Rolling release con pruebas automáticas openQA de openSUSE.",
			Arch:        "x86_64 / arm64",
			Badge:       "Rolling",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:gentoo",
			Label:       "Gentoo Linux",
			Category:    "dev",
			Distro:      "gentoo",
			Description: "Distribución source-based meta altamente optimizable con Portage.",
			Arch:        "x86_64 / arm64",
			Badge:       "Source",
			RecVCPUs:    4,
			RecRAMMB:    4096,
			RecDiskGB:   30,
		},
	}
}

// simplestreamsIndex represents the root structure of Simplestreams index.json
type simplestreamsIndex struct {
	Index map[string]struct {
		Products []string `json:"products"`
	} `json:"index"`
}

type simplestreamsCache struct {
	sync.RWMutex
	lastFetched time.Time
	items       []models.IncusImageItem
}

var ssCache simplestreamsCache

// fetchDynamicSimplestreamsImages queries https://images.linuxcontainers.org/streams/v1/index.json
// dynamically with in-memory caching (6h TTL) and a fast 3-second timeout.
// If offline or if an error occurs, it falls back seamlessly to defaultIncusCatalog().
func fetchDynamicSimplestreamsImages() []models.IncusImageItem {
	ssCache.RLock()
	if time.Since(ssCache.lastFetched) < 6*time.Hour && len(ssCache.items) > 0 {
		cached := make([]models.IncusImageItem, len(ssCache.items))
		copy(cached, ssCache.items)
		ssCache.RUnlock()
		return cached
	}
	ssCache.RUnlock()

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://images.linuxcontainers.org/streams/v1/index.json", nil)
	if err != nil {
		return defaultIncusCatalog()
	}
	req.Header.Set("User-Agent", "WebKVM/2.4")

	resp, err := client.Do(req)
	if err != nil {
		return defaultIncusCatalog()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return defaultIncusCatalog()
	}

	var idx simplestreamsIndex
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return defaultIncusCatalog()
	}

	imgNode, ok := idx.Index["images"]
	if !ok || len(imgNode.Products) == 0 {
		return defaultIncusCatalog()
	}

	items := parseSimplestreamsProducts(imgNode.Products)
	if len(items) == 0 {
		return defaultIncusCatalog()
	}

	ssCache.Lock()
	ssCache.lastFetched = time.Now()
	ssCache.items = items
	ssCache.Unlock()

	res := make([]models.IncusImageItem, len(items))
	copy(res, items)
	return res
}

func parseSimplestreamsProducts(products []string) []models.IncusImageItem {
	targetArch := "amd64"
	if runtime.GOARCH == "arm64" {
		targetArch = "arm64"
	}

	type key struct {
		distro  string
		release string
	}
	// best tracks, per (distro, release), both the winning variant AND
	// the release string's ORIGINAL casing from the product line. Ref
	// generation below must reuse that exact casing — simplestreams
	// aliases are case-sensitive (e.g. "centos/10-Stream", capital S),
	// so lower-casing it when building "images:%s/%s" produced a Ref
	// that resolves on nothing, failing every pull for that image with
	// "Alias ... doesn't exist" even though the image is real.
	type best struct {
		variant     string
		origDistro  string
		origRelease string
	}

	variantScore := func(v string) int {
		switch v {
		case "default", "default+ufs":
			return 100
		case "cloud", "cloud+ufs":
			return 90
		case "tinycloud":
			return 80
		case "openrc", "systemd":
			return 70
		default:
			return 10
		}
	}

	bestVariants := make(map[key]best)
	for _, p := range products {
		parts := strings.Split(p, ":")
		if len(parts) < 4 {
			continue
		}
		distro, release, arch, variant := parts[0], parts[1], parts[2], parts[3]
		if arch != targetArch {
			continue
		}
		k := key{distro: strings.ToLower(distro), release: strings.ToLower(release)}
		curr, exists := bestVariants[k]
		if !exists || variantScore(variant) > variantScore(curr.variant) {
			bestVariants[k] = best{variant: variant, origDistro: distro, origRelease: release}
		}
	}

	archLabel := "x86_64 / arm64"
	var out []models.IncusImageItem

	// NOTE: deliberately NOT prepending hardcoded "ubuntu:24.04"/
	// "ubuntu:22.04" entries here (the legacy cloud-images.ubuntu.com
	// remote). That stream's metadata format isn't recognized by the
	// vendored Incus client, so every pull against it fails with
	// "Alias ... doesn't exist" no matter which alias is requested.
	// "images:ubuntu/noble" and "images:ubuntu/jammy" below — sourced
	// from the SAME live index this function already parses — cover the
	// exact same releases and actually work.

	// Sort keys for deterministic output
	keys := make([]key, 0, len(bestVariants))
	for k := range bestVariants {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].distro != keys[j].distro {
			return keys[i].distro < keys[j].distro
		}
		return keys[i].release > keys[j].release
	})

	for _, k := range keys {
		// d/r stay lower-cased for the switch matching below; the Ref
		// sent to Incus must use the ORIGINAL casing the image server
		// advertised (see the "best" struct comment above).
		d := k.distro
		r := k.release
		orig := bestVariants[k]

		item := models.IncusImageItem{
			Ref:       fmt.Sprintf("images:%s/%s", orig.origDistro, orig.origRelease),
			Arch:      archLabel,
			RecVCPUs:  2,
			RecRAMMB:  2048,
			RecDiskGB: 20,
		}

		switch d {
		case "ubuntu":
			item.Category = "popular"
			item.Distro = "ubuntu"
			switch r {
			case "resolute":
				item.Label = "Ubuntu 26.04 (Resolute)"
				item.Badge = "26.04"
				item.Description = "Ubuntu 26.04 Resolute LTS (desarrollo / compilación más reciente)."
			case "noble":
				item.Label = "Ubuntu 24.04 LTS (Noble)"
				item.Badge = "LTS"
				item.Description = "LTS más reciente de Ubuntu con soporte oficial hasta 2029."
			case "jammy":
				item.Label = "Ubuntu 22.04 LTS (Jammy)"
				item.Badge = "LTS"
				item.Description = "Versión LTS clásica y ampliamente compatible para producción."
			case "oracular":
				item.Label = "Ubuntu 24.10 (Oracular)"
				item.Badge = "Interim"
				item.Category = "dev"
				item.Description = "Versión de ciclo corto con los paquetes y kernels más recientes."
			default:
				item.Label = fmt.Sprintf("Ubuntu %s", titleCase(r))
				item.Badge = "Ubuntu"
				item.Description = fmt.Sprintf("Plantilla Ubuntu %s desde images:.", r)
			}

		case "debian":
			item.Distro = "debian"
			item.RecRAMMB = 1024
			item.RecDiskGB = 15
			switch r {
			case "forky":
				item.Label = "Debian 14 (Forky)"
				item.Category = "dev"
				item.Badge = "Debian 14"
				item.Description = "Siguiente generación de Debian 14 con paquetería de última hornada."
			case "trixie":
				item.Label = "Debian 13 (Trixie)"
				item.Category = "popular"
				item.Badge = "Testing"
				item.Description = "Siguiente generación de Debian con paquetería moderna."
			case "bookworm":
				item.Label = "Debian 12 (Bookworm)"
				item.Category = "popular"
				item.Badge = "Estable"
				item.Description = "Debian estable, ideal para servidores ligeros y bases de datos."
			case "bullseye":
				item.Label = "Debian 11 (Bullseye)"
				item.Category = "popular"
				item.Badge = "Oldstable"
				item.Description = "Debian oldstable optimizada para servicios heredados."
			case "sid":
				item.Label = "Debian Sid (Unstable)"
				item.Category = "dev"
				item.Badge = "Unstable"
				item.Description = "Rama inestable de Debian para desarrolladores y bleeding-edge."
			default:
				item.Label = fmt.Sprintf("Debian %s", titleCase(r))
				item.Category = "popular"
				item.Badge = "Debian"
				item.Description = fmt.Sprintf("Plantilla Debian %s.", r)
			}

		case "alpine":
			item.Distro = "alpine"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("Alpine Linux %s", r)
			item.Badge = r
			if r == "edge" {
				item.Badge = "Rolling"
			}
			item.Description = fmt.Sprintf("Alpine Linux distribución ultraligera y segura basada en musl y BusyBox (v%s).", r)
			item.RecVCPUs = 1
			item.RecRAMMB = 512
			item.RecDiskGB = 5

		case "fedora":
			item.Distro = "fedora"
			item.Label = fmt.Sprintf("Fedora %s", r)
			item.Description = fmt.Sprintf("Distribución Fedora %s orientada a desarrollo y servidores modernos.", r)
			switch r {
			case "44":
				item.Category = "dev"
				item.Badge = "Fedora 44"
				item.Label = "Fedora 44 (Rawhide/Next)"
			case "43":
				item.Category = "dev"
				item.Badge = "Fedora 43"
			case "41":
				item.Category = "popular"
				item.Badge = "Latest"
			case "40":
				item.Category = "popular"
				item.Badge = "Stable"
			default:
				item.Category = "dev"
				item.Badge = r
			}

		case "rockylinux":
			item.Distro = "rocky"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("Rocky Linux %s", r)
			item.Badge = fmt.Sprintf("RHEL %s", r)
			item.Description = fmt.Sprintf("100%% compatible binario con Red Hat Enterprise Linux %s.", r)

		case "almalinux":
			item.Distro = "almalinux"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("AlmaLinux %s", r)
			item.Badge = fmt.Sprintf("Enterprise %s", r)
			item.Description = fmt.Sprintf("Distribución empresarial con soporte comunitario y compatibilidad binaria RHEL %s.", r)

		case "archlinux":
			item.Distro = "arch"
			item.Category = "dev"
			item.Ref = "images:archlinux"
			item.Label = "Arch Linux"
			item.Badge = "Rolling"
			item.Description = "Distribución Rolling Release siempre al día con pacman."
			item.RecDiskGB = 15

		case "kali":
			item.Distro = "kali"
			item.Category = "dev"
			item.Ref = "images:kali"
			item.Label = "Kali Linux"
			item.Badge = "SecTools"
			item.Description = "Distribución especializada en auditoría de seguridad y pentesting."

		case "nixos":
			item.Distro = "nixos"
			item.Category = "dev"
			item.Label = fmt.Sprintf("NixOS %s", r)
			item.Badge = "Declarative"
			item.Description = "Distribución declarativa y reproducible basada en el gestor de paquetes Nix."

		case "opensuse":
			item.Distro = "opensuse"
			if r == "tumbleweed" {
				item.Category = "dev"
				item.Label = "openSUSE Tumbleweed"
				item.Badge = "Rolling"
				item.Description = "Rolling release con pruebas automáticas openQA de openSUSE."
			} else {
				item.Category = "enterprise"
				item.Label = fmt.Sprintf("openSUSE Leap %s", r)
				item.Badge = "Leap"
				item.Description = "Distribución estable orientada a servidores empresariales con YaST."
			}

		case "openwrt":
			item.Distro = "openwrt"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("OpenWrt %s", r)
			item.Badge = "Router"
			item.Description = "Sistema operativo embebido ultraligero ideal para enrutamiento y micro-servicios."
			item.RecVCPUs = 1
			item.RecRAMMB = 256
			item.RecDiskGB = 2

		case "voidlinux":
			item.Distro = "voidlinux"
			item.Category = "minimal"
			item.Ref = "images:voidlinux"
			item.Label = "Void Linux"
			item.Badge = "runit"
			item.Description = "Distribución independiente minimalista con gestor de paquetes xbps e init runit."
			item.RecVCPUs = 1
			item.RecRAMMB = 512
			item.RecDiskGB = 10

		case "busybox":
			item.Distro = "busybox"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("BusyBox %s", r)
			item.Badge = "Tiny"
			item.Description = "Entorno mínimo autocontenido con utilidades estándar UNIX en un único binario."
			item.RecVCPUs = 1
			item.RecRAMMB = 128
			item.RecDiskGB = 1

		case "gentoo":
			item.Distro = "gentoo"
			item.Category = "dev"
			item.Ref = "images:gentoo"
			item.Label = "Gentoo Linux"
			item.Badge = "Source"
			item.Description = "Distribución source-based meta altamente optimizable con Portage."
			item.RecVCPUs = 4
			item.RecRAMMB = 4096
			item.RecDiskGB = 30

		case "oracle":
			item.Distro = "oracle"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("Oracle Linux %s", r)
			item.Badge = "UEK"
			item.Description = fmt.Sprintf("Oracle Linux %s con Unbreakable Enterprise Kernel.", r)

		case "centos":
			// r is the lower-cased release key (e.g. "10-stream"); strip
			// the redundant "-stream" suffix so the label doesn't read
			// "CentOS Stream 10-stream".
			ver := strings.TrimSuffix(r, "-stream")
			item.Distro = "centos"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("CentOS Stream %s", ver)
			item.Badge = "Stream"
			item.Description = fmt.Sprintf("Rama previa y de desarrollo continuo para RHEL %s.", ver)

		case "devuan":
			item.Distro = "devuan"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("Devuan %s", titleCase(r))
			item.Badge = "No-systemd"
			item.Description = "Bifurcación de Debian sin systemd (SysVinit / OpenRC)."
			item.RecVCPUs = 1
			item.RecRAMMB = 1024
			item.RecDiskGB = 10

		case "mint":
			item.Distro = "mint"
			item.Category = "popular"
			item.Label = fmt.Sprintf("Linux Mint %s", titleCase(r))
			item.Badge = "Mint"
			item.Description = fmt.Sprintf("Distribución de escritorio/servidor Linux Mint %s.", titleCase(r))

		case "freebsd":
			item.Distro = "freebsd"
			item.Category = "community"
			item.Label = fmt.Sprintf("FreeBSD %s", r)
			item.Badge = "FreeBSD"
			item.Description = fmt.Sprintf("Sistema operativo Unix avanzado FreeBSD %s.", r)

		case "netbsd":
			item.Distro = "netbsd"
			item.Category = "community"
			item.Label = fmt.Sprintf("NetBSD %s", r)
			item.Badge = "NetBSD"
			item.Description = fmt.Sprintf("Sistema operativo tipo Unix altamente portable NetBSD %s.", r)

		default:
			item.Distro = d
			item.Category = "community"
			item.Label = fmt.Sprintf("%s %s", titleCase(d), r)
			item.Badge = "Community"
			item.Description = fmt.Sprintf("Plantilla %s %s desde el catálogo oficial images:.", titleCase(d), r)
		}

		out = append(out, item)
	}

	return out
}

// titleCaser replaces strings.Title (deprecated, SA1019) with the
// officially recommended x/text implementation for image labels.
var titleCaser = cases.Title(language.English)

func titleCase(s string) string { return titleCaser.String(s) }

// formatLocalImageLabel derives a clean, human-readable name from local image metadata.
func formatLocalImageLabel(img api.Image, shortFP string) string {
	osName := strings.ToLower(img.Properties["os"])
	release := strings.ToLower(img.Properties["release"])

	switch osName {
	case "ubuntu":
		switch release {
		case "resolute":
			return "Ubuntu 26.04 LTS (Resolute)"
		case "oracular":
			return "Ubuntu 24.10 (Oracular)"
		case "noble":
			return "Ubuntu 24.04 LTS (Noble)"
		case "jammy":
			return "Ubuntu 22.04 LTS (Jammy)"
		case "focal":
			return "Ubuntu 20.04 LTS (Focal)"
		default:
			if release != "" {
				return "Ubuntu " + titleCase(release)
			}
		}
	case "fedora":
		if release != "" {
			return "Fedora " + release
		}
	case "debian":
		switch release {
		case "forky":
			return "Debian 14 (Forky)"
		case "trixie":
			return "Debian 13 (Trixie)"
		case "bookworm":
			return "Debian 12 (Bookworm)"
		case "bullseye":
			return "Debian 11 (Bullseye)"
		default:
			if release != "" {
				return "Debian " + titleCase(release)
			}
		}
	case "alpine":
		if release != "" {
			return "Alpine Linux " + release
		}
	}

	if len(img.Aliases) > 0 && img.Aliases[0].Name != "" {
		return img.Aliases[0].Name
	}
	if osName != "" {
		if release != "" {
			return fmt.Sprintf("%s %s (%s)", titleCase(osName), release, shortFP)
		}
		return fmt.Sprintf("%s (%s)", titleCase(osName), shortFP)
	}
	return "Local Image (" + shortFP + ")"
}

// resolveRemoteAlias looks up alias on remote, falling back to a
// case-insensitive scan of every alias the server advertises when the
// exact name doesn't match. Upstream image servers are not consistent
// about casing (e.g. "centos/10-Stream" with a capital S), and any
// component that builds a ref programmatically — our own dynamic catalog
// included — can get that one detail wrong; this keeps a single typo in
// casing from turning into a hard "doesn't exist" failure.
func resolveRemoteAlias(remote incus.ImageServer, alias string) (*api.ImageAliasesEntry, error) {
	if entry, _, err := remote.GetImageAlias(alias); err == nil {
		return entry, nil
	}
	names, err := remote.GetImageAliasNames()
	if err != nil {
		return nil, fmt.Errorf("alias %q doesn't exist", alias)
	}
	lower := strings.ToLower(alias)
	for _, name := range names {
		if strings.ToLower(name) != lower {
			continue
		}
		entry, _, err := remote.GetImageAlias(name)
		if err != nil {
			return nil, err
		}
		return entry, nil
	}
	return nil, fmt.Errorf("alias %q doesn't exist", alias)
}

// needsCloudInit reports whether this create actually depends on
// cloud-init running inside the guest. Hostname alone does not: Incus
// sets that itself. Anything else in the seed — users, passwords, SSH
// keys, a provisioning script, custom user-data — is delivered ONLY by
// cloud-init, so an image without it produces a container that silently
// lacks everything the operator asked for.
func needsCloudInit(ci *models.CloudInitRequest) bool {
	if ci == nil {
		return false
	}
	return ci.User != "" || ci.Password != "" || ci.SSHKey != "" ||
		ci.ProvisionScript != "" || ci.CustomUserData != "" || ci.SnippetID != ""
}

// cloudVariantAlias upgrades an image alias to its "/cloud" variant, the
// linuxcontainers build that ships cloud-init preinstalled.
//
// The upgrade is verified against the image server before it is applied,
// and the original alias is returned untouched when the variant does not
// exist (openSUSE and Fedora publish no /cloud container at the time of
// writing) or the server cannot be reached. Failing to find a better
// image is not a reason to fail the create: the caller keeps the exact
// image it asked for, which is no worse than before this check existed.
func cloudVariantAlias(server, alias string) string {
	if alias == "" || strings.Contains(alias, "/cloud") {
		return alias
	}
	candidate := alias + "/cloud"
	remote, err := incus.ConnectSimpleStreams(server, &incus.ConnectionArgs{})
	if err != nil {
		slog.Warn("incus: cannot reach image server to select a cloud-init image",
			"server", server, "alias", alias, "error", err)
		return alias
	}
	if _, err := resolveRemoteAlias(remote, candidate); err != nil {
		slog.Warn("incus: no cloud-init variant for this image; cloud-init will not run",
			"alias", alias, "tried", candidate)
		return alias
	}
	return candidate
}

// PullIncusImage downloads ref (e.g. "images:alpine/3.20") straight from
// its simplestreams image server into the local Incus image store. This
// talks only to the (remote, read-only) ImageServer and the local daemon's
// image API — no instance is ever created, so unlike CreateDomain it needs
// no network/bridge at all and works on hosts with zero configured
// networks.
func (b *IncusBackend) PullIncusImage(ref string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	// A fingerprint names an image that is ALREADY in the local store —
	// that is the only reason we know the fingerprint at all. There is
	// nothing to download, so this is a no-op rather than an error.
	if isImageFingerprint(ref) {
		if _, _, err := b.client.GetImage(strings.ToLower(strings.TrimSpace(ref))); err == nil {
			return nil
		}
		return fmt.Errorf("no local image with fingerprint %q", ref)
	}
	server, alias, err := parseImageRef(ref)
	if err != nil {
		return err
	}
	// Idempotent: if the alias is already cached locally (a previous pull,
	// or the image warmed via some other path), there is nothing to do.
	// Without this check, re-pulling an already-cached image fails with
	// "Alias already exists" even though the desired end state (the
	// image sitting in the local store) is already true.
	if _, _, err := b.client.GetImageAlias(alias); err == nil {
		return nil
	}
	remote, err := incus.ConnectSimpleStreams(server, &incus.ConnectionArgs{})
	if err != nil {
		return fmt.Errorf("connect to image server %s: %w", server, err)
	}
	aliasEntry, err := resolveRemoteAlias(remote, alias)
	if err != nil {
		return fmt.Errorf("resolve image alias %q on %s: %w", alias, server, err)
	}
	image, _, err := remote.GetImage(aliasEntry.Target)
	if err != nil {
		return fmt.Errorf("fetch image metadata: %w", err)
	}
	// The fingerprint (not just the alias) may already be cached under a
	// different alias — CopyAliases below would then also collide.
	if _, _, err := b.client.GetImage(image.Fingerprint); err == nil {
		return b.client.CreateImageAlias(api.ImageAliasesPost{
			ImageAliasesEntry: api.ImageAliasesEntry{
				Name:                 alias,
				ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: image.Fingerprint},
			},
		})
	}
	// CopyAliases pulls every alias the source image server already
	// advertises for this fingerprint (e.g. "24.04", "noble", "n" for a
	// single Ubuntu image) — the requested alias is always among them.
	// Passing it AGAIN via the Aliases field made the client submit the
	// same alias twice in one CreateImage call, which the daemon rejects
	// with "Alias already exists" on every single pull, clean or not.
	op, err := b.client.CopyImage(remote, *image, &incus.ImageCopyArgs{
		Type:        aliasEntry.Type,
		CopyAliases: true,
		AutoUpdate:  true,
	})
	if err != nil {
		return fmt.Errorf("start image copy: %w", err)
	}
	return op.Wait()
}

// ListIncusImages returns available official and locally cached Incus images.
func (b *IncusBackend) ListIncusImages() ([]models.IncusImageItem, error) {
	catalog := fetchDynamicSimplestreamsImages()
	if b.client == nil {
		return catalog, nil
	}

	// Query cached images from Incus daemon
	localImages, err := b.client.GetImages()
	if err != nil {
		// Return catalog even if querying local daemon fails
		return catalog, nil
	}

	return mergeLocalImages(catalog, localImages), nil
}

// mergeLocalImages folds the host's cached Incus images into the remote
// catalog: entries whose ref matches a local alias are marked as cached,
// and any local image with no catalog counterpart is appended as its own
// entry.
//
// It is deliberately a pure function. Every interesting failure in here
// needs a specific set of local images AND a specific catalog, which a
// live Incus daemon will not reliably provide on demand — see
// merge_local_images_test.go for the shapes that used to produce a list
// whose entries could not be told apart.
func mergeLocalImages(catalog []models.IncusImageItem, localImages []api.Image) []models.IncusImageItem {
	// Index local images by alias, release, and fingerprint
	localByAlias := make(map[string]api.Image)
	localByFingerprint := make(map[string]api.Image)
	for _, img := range localImages {
		fp := img.Fingerprint
		if len(fp) > 12 {
			localByFingerprint[fp[:12]] = img
		}
		localByFingerprint[fp] = img
		for _, a := range img.Aliases {
			localByAlias[strings.ToLower(a.Name)] = img
		}
		if rel := strings.ToLower(img.Properties["release"]); rel != "" {
			osName := strings.ToLower(img.Properties["os"])
			localByAlias[osName+"/"+rel] = img
			localByAlias["images:"+osName+"/"+rel] = img
		}
	}

	// Match and mark local images in catalog
	matchedFPs := make(map[string]bool)
	for i := range catalog {
		ref := catalog[i].Ref
		server, alias, err := parseImageRef(ref)
		if err == nil {
			aliasLower := strings.ToLower(alias)
			if img, ok := localByAlias[aliasLower]; ok {
				catalog[i].IsLocal = true
				catalog[i].Fingerprint = img.Fingerprint[:12]
				catalog[i].Size = img.Size
				catalog[i].Badge = "Local"
				matchedFPs[img.Fingerprint] = true
				continue
			}
			refLower := strings.ToLower(ref)
			if img, ok := localByAlias[refLower]; ok {
				catalog[i].IsLocal = true
				catalog[i].Fingerprint = img.Fingerprint[:12]
				catalog[i].Size = img.Size
				catalog[i].Badge = "Local"
				matchedFPs[img.Fingerprint] = true
				continue
			}
		}
		_ = server
	}

	// Add any unmatched local images as custom local entries.
	//
	// Two local images can share an (os, release) pair — two builds of
	// the same release, pulled at different times — and need not carry
	// aliases at all. localByAlias keeps only the last of them, so the
	// others land here. The ref derived from (os, release) is then
	// IDENTICAL to the catalog entry that was matched to their sibling,
	// and the API would return two different images claiming the same
	// ref. That is not cosmetic: the UI keys its list on ref, so Svelte
	// throws each_key_duplicate and aborts the whole render — the Images
	// page comes up showing zero cached images, zero ISOs and zero base
	// disks even though every one of those API calls returned data.
	//
	// seenRefs keeps the list's identity unique by construction, so the
	// invariant does not depend on how many collisions the host happens
	// to have today.
	seenRefs := make(map[string]bool, len(catalog))
	for i := range catalog {
		if catalog[i].Ref != "" {
			seenRefs[strings.ToLower(catalog[i].Ref)] = true
		}
	}
	for _, img := range localImages {
		if matchedFPs[img.Fingerprint] {
			continue
		}
		primaryAlias := ""
		if len(img.Aliases) > 0 {
			primaryAlias = img.Aliases[0].Name
		}
		shortFP := img.Fingerprint
		if len(shortFP) > 12 {
			shortFP = shortFP[:12]
		}
		label := formatLocalImageLabel(img, shortFP)
		desc := img.Properties["description"]
		if desc == "" {
			desc = fmt.Sprintf("Imagen local (%s) creada en %s.", shortFP, img.CreatedAt.Format("2006-01-02"))
		}
		distro := strings.ToLower(img.Properties["os"])
		if distro == "" {
			distro = "linux"
		}
		ref := shortFP
		if primaryAlias != "" {
			ref = primaryAlias
		} else if img.Properties["os"] != "" && img.Properties["release"] != "" {
			ref = fmt.Sprintf("images:%s/%s", strings.ToLower(img.Properties["os"]), strings.ToLower(img.Properties["release"]))
		}
		// A ref already taken by a catalog entry would make this image
		// indistinguishable from it. Fall back to the fingerprint, which
		// is what an image with no os/release already gets and is
		// guaranteed unique.
		if seenRefs[strings.ToLower(ref)] {
			ref = shortFP
		}
		seenRefs[strings.ToLower(ref)] = true
		catalog = append([]models.IncusImageItem{
			{
				Ref:         ref,
				Label:       label,
				Category:    "local",
				Distro:      distro,
				Description: desc,
				Arch:        img.Architecture,
				Badge:       "Local",
				IsLocal:     true,
				Fingerprint: shortFP,
				Size:        img.Size,
				RecVCPUs:    2,
				RecRAMMB:    2048,
				RecDiskGB:   15,
			},
		}, catalog...)
	}

	return catalog
}

