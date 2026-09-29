package libvirtbackup

import (
	"strings"
	"testing"
)

func TestBuildBackupXMLFull(t *testing.T) {
	xml := BuildBackupXML([]DiskTarget{
		{Device: "vda", File: "/tmp/backup.qcow2"},
	}, "")
	if !strings.Contains(xml, "<domainbackup mode='push'>") {
		t.Error("missing domainbackup push mode")
	}
	if strings.Contains(xml, "<incremental>") {
		t.Error("full backup should not have incremental tag")
	}
	if !strings.Contains(xml, "vda") || !strings.Contains(xml, "/tmp/backup.qcow2") {
		t.Error("missing disk target")
	}
}

func TestBuildBackupXMLIncremental(t *testing.T) {
	xml := BuildBackupXML([]DiskTarget{
		{Device: "vda", File: "/tmp/inc.qcow2"},
	}, "chk-12345")
	if !strings.Contains(xml, "<incremental>chk-12345</incremental>") {
		t.Error("missing incremental checkpoint reference")
	}
}

func TestBuildCheckpointXML(t *testing.T) {
	xml := BuildCheckpointXML("chk-999", []string{"vda", "vdb"})
	if !strings.Contains(xml, "<name>chk-999</name>") {
		t.Error("missing checkpoint name")
	}
	if !strings.Contains(xml, "checkpoint='bitmap'") {
		t.Error("missing bitmap flag")
	}
	if !strings.Contains(xml, "vda") || !strings.Contains(xml, "vdb") {
		t.Error("missing disk devices")
	}
}

func TestGenerateCheckpointName(t *testing.T) {
	n1 := GenerateCheckpointName()
	n2 := GenerateCheckpointName()
	if n1 == n2 {
		t.Error("checkpoint names should be unique")
	}
	if !strings.HasPrefix(n1, "chk-") {
		t.Errorf("name %q should start with chk-", n1)
	}
}
