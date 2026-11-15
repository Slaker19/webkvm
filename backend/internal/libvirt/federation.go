package libvirt

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"time"

	"libvirt.org/go/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// openForWrapper returns a libvirt connection for the URI-based wrappers: the
// pooled one when a pool is registered (never closed by the caller), else a
// throwaway connection closed by release.
func openForWrapper(uri string) (*libvirt.Connect, func(), error) {
	if p := DefaultPool(); p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), poolConnectTimeout)
		defer cancel()
		c, err := p.Connector(ctx, uriNode(uri))
		if err != nil {
			return nil, nil, err
		}
		if conn := c.Get(); conn != nil {
			node, started := uriNode(uri).ID, time.Now()
			return conn, func() { p.ObserveCall(node, "wrapper", time.Since(started)) }, nil
		}
		return nil, nil, ErrNodeUnreachable
	}
	conn, err := libvirt.NewConnect(uri)
	if err != nil {
		return nil, nil, errors.New("remote libvirt connection failed")
	}
	return conn, func() { conn.Close() }, nil
}

// NewRemoteConnector opens a Connector bound to a remote libvirt URI. The
// caller owns it and must Close it when done. The Connector is configured
// with no pool-purpose store (nil cfg): lifecycle/domain operations do not
// need it, and remote storage layout is the remote node's concern.
func NewRemoteConnector(uri string) (*Connector, error) {
	c := NewConnector(uri, nil)
	if err := c.Open(); err != nil {
		return nil, errors.New("remote libvirt connection failed")
	}
	return c, nil
}

// migrateFlags computes the DomainMigrateFlags for MigrateDomain.
// Pure so it is unit-testable without a live libvirt connection.
func migrateFlags(live, sharedStorage bool) libvirt.DomainMigrateFlags {
	flags := libvirt.MIGRATE_PERSIST_DEST | libvirt.MIGRATE_UNDEFINE_SOURCE
	if live {
		flags |= libvirt.MIGRATE_LIVE | libvirt.MIGRATE_PEER2PEER
		if !sharedStorage {
			flags |= libvirt.MIGRATE_NON_SHARED_DISK
		}
	} else {
		// Libvirt offline migration moves only the domain definition; the
		// caller must guarantee the disk paths are shared by both nodes.
		flags |= libvirt.MIGRATE_OFFLINE
	}
	return flags
}

// migrateFlagsClient is migrateFlags for client-orchestrated migration: the
// caller holds both connections, so PEER2PEER (the source daemon dialing the
// destination, which needs node-to-node SSH) must not be set.
func migrateFlagsClient(live, sharedStorage bool) libvirt.DomainMigrateFlags {
	return migrateFlags(live, sharedStorage) &^ libvirt.MIGRATE_PEER2PEER
}

// MigrateDomainTo migrates the named domain from this connector to dest
// with the client driving both ends (no node-to-node SSH). Source and
// destination may be any node, including the local one.
func (c *Connector) MigrateDomainTo(dest *Connector, id string, live, sharedStorage bool) error {
	return c.MigrateDomainVia(context.Background(), dest, id, live, sharedStorage, "")
}

// MigrationURI is the data-channel URI the SOURCE node must dial to reach the
// destination, derived from the destination node's own address. Without it
// libvirt uses the destination's self-reported hostname, which a source
// node may not resolve (or resolves to a wrong host through a search
// domain); the migration then hangs on a TCP connect to the wrong address.
// Returns "" (libvirt's default) for local or non-SSH destinations.
func MigrationURI(n nodes.Node) string {
	if n.Type == nodes.NodeTypeLocal {
		return ""
	}
	_, host, _, err := nodes.NodeSSHTarget(n.URI)
	if err != nil || host == "" || nodes.IsLocalHost(host) {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() {
			return ""
		}
		if ip.To4() == nil {
			return "tcp://[" + host + "]"
		}
	}
	return "tcp://" + host
}

