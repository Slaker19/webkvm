package vzdump_test

import (
	"archive/tar"
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"webkvm/internal/vzdump"
)

func vzdumpWithConf(t *testing.T, conf string) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeEntry(t, tw, "./etc/vzdump/pct.conf", conf)
	writeEntry(t, tw, "./rootfs/", "")
	writeEntry(t, tw, "./rootfs/etc/hostname", "x\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return writeTmpFile(t, buf.Bytes())
}

// pct.conf values reach a YAML document Incus interprets, so hostile values
// must be rejected or neutralised, never spliced into the YAML (audit #3).
func TestImportFromProxmox_RejectsHostileConf(t *testing.T) {
	bad := map[string]string{
		"control char in name": "hostname: a\rb\ncores: 1\nmemory: 512\narch: amd64\nostype: debian\n",
		"yaml in cores":        "hostname: ct\ncores: 1\"\nmemory: 512\narch: amd64\nostype: debian\n",
		"yaml in memory":       "hostname: ct\ncores: 1\nmemory: 512MB\", security.privileged: \"true\narch: amd64\nostype: debian\n",
		"bad arch":             "hostname: ct\ncores: 1\nmemory: 512\narch: amd64\\nprofiles: [evil]\nostype: debian\n",
		"bad ostype":           "hostname: ct\ncores: 1\nmemory: 512\narch: amd64\nostype: a b\n",
	}
	for name, conf := range bad {
		src := vzdumpWithConf(t, conf)
		dest := filepath.Join(t.TempDir(), "out.tar")
		if err := vzdump.ImportFromProxmox(src, dest, "", ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestImportFromProxmox_NameIsNeutralisedAndYAMLStructureFixed(t *testing.T) {
	src := vzdumpWithConf(t, "hostname: web: {privileged}\ncores: 2\nmemory: 1024\narch: amd64\nostype: debian\n")
	dest := filepath.Join(t.TempDir(), "out.tar")
	if err := vzdump.ImportFromProxmox(src, dest, "", ""); err != nil {
		t.Fatal(err)
	}
	backup := yamlFromArchive(t, dest, "backup/container/backup.yaml")
	var doc struct {
		Container struct {
			Name     string            `yaml:"name"`
			Config   map[string]string `yaml:"config"`
			Profiles []string          `yaml:"profiles"`
			Devices  map[string]any    `yaml:"devices"`
		} `yaml:"container"`
	}
	if err := yaml.Unmarshal([]byte(backup), &doc); err != nil {
		t.Fatalf("backup.yaml is not valid YAML: %v\n%s", err, backup)
	}
	if strings.ContainsAny(doc.Container.Name, " :{}") {
		t.Errorf("container name not neutralised: %q", doc.Container.Name)
	}
	if len(doc.Container.Config) != 2 || len(doc.Container.Profiles) != 1 || doc.Container.Profiles[0] != "default" || len(doc.Container.Devices) != 1 {
		t.Errorf("unexpected structure: %+v", doc.Container)
	}
	if strings.Contains(backup, "security.") {
		t.Errorf("unexpected security key in generated YAML:\n%s", backup)
	}
}

func TestReadLimitedViaOversizedPctConf(t *testing.T) {
	conf := "hostname: ct\n" + strings.Repeat("# padding\n", 200000) // > 1 MiB
	src := vzdumpWithConf(t, conf)
	if err := vzdump.ImportFromProxmox(src, filepath.Join(t.TempDir(), "o.tar"), "", ""); err == nil {
		t.Fatal("oversized pct.conf accepted")
	}
}
