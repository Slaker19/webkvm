package incus

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
)

// Compile-time proof that the Incus adapter offers the optional cluster
// extension.
var _ compute.ClusterBackend = (*IncusBackend)(nil)

const memberStatusOnline = "Online"

// clusterOpTimeout bounds a single move/evacuate/restore operation.
const clusterOpTimeout = 30 * time.Minute

var evacuateModeRe = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)

// waitOperationCtx waits for op honouring ctx. On cancellation the remote
// operation is cancelled best-effort and ctx.Err() is returned.
func waitOperationCtx(ctx context.Context, op incus.Operation, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cur := op.Get()
		switch cur.Status {
		case "Success":
			if cur.Err != "" {
				return errors.New(cur.Err)
			}
			return nil
		case "Failure", "Cancelled":
			if cur.Err != "" {
				return errors.New(cur.Err)
			}
			return errors.New("incus operation failed")
		}
		if time.Now().After(deadline) {
			return errors.New("incus operation timed out")
		}
		select {
		case <-ctx.Done():
			_ = op.Cancel()
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if err := op.Refresh(); err != nil {
			return err
		}
	}
}

// clusterMemberList returns the raw member list, or ErrNotClustered.
func (b *IncusBackend) clusterMemberList() ([]api.ClusterMember, error) {
	if b == nil || b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	if !b.client.IsClustered() {
		return nil, compute.ErrNotClustered
	}
	lister, ok := b.client.(clusterMembersLister)
	if !ok {
		return nil, errors.New("incus client does not expose cluster members")
	}
	return lister.GetClusterMembers()
}

// findMember returns the named member or ErrMemberNotFound.
func (b *IncusBackend) findMember(name string) (api.ClusterMember, error) {
	name = strings.TrimSpace(name)
	members, err := b.clusterMemberList()
	if err != nil {
		return api.ClusterMember{}, err
	}
	for _, m := range members {
		if m.ServerName == name && name != "" {
			return m, nil
		}
	}
	return api.ClusterMember{}, fmt.Errorf("%w: %q", compute.ErrMemberNotFound, name)
}

// onlineMember is findMember plus an Online check (ErrMemberOffline).
func (b *IncusBackend) onlineMember(name string) (api.ClusterMember, error) {
	m, err := b.findMember(name)
	if err != nil {
		return m, err
	}
	if m.Status != memberStatusOnline {
		return m, fmt.Errorf("%w: %q is %s", compute.ErrMemberOffline, m.ServerName, m.Status)
	}
	return m, nil
}

// memberOnlineMap maps member name -> online. Nil when the daemon is not
// clustered or the list cannot be read (the flag is then simply omitted).
func (b *IncusBackend) memberOnlineMap() map[string]bool {
	if b == nil || b.client == nil || !b.client.IsClustered() {
		return nil
	}
	members, err := b.clusterMemberList()
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(members))
	for _, m := range members {
		out[m.ServerName] = m.Status == memberStatusOnline
	}
	return out
}

// InstanceLocation returns the cluster member hosting instance id.
func (b *IncusBackend) InstanceLocation(id string) (string, error) {
	if b == nil || b.client == nil {
		return "", compute.ErrNotImplemented
	}
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return "", mapLXErr(err)
	}
	return inst.Location, nil
}

// MoveInstance moves an instance to another cluster member. Incus performs
// a cluster-internal move when the migration request is sent to the daemon
// with ?target=<member> (UseTarget), the same call `incus move --target`
// issues.
func (b *IncusBackend) MoveInstance(ctx context.Context, id string, targetMember string, live bool) error {
	if b == nil || b.client == nil {
		return compute.ErrNotImplemented
	}
	if _, err := b.onlineMember(targetMember); err != nil {
		return err
	}
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return mapLXErr(err)
	}
	if inst.Location == targetMember {
		return fmt.Errorf("%w: %q", compute.ErrSameMember, targetMember)
	}
	if !strings.EqualFold(inst.Status, "Stopped") && !live {
		return compute.ErrNeedsStop
	}
	op, err := b.client.UseTarget(targetMember).MigrateInstance(id, api.InstancePost{
		Name:      id,
		Migration: true,
		Live:      live,
	})
	if err != nil {
		return mapLXErr(err)
	}
	return waitOperationCtx(ctx, op, clusterOpTimeout)
}

// EvacuateMember evacuates a cluster member. An empty mode keeps the
// cluster-configured default.
func (b *IncusBackend) EvacuateMember(ctx context.Context, name string, mode string) error {
	mode = strings.TrimSpace(mode)
	if mode != "" && !evacuateModeRe.MatchString(mode) {
		return fmt.Errorf("invalid evacuation mode %q", mode)
	}
	return b.memberState(ctx, name, api.ClusterMemberStatePost{Action: "evacuate", Mode: mode})
}

// RestoreMember restores a previously evacuated member.
func (b *IncusBackend) RestoreMember(ctx context.Context, name string) error {
	return b.memberState(ctx, name, api.ClusterMemberStatePost{Action: "restore"})
}

func (b *IncusBackend) memberState(ctx context.Context, name string, post api.ClusterMemberStatePost) error {
	m, err := b.findMember(name)
	if err != nil {
		return err
	}
	op, err := b.client.UpdateClusterMemberState(m.ServerName, post)
	if err != nil {
		return err
	}
	return waitOperationCtx(ctx, op, clusterOpTimeout)
}
