package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// validNetworkNameRE matches bridge-safe network names. The name becomes
// a REAL Linux bridge (IFNAMSIZ caps interface names at 15 chars), so
// only a strict allowlist is acceptable here: the libvirt layer keeps
// its own denylist as backstop, but shell metacharacters (;, $, `, ...)
// must never get this far.
var validNetworkNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func validateNetworkName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !validNetworkNameRE.MatchString(name) {
		return fmt.Errorf("invalid network name: must be 1-15 characters, alphanumeric, dots, hyphens, or underscores (it becomes a Linux bridge)")
	}
	// Dots-only names pass the charset but are path traversal payloads;
	// reserved/virtual prefixes collide with libvirt/docker namespaces.
	// The libvirt layer re-checks all of this as backstop.
	if name == "." || name == ".." {
		return fmt.Errorf("invalid network name %q", name)
	}
	for _, p := range []string{"virbr", "lxdbr", "lxcbr", "incusbr", "docker", "br-"} {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("invalid network name %q: reserved prefix %q", name, p)
		}
	}
	switch name {
	case "lo", "default", "webkvm-bridge", "br0-bridge":
		return fmt.Errorf("invalid network name %q: reserved", name)
	}
	return nil
}

// validateNetworkCIDR accepts an empty CIDR (direct networks derive it)
// and otherwise requires a parseable CIDR block.
func validateNetworkCIDR(cidr string) error {
	if cidr == "" {
		return nil
	}
	if ip, _, err := net.ParseCIDR(cidr); err != nil || ip == nil {
		return fmt.Errorf("invalid CIDR %q", cidr)
	}
	return nil
}

func (h *Handler) ListNetworks(w http.ResponseWriter, r *http.Request) {
	nets, err := h.compute.ListNetworks()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Scope the list to the caller's AllowedNetworks, mirroring ListPools'
	// AllowedPools scoping exactly. Admins and unrestricted users see
	// everything.
	if user, role, _ := audit.FromRequest(r); role != models.RoleAdmin && user != "" {
		if u, uerr := h.userStore.Get(user); uerr == nil {
			if set, all := networkAllowSet(u); !all {
				filtered := nets[:0]
				for _, n := range nets {
					if set[n.Name] {
						filtered = append(filtered, n)
					}
				}
				nets = filtered
			}
		}
	}
	jsonResp(w, http.StatusOK, nets)
}

func (h *Handler) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	var req models.CreateNetworkRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateNetworkName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Fail fast with 400 on malformed CIDR instead of a 500 from the
	// libvirt layer after partial work.
	if err := validateNetworkCIDR(req.CIDR); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// VLanAware left unspecified falls back to the operator's configured
	// default (Settings -> Network) instead of silently always false.
	if req.VLanAware == nil && h.settings != nil {
		def := h.settings.GetBool("network.vlan_aware_default")
		req.VLanAware = &def
	}
	net, err := h.compute.CreateNetwork(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.create", req.Name, nil))
	jsonResp(w, http.StatusCreated, net)
}

func (h *Handler) UpdateNetwork(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "id")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	var req models.UpdateNetworkRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	net, err := h.compute.UpdateNetwork(name, req)
	if err != nil {
		if errors.Is(err, compute.ErrNetworkNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.update", name, nil))
	jsonResp(w, http.StatusOK, net)
}

func (h *Handler) DeleteNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Refuse early (before the libvirt round-trip) so the response
	// is a clean 403, not a 500 with a libvirt error. The libvirt
	// layer also enforces this as defense in depth — see
	// libvirt.IsManagedNetwork and the check inside
	// Connector.DeleteNetwork.
	if compute.IsManagedNetwork(id) {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf("network %q is managed by webVM and cannot be deleted via the API; remove the underlying Linux bridge manually (or re-run setup-bridge.sh) if you really want it gone", id))
		return
	}
	if err := h.compute.DeleteNetwork(id); err != nil {
		// A bridge still carrying a live VM/container tap is an expected,
		// caller-actionable guard (detach the interface first), not a
		// server failure — matches the 409-for-state-conflicts pattern
		// vmActionErr already uses for VM lifecycle ops.
		if errors.Is(err, compute.ErrNetworkInUse) {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, compute.ErrNetworkNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.delete", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) checkNetworkAccess(r *http.Request, network string) error {
	user, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin || user == "" {
		return nil
	}
	u, err := h.userStore.Get(user)
	if err != nil {
		return err
	}
	return assertNetworkAllowed(u, network)
}

func (h *Handler) StartNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.checkNetworkAccess(r, id); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	net, err := h.compute.StartNetwork(id)
	if err != nil {
		if errors.Is(err, compute.ErrNetworkNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.start", id, nil))
	jsonResp(w, http.StatusOK, net)
}

func (h *Handler) StopNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.checkNetworkAccess(r, id); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	net, err := h.compute.StopNetwork(id)
	if err != nil {
		if errors.Is(err, compute.ErrNetworkNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.stop", id, nil))
	jsonResp(w, http.StatusOK, net)
}

func (h *Handler) ListNetworkLeases(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.checkNetworkAccess(r, id); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	leases, err := h.compute.NetworkLeases(id)
	if err != nil {
		if errors.Is(err, compute.ErrNetworkNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, leases)
}

func (h *Handler) ReleaseNetworkLease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.checkNetworkAccess(r, id); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	// chi keeps the raw (still percent-encoded) path segment whenever the
	// request's RawPath differs from Path — true here since a MAC's ':'
	// gets escaped to %3A by the client but doesn't need re-escaping in a
	// path segment, so URLParam alone would hand back "%3A" literally.
	// Same fix already applied to the equivalent VM interface route
	// (vms.go's DetachNetworkIface).
	mac, err := url.PathUnescape(chi.URLParam(r, "mac"))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid mac parameter")
		return
	}
	ip := r.URL.Query().Get("ip")
	if ip == "" {
		jsonErr(w, http.StatusBadRequest, "ip query parameter is required")
		return
	}
	if err := h.compute.ReleaseNetworkLease(id, ip, mac); err != nil {
		// dhcp_release itself can't distinguish "released" from "sent a
		// packet for an address nobody had leased" (it exits 0 either
		// way), so the libvirt layer checks the leasefile first — a
		// clean 404 here, not a 200 masking a no-op.
		if errors.Is(err, compute.ErrLeaseNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, compute.ErrNetworkNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.lease.release", id, map[string]any{"ip": ip, "mac": mac}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "released"})
}
