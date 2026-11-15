package compute

import (
	"context"
	"testing"

	"webkvm/internal/models"
)

// clusterFake is a secondary (Incus-like) backend with snapshots and the
// optional cluster extension.
type clusterFake struct {
	*fakeBackend
	snapCalls []string
	moved     string
}

func (c *clusterFake) ListSnapshots(id string) ([]models.Snapshot, error) {
	c.snapCalls = append(c.snapCalls, "list:"+id)
	return []models.Snapshot{{ID: "s1", Name: "s1"}}, nil
}
func (c *clusterFake) CreateSnapshot(id string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	c.snapCalls = append(c.snapCalls, "create:"+id)
	return models.Snapshot{ID: req.Name, Name: req.Name}, nil
}
func (c *clusterFake) DeleteSnapshot(id, snap string) (int64, error) {
	c.snapCalls = append(c.snapCalls, "delete:"+id)
	return 7, nil
}
func (c *clusterFake) RevertSnapshot(id, snap string) error {
	c.snapCalls = append(c.snapCalls, "revert:"+id)
	return nil
}
func (c *clusterFake) ClusterMembers() ([]MemberInfo, error) {
	return []MemberInfo{{Name: "a", Status: "Online"}}, nil
}
func (c *clusterFake) MoveInstance(_ context.Context, id, target string, _ bool) error {
	c.moved = id + "->" + target
	return nil
}
func (c *clusterFake) EvacuateMember(context.Context, string, string) error { return nil }
func (c *clusterFake) RestoreMember(context.Context, string) error          { return nil }
func (c *clusterFake) InstanceLocation(string) (string, error)              { return "a", nil }

func TestCombinedRoutesSnapshotsToIncusSecondary(t *testing.T) {
	kvm := &fakeBackend{vms: []models.VM{{ID: "vm1", Name: "vm1"}}}
	sec := &clusterFake{fakeBackend: &fakeBackend{vms: []models.VM{{ID: "ct1", Name: "ct1", Hypervisor: "incus"}}}}
	c := NewCombined(kvm, sec)

	if list, err := c.ListSnapshots("ct1"); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := c.CreateSnapshot("ct1", models.CreateSnapshotRequest{Name: "s2"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n, err := c.DeleteSnapshot("ct1", "s1"); err != nil || n != 7 {
		t.Fatalf("delete: %d %v", n, err)
	}
	if err := c.RevertSnapshot("ct1", "s1"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if len(sec.snapCalls) != 4 {
		t.Fatalf("snapshot calls did not reach the secondary: %v", sec.snapCalls)
	}
}

func TestCombinedCluster(t *testing.T) {
	sec := &clusterFake{fakeBackend: &fakeBackend{}}
	cb, ok := NewCombined(&fakeBackend{}, sec).Cluster()
	if !ok || cb == nil {
		t.Fatal("expected the secondary's ClusterBackend")
	}
	if err := cb.MoveInstance(context.Background(), "ct1", "b", false); err != nil || sec.moved != "ct1->b" {
		t.Fatalf("move not delegated: %q %v", sec.moved, err)
	}
	if _, ok := NewCombined(&fakeBackend{}, nil).Cluster(); ok {
		t.Error("KVM-only must not expose a ClusterBackend")
	}
	if _, ok := NewCombined(&fakeBackend{}, &fakeBackend{}).Cluster(); ok {
		t.Error("a non-cluster secondary must not expose a ClusterBackend")
	}
}
