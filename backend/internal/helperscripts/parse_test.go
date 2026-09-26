package helperscripts

import "testing"

// jellyfinLauncher is the common shape: every value declared literally
// at column zero.
const jellyfinLauncher = `#!/usr/bin/env bash
source <(curl -fsSL https://example/build.func)
# Copyright (c) 2021-2026 community-scripts ORG
# Source: https://jellyfin.org/

APP="Jellyfin"
var_tags="${var_tags:-media}"
var_cpu="${var_cpu:-2}"
var_ram="${var_ram:-2048}"
var_disk="${var_disk:-16}"
var_os="${var_os:-ubuntu}"
var_version="${var_version:-24.04}"
var_unprivileged="${var_unprivileged:-1}"
var_gpu="${var_gpu:-yes}"

header_info "$APP"
start
build_container
description

echo -e "${INFO}${YW}Access it using the following URL:${CL}"
echo -e "${GATEWAY}${BGN}http://${IP}:8096${CL}"
`

func TestParse_TypicalLauncher(t *testing.T) {
	s, ok := Parse("jellyfin", jellyfinLauncher)
	if !ok {
		t.Fatal("a launcher calling build_container must parse")
	}
	if s.Name != "Jellyfin" {
		t.Errorf("Name = %q, want Jellyfin", s.Name)
	}
	if s.VCPUs != 2 || s.RAMMB != 2048 || s.DiskGB != 16 {
		t.Errorf("resources = %d/%d/%d, want 2/2048/16", s.VCPUs, s.RAMMB, s.DiskGB)
	}
	if s.OS != "ubuntu" || s.Version != "24.04" {
		t.Errorf("base = %s/%s, want ubuntu/24.04", s.OS, s.Version)
	}
	if s.Port != 8096 {
		t.Errorf("Port = %d, want 8096", s.Port)
	}
	if !s.NeedsGPU {
		t.Error("var_gpu=yes must set NeedsGPU: it needs device passthrough")
	}
	if !s.Unprivileged {
		t.Error("var_unprivileged=1 must set Unprivileged")
	}
	if s.Website != "https://jellyfin.org/" {
		t.Errorf("Website = %q", s.Website)
	}
	if len(s.Tags) != 1 || s.Tags[0] != "media" {
		t.Errorf("Tags = %v, want [media]", s.Tags)
	}
}

// adguardLauncher reproduces the awkward real case: an interactive OS
// menu, with the sizes declared *indented* inside the resulting if/else.
// A regex anchored at column zero silently returned zero RAM and disk
// for every app shaped like this.
const adguardLauncher = `#!/usr/bin/env bash
# Source: https://adguard.com/

APP="Adguard"
var_tags="${var_tags:-adblock}"
var_cpu="${var_cpu:-1}"
if [[ -z "${var_os:-}" ]] && command -v pveversion >/dev/null 2>&1; then
  var_os=$(msg_menu "Choose the container OS" \
    "debian" "Debian 13" \
    "alpine" "Alpine (smaller footprint)")
fi

if [[ "${var_os:-}" == "alpine" ]]; then
  var_ram="${var_ram:-256}"
  var_disk="${var_disk:-1}"
  var_version="${var_version:-3.24}"
else
  var_ram="${var_ram:-512}"
  var_disk="${var_disk:-2}"
  var_version="${var_version:-13}"
fi

start
build_container
echo -e "${GATEWAY}${BGN}http://${IP}:3000${CL}"
`

func TestParse_IndentedValuesAndOSMenu(t *testing.T) {
	s, ok := Parse("adguard", adguardLauncher)
	if !ok {
		t.Fatal("expected a parseable launcher")
	}
	if s.RAMMB == 0 || s.DiskGB == 0 {
		t.Errorf("indented declarations must be read, got ram=%d disk=%d", s.RAMMB, s.DiskGB)
	}
	// var_os comes from a command substitution, so neither it nor the
	// per-branch version can be trusted as the default.
	if s.OS != "" {
		t.Errorf("OS = %q, want empty: $(msg_menu ...) is shell, not a distro", s.OS)
	}
	if s.Version != "" {
		t.Errorf("Version = %q, want empty when the OS is undetermined", s.Version)
	}
	if s.Port != 3000 {
		t.Errorf("Port = %d, want 3000", s.Port)
	}
}

// Upstream leaves withdrawn apps in the tree as stubs that print an
// error and exit. Importing one would offer the user an app that cannot
// possibly install.
func TestParse_RejectsRetiredAndNonInstallable(t *testing.T) {
	retired := `#!/usr/bin/env bash
APP="BookLore"
header_info "$APP"
msg_error "This script is no longer available in community-scripts."
exit 1
`
	if _, ok := Parse("booklore", retired); ok {
		t.Error("a retired stub must not be imported")
	}

	noBuild := `#!/usr/bin/env bash
APP="Something"
echo hello
`
	if _, ok := Parse("something", noBuild); ok {
		t.Error("without build_container it is not an installable app")
	}

	if _, ok := Parse("nameless", "#!/bin/bash\nbuild_container\n"); ok {
		t.Error("an entry with no APP= has no display name; reject it")
	}
}

// "http://${IP}" with no port means plain HTTP. Treating that as "no
// port" would hide the web UI of apps that have one.
func TestParse_ImplicitPortAndWebPath(t *testing.T) {
	base := `#!/usr/bin/env bash
APP="BookStack"
var_cpu="${var_cpu:-1}"
build_container
`
	s, _ := Parse("bookstack", base+"echo -e \"${GATEWAY}${BGN}http://${IP}${CL}\"\n")
	if s.Port != 80 {
		t.Errorf("Port = %d, want 80 for a bare http://${IP}", s.Port)
	}

	s, _ = Parse("pihole", base+"echo -e \"${GATEWAY}${BGN}http://${IP}:3010/admin${CL}\"\n")
	if s.Port != 3010 || s.WebPath != "/admin" {
		t.Errorf("got port=%d path=%q, want 3010 and /admin", s.Port, s.WebPath)
	}

	// A base OS with no web service must stay at zero rather than
	// inventing a port.
	s, _ = Parse("almalinux", base)
	if s.Port != 0 {
		t.Errorf("Port = %d, want 0 when no URL is printed", s.Port)
	}
}

func TestIsPlainValue(t *testing.T) {
	for _, v := range []string{"debian", "13", "24.04", "alpine", "3.24"} {
		if !isPlainValue(v) {
			t.Errorf("isPlainValue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"$(msg_menu", "`cmd`", "a b", "${x}", ""} {
		if isPlainValue(v) {
			t.Errorf("isPlainValue(%q) = true, want false", v)
		}
	}
}

func TestSplitTags(t *testing.T) {
	got := splitTags("monitoring;visualization; ")
	if len(got) != 2 || got[0] != "monitoring" || got[1] != "visualization" {
		t.Errorf("splitTags = %v", got)
	}
	if splitTags("") != nil {
		t.Error("empty tags must yield nil, not a one-element empty slice")
	}
}
