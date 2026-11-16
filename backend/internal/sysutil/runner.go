package sysutil

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// CommandRunner abstracts host command execution for testability and safety.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error)
}

// InputCommandRunner extends CommandRunner for commands that consume stdin,
// such as a reviewed script sent over SSH.
type InputCommandRunner interface {
	RunInput(ctx context.Context, name string, input []byte, args ...string) ([]byte, error)
}

// OSCommandRunner executes commands directly on the host system.
type OSCommandRunner struct{}

var DefaultRunner CommandRunner = &OSCommandRunner{}

func (r *OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	// codeql[go/command-injection] All callers pass a hard-coded command
	// name and separate argv entries; this wrapper never invokes a shell.
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// RunInput runs a command with a byte slice connected to stdin.
func (r *OSCommandRunner) RunInput(ctx context.Context, name string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

func (r *OSCommandRunner) RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.Run(ctx, name, args...)
}

// MockCommandRunner allows simulating command executions in tests.
type MockCommandRunner struct {
	Handler      func(ctx context.Context, name string, args ...string) ([]byte, error)
	InputHandler func(ctx context.Context, name string, input []byte, args ...string) ([]byte, error)
}

func (m *MockCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Handler != nil {
		return m.Handler(ctx, name, args...)
	}
	return nil, nil
}

func (m *MockCommandRunner) RunInput(ctx context.Context, name string, input []byte, args ...string) ([]byte, error) {
	if m.InputHandler != nil {
		return m.InputHandler(ctx, name, input, args...)
	}
	return m.Run(ctx, name, args...)
}

func (m *MockCommandRunner) RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return m.Run(ctx, name, args...)
}

// MockRunner is a MockCommandRunner with a registered command response map for testing.
type MockRunner struct {
	MockCommandRunner
	responses map[string][]byte
	errors    map[string]error
}

// NewMockRunner creates a convenient registry-backed MockRunner.
func NewMockRunner() *MockRunner {
	m := &MockRunner{
		responses: make(map[string][]byte),
		errors:    make(map[string]error),
	}
	m.Handler = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if err, ok := m.errors[name]; ok {
			return m.responses[name], err
		}
		return m.responses[name], nil
	}
	return m
}

// Register maps a binary name to a canned output and error response.
func (m *MockRunner) Register(name string, out []byte, err error) {
	m.responses[name] = out
	if err != nil {
		m.errors[name] = err
	}
}
