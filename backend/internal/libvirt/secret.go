package libvirt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SecretRef identifies a persisted libvirt secret. The on-disk
// mapping only stores the UUID libvirt assigned to the secret —
// the actual password lives inside libvirt (as the secret's
// value) and never touches our filesystem.
type SecretRef struct {
	PoolName   string `json:"pool_name"`
	SecretUUID string `json:"secret_uuid"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

const cifsSecretsFileMode os.FileMode = 0600

var (
	cifsSecretsMu sync.RWMutex
	cifsSecrets   = map[string]SecretRef{}
)

func cifsSecretsPath(dataDir string) string {
	return filepath.Join(dataDir, "cifs-secrets.json")
}

// defineCIFSSecret is the SINGLE source of truth for creating a
// libvirt secret with CIFS credentials. All other code MUST call
// this rather than calling SecretDefineXML directly. The flow is:
//
//  1. Idempotent: undefine any pre-existing secret for this pool.
//  2. Define the secret in libvirt (ephemeral XML, no value yet).
//  3. Set the secret value (in-memory in libvirt).
//  4. Persist the poolName -> secretUUID mapping to disk.
//  5. Update the in-memory cache.
//
// On any failure after step 2, the libvirt secret is undefined
// to avoid leaving orphans behind.
func defineCIFSSecret(ctx context.Context, c *Connector,
	poolName, usage, user, pass string) (*SecretRef, error) {

	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	conn := c.Get()
	if conn == nil {
		return nil, errors.New("libvirt not connected")
	}

	// Step 1: idempotent — clear any previous mapping for this pool.
	_ = unsetCIFSSecret(ctx, c, poolName)

	// Step 2: define the secret in libvirt.
	secret, err := conn.SecretDefineXML(buildCIFSSecretXML(usage, user), 0)
	if err != nil {
		return nil, fmt.Errorf("cifs: libvirt define: %w", err)
	}
	defer secret.Free()

	// Step 3: set the value in libvirt's memory.
	if err := secret.SetValue([]byte(pass), 0); err != nil {
		_ = secret.Undefine()
		return nil, fmt.Errorf("cifs: libvirt set value: %w", err)
	}

	uuid, err := secret.GetUUIDString()
	if err != nil {
		_ = secret.Undefine()
		return nil, fmt.Errorf("cifs: get uuid: %w", err)
	}

	// Step 4: persist the poolName -> secretUUID mapping.
	ref := &SecretRef{
		PoolName:   poolName,
		SecretUUID: uuid,
		CreatedAt:  time.Now().Unix(),
	}
	if c.cfg == nil {
		_ = secret.Undefine()
		return nil, errors.New("cifs: connector has no config; cannot resolve dataDir")
	}
	if err := persistCIFSSecretRef(c.cfg.DataDir, ref); err != nil {
		_ = secret.Undefine()
		return nil, fmt.Errorf("cifs: persist (rolled back libvirt secret): %w", err)
	}

	// Step 5: in-memory cache.
	cifsSecretsMu.Lock()
	cifsSecrets[poolName] = *ref
	cifsSecretsMu.Unlock()

	return ref, nil
}

// unsetCIFSSecret removes the libvirt secret and the on-disk
// mapping for a pool. Idempotent: no error if already absent.
// Safe to call from cleanup paths even if libvirt is offline
// or if the connector is nil (caller without a live connector
// still gets the disk entry cleaned up).
func unsetCIFSSecret(ctx context.Context, c *Connector, poolName string) error {
	cifsSecretsMu.Lock()
	ref, inMem := cifsSecrets[poolName]
	delete(cifsSecrets, poolName)
	cifsSecretsMu.Unlock()

	if !inMem && c != nil && c.cfg != nil {
		if r, err := readCIFSSecretRef(c.cfg.DataDir, poolName); err == nil && r != nil {
			ref = *r
		}
	}

	if ref.SecretUUID != "" && c != nil && c.IsConnected() {
		if conn := c.Get(); conn != nil {
			if s, err := conn.LookupSecretByUUIDString(ref.SecretUUID); err == nil && s != nil {
				_ = s.Undefine()
				s.Free()
			}
		}
	}

	if c == nil || c.cfg == nil {
		return nil
	}
	return removeCIFSSecretRef(c.cfg.DataDir, poolName)
}

// lookupCIFSSecretRef returns the ref for a pool. Reads from the
// in-memory map first, falls back to disk. Returns (nil, nil) if
// the pool has no secret on record.
func lookupCIFSSecretRef(dataDir, poolName string) (*SecretRef, error) {
	cifsSecretsMu.RLock()
	if r, ok := cifsSecrets[poolName]; ok {
		cifsSecretsMu.RUnlock()
		return &r, nil
	}
	cifsSecretsMu.RUnlock()
	return readCIFSSecretRef(dataDir, poolName)
}

// loadCIFSSecretsFromDisk hydrates the in-memory map at startup.
// Called from Connector.EnsureDefaults (best-effort, logs warnings).
// Missing file is not an error; corrupt file is logged and ignored.
func loadCIFSSecretsFromDisk(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	b, err := os.ReadFile(cifsSecretsPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var all map[string]SecretRef
	if err := json.Unmarshal(b, &all); err != nil {
		return fmt.Errorf("cifs-secrets.json: %w", err)
	}
	cifsSecretsMu.Lock()
	defer cifsSecretsMu.Unlock()
	for k, v := range all {
		cifsSecrets[k] = v
	}
	return nil
}

// persistCIFSSecretRef writes the poolName -> secretUUID mapping
// to disk atomically (write to .tmp + rename). The on-disk file
// never contains the password — only the UUID libvirt assigned.
func persistCIFSSecretRef(dataDir string, ref *SecretRef) error {
	if dataDir == "" {
		return errors.New("cifs: dataDir is empty")
	}
	path := cifsSecretsPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	cifsSecretsMu.Lock()
	all := make(map[string]SecretRef, len(cifsSecrets)+1)
	for k, v := range cifsSecrets {
		all[k] = v
	}
	cifsSecretsMu.Unlock()

	// Layer any on-disk entries on top so we don't drop a mapping
	// that hasn't been hydrated into memory yet (e.g. before
	// loadCIFSSecretsFromDisk has run on a fresh boot).
	if existing, err := readCIFSSecretRefMap(dataDir); err == nil {
		for k, v := range existing {
			if _, taken := all[k]; !taken {
				all[k] = v
			}
		}
	}
	all[ref.PoolName] = *ref

	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, cifsSecretsFileMode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeCIFSSecretRef(dataDir, poolName string) error {
	if dataDir == "" {
		return nil
	}
	path := cifsSecretsPath(dataDir)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var all map[string]SecretRef
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	if _, ok := all[poolName]; !ok {
		return nil
	}
	delete(all, poolName)
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, cifsSecretsFileMode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readCIFSSecretRef(dataDir, poolName string) (*SecretRef, error) {
	all, err := readCIFSSecretRefMap(dataDir)
	if err != nil || all == nil {
		return nil, err
	}
	r, ok := all[poolName]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

// readCIFSSecretRefMap returns the full poolName -> SecretRef map
// from disk. A missing file returns (nil, nil); a corrupt file
// returns an error.
func readCIFSSecretRefMap(dataDir string) (map[string]SecretRef, error) {
	if dataDir == "" {
		return nil, nil
	}
	b, err := os.ReadFile(cifsSecretsPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all map[string]SecretRef
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	return all, nil
}

// buildCIFSSecretXML constructs the libvirt <secret> XML for CIFS
// auth. Unlike ceph/iscsi/tls, libvirt has no native "cifs" usage
// type, so we omit the <usage> block entirely and rely on the
// pool's <auth type='cifs' username='...'> reference for binding.
// The user and usage are stored in <description> for human
// readability when listing secrets with `virsh secret-list`.
//
// Reference: https://libvirt.org/formatsecret.html
func buildCIFSSecretXML(usage, user string) string {
	return fmt.Sprintf(`<secret ephemeral='no' private='yes'>
  <description>CIFS auth: pool usage=%s user=%s</description>
</secret>`, xmlEscape(usage), xmlEscape(user))
}

// verifyCIFSSecretsConsistency is called at startup. For every
// pool we have a mapping for, it checks the libvirt secret still
// exists. Missing secrets are logged as warnings — the operator
// can re-add them via PUT /api/storage/pools/{name} with
// cifs-needs-reauth=true. Never fatal: libvirt being offline is
// a valid state at boot (e.g. after a libvirtd restart).
func verifyCIFSSecretsConsistency(ctx context.Context, c *Connector) error {
	if c == nil || !c.IsConnected() {
		return nil
	}
	conn := c.Get()
	if conn == nil {
		return nil
	}
	cifsSecretsMu.RLock()
	pairs := make([]SecretRef, 0, len(cifsSecrets))
	for _, v := range cifsSecrets {
		pairs = append(pairs, v)
	}
	cifsSecretsMu.RUnlock()

	for _, ref := range pairs {
		s, err := conn.LookupSecretByUUIDString(ref.SecretUUID)
		if err != nil {
			slog.Warn("cifs_secret_missing_in_libvirt",
				"pool", ref.PoolName,
				"uuid", ref.SecretUUID,
				"hint", "PUT /api/storage/pools/"+ref.PoolName+" with cifs-needs-reauth=true to recreate")
			continue
		}
		s.Free()
	}
	return nil
}

// LoadCIFSSecrets hydrates the in-memory cifsSecrets map from
// disk at startup. Best-effort: corrupt files are logged but
// don't stop the backend.
func LoadCIFSSecrets(c *Connector) error {
	if c == nil || c.cfg == nil {
		return nil
	}
	return loadCIFSSecretsFromDisk(c.cfg.DataDir)
}

// VerifyCIFSSecretsConsistency is the exported wrapper for the
// startup-time consistency check. Safe to call when libvirt is
// offline; it returns nil in that case.
func VerifyCIFSSecretsConsistency(ctx context.Context, c *Connector) error {
	return verifyCIFSSecretsConsistency(ctx, c)
}

// DefineCIFSSecretForTest exposes defineCIFSSecret for the
// cmd/cifs-smoke binary. It is not used by the running backend
// (callers go through CreateStoragePool) and is only here to
// keep the smoke harness out of the api package.
func DefineCIFSSecretForTest(ctx context.Context, c *Connector,
	poolName, usage, user, pass string) (*SecretRef, error) {
	return defineCIFSSecret(ctx, c, poolName, usage, user, pass)
}

// UnsetCIFSSecretForTest exposes unsetCIFSSecret for the smoke
// harness. Same rationale as DefineCIFSSecretForTest.
func UnsetCIFSSecretForTest(ctx context.Context, c *Connector, poolName string) error {
	return unsetCIFSSecret(ctx, c, poolName)
}

// LookupCIFSSecretRefForTest exposes lookupCIFSSecretRef for
// the smoke harness.
func LookupCIFSSecretRefForTest(dataDir, poolName string) (*SecretRef, error) {
	return lookupCIFSSecretRef(dataDir, poolName)
}
