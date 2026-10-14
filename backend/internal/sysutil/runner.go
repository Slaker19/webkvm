package sysutil

import (
	"context"
	"os/exec"
	"time"
)

// CommandRunner abstracts host command execution for testability and safety.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error)
}

// OSCommandRunner executes commands directly on the host system.
type OSCommandRunner struct{}

var DefaultRunner CommandRunner = &OSCommandRunner{}

func (r *OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (r *OSCommandRunner) RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.Run(ctx, name, args...)
}

// MockCommandRunner allows simulating command executions in tests.
type MockCommandRunner struct {
	Handler func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (m *MockCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Handler != nil {
		return m.Handler(ctx, name, args...)
	}
	return nil, nil
}

func (m *MockCommandRunner) RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return m.Run(ctx, name, args...)
}