// MigrateDomainVia is MigrateDomainTo with an explicit migration URI (see
// MigrationURI; "" keeps libvirt's default) and a context: when ctx ends
// before the migration does (client gone, timeout) the running job is
// aborted so neither node is left holding a half-migrated domain.
func (c *Connector) MigrateDomainVia(ctx context.Context, dest *Connector, id string, live, sharedStorage bool, miguri string) error {
	if !live && !sharedStorage {
		return errors.New("offline migration requires shared storage")
	}
	if dest == nil {
		return errors.New("destination connector is required")
	}
	if err := c.ensureConnected(); err != nil {
		return err
	}
	if err := dest.ensureConnected(); err != nil {
		return err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	dconn := dest.Get()
	if dconn == nil {
		return errors.New("destination connection lost")
	}
	type result struct {
		moved *libvirt.Domain
		err   error
	}
	done := make(chan result, 1)
	go func() {
		moved, err := dom.Migrate(dconn, migrateFlagsClient(live, sharedStorage), "", miguri, 0)
		done <- result{moved, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		// AbortJob makes the blocked Migrate return; wait for it so the
		// domain handle is not freed under a running call.
		_ = dom.AbortJob()
		r = <-done
		if r.err == nil {
			r.err = ctx.Err()
		}
	}
	if r.err != nil {
		return r.err
	}
	if r.moved != nil {
		r.moved.Free()
	}
	return nil
}

// MigrateDomain migrates the named domain from this connector to a
// destination libvirt URI. live selects a live migration (the domain must
// be running and, without shared storage, the disks are block-migrated via
// MIGRATE_NON_SHARED_DISK). On success the source definition is undefined
// and the destination copy made persistent. dname is left empty to keep the
// domain name; bandwidth 0 means unlimited.
func (c *Connector) MigrateDomain(destURI, id string, live, sharedStorage bool) error {
	if !live && !sharedStorage {
		return errors.New("offline migration requires shared storage")
	}
	if p := DefaultPool(); p != nil {
		// Pool registered: client-orchestrated, no node-to-node SSH.
		ctx, cancel := context.WithTimeout(context.Background(), poolConnectTimeout)
		defer cancel()
		dest, err := p.Connector(ctx, uriNode(destURI))
		if err != nil {
			return err
		}
		return c.MigrateDomainTo(dest, id, live, sharedStorage)
	}
	if err := c.ensureConnected(); err != nil {
		return err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	return dom.MigrateToURI(destURI, migrateFlags(live, sharedStorage), "", 0)
}

// HostResources reports a node's CPU count and free/total RAM, read from the
// libvirt host API. Used by the nodes registry to surface per-node load for
// placement decisions. Memory is normalised to bytes; a failure to read free
// memory is non-fatal (returns 0 free, keeps the total from GetNodeInfo).
func HostResources(uri string) (cpus int, ramFree, ramTotal int64, err error) {
	conn, release, err := openForWrapper(uri)
	if err != nil {
		return 0, 0, 0, err
	}
	defer release()
	info, err := conn.GetNodeInfo()
	if err != nil {
		return 0, 0, 0, errors.New("remote libvirt host information unavailable")
	}
	free, _ := conn.GetFreeMemory()
	return int(info.Cpus), int64(free), int64(info.Memory) * 1024, nil
}

// TestRemoteConnection verifies that a remote libvirt URI is usable and
// returns the number of domains it exposes. It does not change node state or
// perform any operation on the domains.
func remoteSupportsKVM(capabilities string) (bool, error) {
	var caps struct {
		Guests []struct {
			Archs []struct {
				Domains []struct {
					Type string `xml:"type,attr"`
				} `xml:"domain"`
			} `xml:"arch"`
		} `xml:"guest"`
	}
	if err := xml.Unmarshal([]byte(capabilities), &caps); err != nil {
		return false, err
	}
	for _, guest := range caps.Guests {
		for _, arch := range guest.Archs {
			for _, domain := range arch.Domains {
				if domain.Type == "kvm" {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func TestRemoteConnection(uri string) (domains int, kvmAvailable bool, err error) {
	conn, release, err := openForWrapper(uri)
	if err != nil {
		return 0, false, err
	}
	defer release()
	return testConn(conn)
}

// TestRemoteConnectionOn is TestRemoteConnection over an already open
// (pooled) connector.
func TestRemoteConnectionOn(c *Connector) (domains int, kvmAvailable bool, err error) {
	conn := c.Get()
	if conn == nil {
		return 0, false, ErrNodeUnreachable
	}
	return testConn(conn)
}

// ConnectorVersion returns the hypervisor version ("major.minor.release")
// or "" when unknown.
func ConnectorVersion(c *Connector) string {
	conn := c.Get()
	if conn == nil {
		return ""
	}
	v, err := conn.GetVersion()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v/1000000, (v/1000)%1000, v%1000)
}

func testConn(conn *libvirt.Connect) (domains int, kvmAvailable bool, err error) {
	list, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
	if err != nil {
		return 0, false, errors.New("remote libvirt domain listing failed")
	}
	count := len(list)
	for i := range list {
		list[i].Free()
	}
	caps, err := conn.GetCapabilities()
	if err != nil {
		return count, false, errors.New("remote libvirt capabilities unavailable")
	}
	kvm, err := remoteSupportsKVM(caps)
	if err != nil {
		return count, false, errors.New("remote libvirt capabilities could not be parsed")
	}
	return count, kvm, nil
}

// ListRemoteDomains opens a throwaway connection to a remote libvirt URI and
// lists its domains, reusing the same domain→VM mapping as the local list.
// Used by the cluster federation layer (api.ListVMs) to aggregate VMs across
// nodes. A connect/list failure returns an error so the caller can log and
// skip that node instead of silently dropping it.
func ListRemoteDomains(uri string) ([]models.VM, error) {
	conn, release, err := openForWrapper(uri)
	if err != nil {
		return nil, err
	}
	defer release()

	doms, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
	if err != nil {
		return nil, errors.New("remote libvirt domain listing failed")
	}

	// domainToVM is a method only for historical reasons — it reads nothing
	// from the Connector, so a zero-value shim is enough to reuse it.
	shim := &Connector{conn: conn}
	vms := make([]models.VM, 0, len(doms))
	for i := range doms {
		vm, err := shim.domainToVM(&doms[i])
		doms[i].Free()
		if err != nil {
			continue
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// ListRemoteDomainsContext is ListRemoteDomains bounded by ctx. libvirt's
// connect call has no timeout of its own, so a hung remote would block the
// caller forever; the abandoned goroutine ends when libvirt gives up.
func ListRemoteDomainsContext(ctx context.Context, uri string) ([]models.VM, error) {
	type result struct {
		vms []models.VM
		err error
	}
	done := make(chan result, 1)
	go func() {
		vms, err := ListRemoteDomains(uri)
		done <- result{vms, err}
	}()
	select {
	case res := <-done:
		return res.vms, res.err
	case <-ctx.Done():
		return nil, errors.New("remote libvirt listing timed out")
	}
}

// NewRemoteConnectorContext is NewRemoteConnector bounded by ctx. A
// connector that finishes opening after the deadline is closed.
func NewRemoteConnectorContext(ctx context.Context, uri string) (*Connector, error) {
	type result struct {
		c   *Connector
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := NewRemoteConnector(uri)
		done <- result{c, err}
	}()
	select {
	case res := <-done:
		return res.c, res.err
	case <-ctx.Done():
		go func() {
			if res := <-done; res.c != nil {
				res.c.Close()
			}
		}()
		return nil, errors.New("remote libvirt connection timed out")
	}
}
