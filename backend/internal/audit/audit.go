// Package audit provides a small append-only JSONL logger for
// security-relevant actions (logins, user changes, VM lifecycle,
// system changes). Records are written one per line to
// {DataDir}/audit.log with 0600 permissions. The file is rotated when
// it exceeds 10 MB.
package audit

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"webkvm/internal/logging"
)

const (
	maxFileBytes = 10 << 20 // 10 MB
)

type Entry struct {
	Time     string                 `json:"time"`
	User     string                 `json:"user,omitempty"`
	Role     string                 `json:"role,omitempty"`
	Action   string                 `json:"action"`
	Resource string                 `json:"resource,omitempty"`
	IP       string                 `json:"ip,omitempty"`
	Detail   map[string]interface{} `json:"detail,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

type Logger struct {
	mu   sync.Mutex
	path string
	file *os.File
	w    *bufio.Writer
}

func New(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	l := &Logger{path: path}
	if err := l.openLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) openLocked() error {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	l.file = f
	l.w = bufio.NewWriter(f)
	return nil
}

func (l *Logger) rotateIfNeededLocked() error {
	if l.file == nil {
		return l.openLocked()
	}
	info, err := l.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < maxFileBytes {
		return nil
	}
	if err := l.w.Flush(); err != nil {
		return err
	}
	if err := l.file.Close(); err != nil {
		return err
	}
	// Rename to .1 (overwriting the previous one).
	_ = os.Remove(l.path + ".1")
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return err
	}
	return l.openLocked()
}

// Log writes an entry. Safe for concurrent use.
func (l *Logger) Log(e Entry) {
	if l == nil {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rotateIfNeededLocked(); err != nil {
		return
	}
	_ = json.NewEncoder(l.w).Encode(e)
	_ = l.w.Flush()
}

// ListOptions filters List's results. All fields are optional; a zero
// value matches everything. Q is a case-insensitive substring match
// across user/role/action/resource/ip/error; User and Action require
// an exact match.
type ListOptions struct {
	Q      string
	User   string
	Action string
}

// List returns entries matching opts, newest first, with offset/limit
// paging applied after filtering (so `total` reflects the filtered
// count, not the whole log). It re-reads the log file(s) from disk on
// every call — audit.log is capped at 10MB by rotation (plus one .1
// backup), so a full parse is cheap even on a long-running install.
func (l *Logger) List(opts ListOptions, limit, offset int) (entries []Entry, total int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		if err := l.w.Flush(); err != nil {
			return nil, 0, err
		}
	}

	var all []Entry
	for _, p := range []string{l.path + ".1", l.path} {
		es, ferr := readEntries(p)
		if ferr != nil && !os.IsNotExist(ferr) {
			return nil, 0, ferr
		}
		all = append(all, es...)
	}
	// Newest first.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	q := strings.ToLower(opts.Q)
	filtered := all[:0]
	for _, e := range all {
		if opts.User != "" && e.User != opts.User {
			continue
		}
		if opts.Action != "" && e.Action != opts.Action {
			continue
		}
		if q != "" && !matchesQ(e, q) {
			continue
		}
		filtered = append(filtered, e)
	}

	total = len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < total {
		end = offset + limit
	}
	return filtered[offset:end], total, nil
}

func matchesQ(e Entry, lowerQ string) bool {
	return strings.Contains(strings.ToLower(e.User), lowerQ) ||
		strings.Contains(strings.ToLower(e.Action), lowerQ) ||
		strings.Contains(strings.ToLower(e.Resource), lowerQ) ||
		strings.Contains(strings.ToLower(e.IP), lowerQ) ||
		strings.Contains(strings.ToLower(e.Error), lowerQ)
}

func readEntries(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			out = append(out, e)
		}
	}
	return out, scanner.Err()
}

// FromRequest extracts a best-effort user/role/ip tuple from r.
// user and role come from the auth middleware's X-* headers; ip
// comes from logging.ClientIP so the audit log and the structured
// request log always agree on the client IP.
func FromRequest(r *http.Request) (user, role, ip string) {
	user = r.Header.Get("X-User")
	role = r.Header.Get("X-Role")
	ip = logging.ClientIP(r)
	return
}
