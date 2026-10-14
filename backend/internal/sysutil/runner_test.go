package sysutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOSCommandRunner(t *testing.T) {
	runner := &OSCommandRunner{}
	out, err := runner.RunWithTimeout("echo", 1*time.Second, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", string(out))
	}
}

func TestMockCommandRunner(t *testing.T) {
	mock := &MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "smartctl" {
				return []byte(`{"smart_status":{"passed":true}}`), nil
			}
			return nil, errors.New("command not found")
		},
	}

	out, err := mock.Run(context.Background(), "smartctl", "-j")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"smart_status":{"passed":true}}` {
		t.Fatalf("unexpected output: %s", string(out))
	}

	_, err = mock.Run(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}
