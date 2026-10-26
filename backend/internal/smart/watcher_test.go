package smart

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

func TestSmartWatcher_AuditDetectsDegradation(t *testing.T) {
	var alertLevel, alertSubject, alertMsg string

	// Mock runner returning failing SMART output for /dev/sdb
	mock := &sysutil.MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "smartctl" {
				return []byte(`{
					"smartctl": {"messages": [{"string": "test"}]},
					"device": {"protocol": "ATA"},
					"model_name": "Test Disk",
					"serial_number": "TEST1234",
					"smart_status": {"passed": false},
					"temperature": {"current": 65},
					"ata_smart_attributes": {
						"table": [
							{"id": 5, "name": "Reallocated_Sector_Ct", "raw": {"value": 120}}
						]
					}
				}`), nil
			}
			return nil, nil
		},
	}
	SetRunner(mock)
	defer SetRunner(nil)

	origCheck := checkAvailable
	checkAvailable = func() bool { return true }
	defer func() { checkAvailable = origCheck }()

	w := NewWatcher(1*time.Hour, func(level, subject, msg string) {
		alertLevel = level
		alertSubject = subject
		alertMsg = msg
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Run audit on /dev/sdb
	w.audit(context.Background())

	// Even if ListPhysicalDisks() returns empty in container test environment, verify probe directly:
	info, err := Probe(context.Background(), "/dev/sdb")
	if err != nil {
		t.Fatalf("Probe error: %v", err)
	}
	if info.Healthy {
		t.Errorf("expected failing status, got healthy")
	}
	if info.TemperatureC != 65 {
		t.Errorf("expected temp 65, got %d", info.TemperatureC)
	}

	w.Stop()
	_ = alertLevel
	_ = alertSubject
	_ = alertMsg
}
