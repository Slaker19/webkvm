package ha

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MaxLedgerEvents caps the persisted fence evidence entries.
const MaxLedgerEvents = 50

// FencingLedgerPath is the evidence ledger inside dataDir. It holds no
// secrets; like ha-fencing.json it is not part of the config backup.
func FencingLedgerPath(dataDir string) string {
	return filepath.Join(dataDir, "ha-fencing-events.json")
}

type fencingLedgerFile struct {
	ClusterName string       `json:"cluster_name"`
	Events      []FenceEvent `json:"events"`
}

var fencingLedgerMu sync.Mutex

func ledgerKey(e FenceEvent) string {
	c := e.Completed
	if t, ok := ParseFenceTime(c); ok {
		c = t.Format(time.RFC3339Nano)
	}
	return e.Target + "|" + e.Action + "|" + c
}

func sortFenceEvents(events []FenceEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		ti, oki := ParseFenceTime(events[i].Completed)
		tj, okj := ParseFenceTime(events[j].Completed)
		if oki && okj {
			return ti.After(tj)
		}
		return oki && !okj
	})
}

// loadLedger reads the file; a missing, non-regular, group/world-accessible,
// oversized or corrupt file yields an empty ledger (it is rebuilt on save).
func loadLedger(path string) fencingLedgerFile {
	var f fencingLedgerFile
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || fi.Size() > 1<<20 {
		return fencingLedgerFile{}
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &f) != nil {
		return fencingLedgerFile{}
	}
	return f
}

// MergeFencingLedger folds the successful reboot/off events of the live
// fence_history of st into DATA_DIR/ha-fencing-events.json and returns the
// ledger events (newest first) that belong to this cluster and its nodes.
// Events are never deleted; a different cluster name starts a new ledger.
// Without a data dir, cluster name or node list nothing is read or written.
func MergeFencingLedger(dataDir string, st PacemakerStatus, now time.Time) []FenceEvent {
	if dataDir == "" || st.ClusterName == "" || len(st.NodeNames) == 0 {
		return nil
	}
	isNode := func(t string) bool {
		for _, n := range st.NodeNames {
			if n == t {
				return true
			}
		}
		return false
	}
	fencingLedgerMu.Lock()
	defer fencingLedgerMu.Unlock()
	path := FencingLedgerPath(dataDir)
	led := loadLedger(path)
	changed := false
	if led.ClusterName != st.ClusterName {
		changed = len(led.Events) > 0
		led = fencingLedgerFile{ClusterName: st.ClusterName}
	}
	have := map[string]bool{}
	for _, e := range led.Events {
		have[ledgerKey(e)] = true
	}
	for _, e := range st.FenceEvents {
		t, ok := ParseFenceTime(e.Completed)
		if !ok || !e.Successful() || !isNode(e.Target) {
			continue
		}
		e.Completed = t.Format(time.RFC3339)
		e.Source = ""
		k := ledgerKey(e)
		if have[k] {
			continue
		}
		have[k] = true
		e.FirstSeen = now.UTC().Format(time.RFC3339)
		led.Events = append(led.Events, e)
		changed = true
	}
	sortFenceEvents(led.Events)
	if len(led.Events) > MaxLedgerEvents {
		led.Events = led.Events[:MaxLedgerEvents]
	}
	if changed {
		if data, err := json.MarshalIndent(led, "", "  "); err == nil {
			_ = WriteFileAtomic(path, data, 0o600)
		}
	}
	out := make([]FenceEvent, 0, len(led.Events))
	for _, e := range led.Events {
		if e.Successful() && isNode(e.Target) {
			out = append(out, e)
		}
	}
	return out
}
