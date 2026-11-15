package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"

	"webkvm/internal/compute"
	computeincus "webkvm/internal/compute/incus"
)

// GET /api/modules/ha/compat is a read-only compatibility inventory for the
// Nodes page: it lists the distro, kernel and Incus version of every cluster
// member and warns when they are mixed, which is not recommended for a
// Cluster/HA setup. It is not gated by modules.cluster_ha because operators
// need it before enabling the module, and it never answers 5xx: anything it
// cannot read is reported as missing data or a members_unknown warning.

type haCompatLocal struct {
	Hostname      string `json:"hostname"`
	OSName        string `json:"os_name"`
	OSVersion     string `json:"os_version"`
	KernelVersion string `json:"kernel_version"`
	IncusVersion  string `json:"incus_version"`
}

type haCompatMember struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Status        string `json:"status"`
	OSName        string `json:"os_name"`
	OSVersion     string `json:"os_version"`
	KernelVersion string `json:"kernel_version"`
	IncusVersion  string `json:"incus_version"`
	Error         string `json:"error"`
}

type haCompatResponse struct {
	Clustered      bool             `json:"clustered"`
	IncusAvailable bool             `json:"incus_available"`
	Local          haCompatLocal    `json:"local"`
	Members        []haCompatMember `json:"members"`
	Warnings       []string         `json:"warnings"`
	Recommended    bool             `json:"recommended"`
}

const (
	haCompatMixedDistro        = "mixed_distro"
	haCompatMixedDistroVersion = "mixed_distro_version"
	haCompatMixedIncusVersion  = "mixed_incus_version"
	haCompatMixedKernelMajor   = "mixed_kernel_major"
	haCompatMembersUnknown     = "members_unknown"

	// haCompatMemberUnreachable is the only member error exposed to clients.
	haCompatMemberUnreachable = "unreachable"
)

// haCompatEnvSource is the part of the Incus backend the handler needs.
type haCompatEnvSource interface {
	IsClustered() bool
	ClusterMemberEnvironments(ctx context.Context) ([]computeincus.MemberEnvironment, error)
}

// Package vars so tests can stub the host probes and the Incus connection.
var (
	haCompatOSReleasePath = "/etc/os-release"
	haCompatHostname      = os.Hostname
	haCompatKernel        = readKernel
	haCompatOpenIncus     = openHACompatIncus
)

// openHACompatIncus reuses the running Incus backend when there is one and
// otherwise opens a short-lived connection to the local socket.
func openHACompatIncus(h *Handler) (haCompatEnvSource, func(), error) {
	if combined, ok := h.compute.(*compute.Combined); ok {
		if src, ok := combined.Secondary().(haCompatEnvSource); ok && src != nil {
			return src, func() {}, nil
		}
	}
	backend, err := computeincus.NewIncusBackend("")
	if err != nil {
		return nil, func() {}, err
	}
	return backend, backend.Close, nil
}

func (h *Handler) GetHACompat(w http.ResponseWriter, r *http.Request) {
	resp := haCompatResponse{
		Local:    readHACompatLocal(),
		Members:  []haCompatMember{},
		Warnings: []string{},
		// Nothing is recommended until at least two readable members agree.
		Recommended: false,
	}
	if h.cfg == nil || !h.cfg.IncusEnabled {
		jsonResp(w, http.StatusOK, resp)
		return
	}
	src, closeFn, err := haCompatOpenIncus(h)
	if err != nil || src == nil {
		closeFn()
		jsonResp(w, http.StatusOK, resp)
		return
	}
	defer closeFn()
	resp.IncusAvailable = true
	resp.Clustered = src.IsClustered()

	envs, err := src.ClusterMemberEnvironments(r.Context())
	if err != nil {
		slog.Warn("ha_compat_members_failed", "err", err)
		resp.Warnings = []string{haCompatMembersUnknown}
		jsonResp(w, http.StatusOK, resp)
		return
	}
	for _, env := range envs {
		memberErr := ""
		if env.Error != "" {
			// The raw client error can carry endpoints or TLS details: log
			// it and expose only a stable code.
			slog.Warn("ha_compat_member_error", "member", env.Name, "err", env.Error)
			memberErr = haCompatMemberUnreachable
		}
		resp.Members = append(resp.Members, haCompatMember{
			Name:          env.Name,
			URL:           env.URL,
			Status:        env.Status,
			OSName:        env.OSName,
			OSVersion:     env.OSVersion,
			KernelVersion: env.KernelVersion,
			IncusVersion:  env.IncusVersion,
			Error:         memberErr,
		})
	}
	resp.Local.IncusVersion = localIncusVersion(resp.Local.Hostname, resp.Clustered, resp.Members)
	resp.Warnings, resp.Recommended = haCompatWarnings(resp.Members)
	jsonResp(w, http.StatusOK, resp)
}

// localIncusVersion picks the local daemon's version from the member list:
// the member named like this host, or the single entry of a standalone
// daemon.
func localIncusVersion(hostname string, clustered bool, members []haCompatMember) string {
	for _, m := range members {
		if m.Name != "" && m.Name == hostname {
			return m.IncusVersion
		}
	}
	if !clustered && len(members) == 1 {
		return members[0].IncusVersion
	}
	return ""
}

// readHACompatLocal uses the same os-release keys Incus reports (NAME and
// VERSION_ID) so local and member values are comparable.
func readHACompatLocal() haCompatLocal {
	local := haCompatLocal{KernelVersion: haCompatKernel()}
	if host, err := haCompatHostname(); err == nil {
		local.Hostname = host
	}
	if f, err := os.Open(haCompatOSReleasePath); err == nil {
		release := parseOSRelease(f)
		_ = f.Close()
		local.OSName = release["NAME"]
		local.OSVersion = release["VERSION_ID"]
	}
	return local
}

// parseOSRelease reads KEY=value lines from an os-release file, stripping
// optional single or double quotes. Comments and malformed lines are
// skipped.
func parseOSRelease(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		out[strings.TrimSpace(key)] = value
	}
	return out
}

// haCompatWarnings compares the members that answered and returns sorted,
// de-duplicated warnings plus whether the mix is recommended: at least two
// readable members, none unreadable and no mixed_* warning. Members with an
// error or no OS name raise members_unknown (which also clears the
// recommendation, as the comparison is incomplete) and are left out of the
// comparisons.
func haCompatWarnings(members []haCompatMember) ([]string, bool) {
	set := map[string]bool{}
	distros := map[string]bool{}
	versionsByDistro := map[string]map[string]bool{}
	incusVersions := map[string]bool{}
	kernels := map[string]bool{}
	readable := 0
	for _, m := range members {
		osName := strings.ToLower(strings.TrimSpace(m.OSName))
		if m.Error != "" || osName == "" {
			set[haCompatMembersUnknown] = true
			continue
		}
		readable++
		distros[osName] = true
		if versionsByDistro[osName] == nil {
			versionsByDistro[osName] = map[string]bool{}
		}
		if v := strings.TrimSpace(m.OSVersion); v != "" {
			versionsByDistro[osName][v] = true
		}
		if v := strings.TrimSpace(m.IncusVersion); v != "" {
			incusVersions[v] = true
		}
		if v := kernelMajorMinor(m.KernelVersion); v != "" {
			kernels[v] = true
		}
	}
	if len(distros) > 1 {
		set[haCompatMixedDistro] = true
	}
	for _, versions := range versionsByDistro {
		if len(versions) > 1 {
			set[haCompatMixedDistroVersion] = true
		}
	}
	if len(incusVersions) > 1 {
		set[haCompatMixedIncusVersion] = true
	}
	if len(kernels) > 1 {
		set[haCompatMixedKernelMajor] = true
	}
	warnings := make([]string, 0, len(set))
	// A single readable member (or none) has nothing to be compatible with.
	recommended := len(distros) > 0 && readable >= 2
	for w := range set {
		warnings = append(warnings, w)
		if strings.HasPrefix(w, "mixed_") || w == haCompatMembersUnknown {
			recommended = false
		}
	}
	sort.Strings(warnings)
	return warnings, recommended
}

// kernelMajorMinor returns "6.8" for "6.8.0-40-generic".
func kernelMajorMinor(release string) string {
	release = strings.TrimSpace(release)
	if i := strings.IndexAny(release, "-+ _"); i >= 0 {
		release = release[:i]
	}
	parts := strings.SplitN(release, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}
