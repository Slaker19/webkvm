package incus

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/models"
)

func TestRingBuffer(t *testing.T) {
	t.Run("push within capacity", func(t *testing.T) {
		r := newRingBuffer(3)
		samples := []models.MetricsSample{
			{T: 1, V: 10.0},
			{T: 2, V: 20.0},
		}

		for _, s := range samples {
			r.push(s)
		}

		snap := r.snapshot()
		if len(snap) != 2 {
			t.Fatalf("expected 2 samples, got %d", len(snap))
		}
		if !reflect.DeepEqual(snap, samples) {
			t.Errorf("expected %v, got %v", samples, snap)
		}
	})

	t.Run("push exceeding capacity", func(t *testing.T) {
		r := newRingBuffer(3)
		samples := []models.MetricsSample{
			{T: 1, V: 10.0},
			{T: 2, V: 20.0},
			{T: 3, V: 30.0},
			{T: 4, V: 40.0},
			{T: 5, V: 50.0},
		}

		for _, s := range samples {
			r.push(s)
		}

		snap := r.snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 samples, got %d", len(snap))
		}

		expected := []models.MetricsSample{
			{T: 3, V: 30.0},
			{T: 4, V: 40.0},
			{T: 5, V: 50.0},
		}
		if !reflect.DeepEqual(snap, expected) {
			t.Errorf("expected %v, got %v", expected, snap)
		}
	})
}

// mockFetcher simulates an Incus client for testing.
type mockFetcher struct {
	instances []api.Instance
	states    map[string]*api.InstanceState
	errList   error
	errState  error
}

func (m *mockFetcher) ListInstances() ([]api.Instance, error) {
	if m.errList != nil {
		return nil, m.errList
	}
	return m.instances, nil
}

func (m *mockFetcher) State(name string) (*api.InstanceState, error) {
	if m.errState != nil {
		return nil, m.errState
	}
	state, ok := m.states[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return state, nil
}

func TestMetricsCollector_sampleOnce(t *testing.T) {
	fetcher := &mockFetcher{
		instances: []api.Instance{
			{Name: "running-vm", Status: "Running"},
			{Name: "stopped-vm", Status: "Stopped"},
		},
		states: map[string]*api.InstanceState{
			"running-vm": {
				CPU:    api.InstanceStateCPU{Usage: 1000000000}, // 1 second
				Memory: api.InstanceStateMemory{Usage: 512, Total: 1024},
				Network: map[string]api.InstanceStateNetwork{
					"eth0": {
						Counters: api.InstanceStateNetworkCounters{
							BytesReceived: 1000,
							BytesSent:     500,
						},
					},
				},
			},
		},
	}

	collector := NewMetricsCollector(fetcher, nil)
	collector.capacity = 5
	collector.sampleOnce()

	// Verify running VM metrics
	metrics, err := collector.Get("running-vm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(metrics.CPU.Points) != 0 {
		t.Errorf("first sample should not emit points for deltas, got %v points", len(metrics.CPU.Points))
	}
	if len(metrics.RAM.Points) != 1 {
		t.Fatalf("expected 1 RAM point, got %v", len(metrics.RAM.Points))
	}
	if metrics.RAM.Points[0].V != 50.0 {
		t.Errorf("expected RAM 50.0, got %v", metrics.RAM.Points[0].V)
	}

	// Wait 100ms and sample again to get deltas
	time.Sleep(100 * time.Millisecond)

	// Update the mock state for the next sample
	fetcher.states["running-vm"].CPU.Usage = 1500000000 // +0.5 second
	fetcher.states["running-vm"].Network["eth0"] = api.InstanceStateNetwork{
		Counters: api.InstanceStateNetworkCounters{
			BytesReceived: 2000, // +1000
			BytesSent:     1500, // +1000
		},
	}
	collector.sampleOnce()

	metrics, err = collector.Get("running-vm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(metrics.CPU.Points) != 1 {
		t.Fatalf("expected 1 CPU point, got %v", len(metrics.CPU.Points))
	}

	if len(metrics.NetRx.Points) != 1 {
		t.Fatalf("expected 1 NetRx point, got %v", len(metrics.NetRx.Points))
	}

	// Ensure stopped VM is not tracked or has 0 points
	stoppedMetrics, err := collector.Get("stopped-vm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stoppedMetrics.CPU.Points) != 0 {
		t.Errorf("stopped VM should have 0 points, got %v", len(stoppedMetrics.CPU.Points))
	}
}

func TestMetricsCollector_SetSink(t *testing.T) {
	fetcher := &mockFetcher{
		instances: []api.Instance{
			{Name: "test-vm", Status: "Running"},
		},
		states: map[string]*api.InstanceState{
			"test-vm": {
				CPU:    api.InstanceStateCPU{Usage: 100},
				Memory: api.InstanceStateMemory{Usage: 1, Total: 100},
			},
		},
	}

	collector := NewMetricsCollector(fetcher, nil)
	collector.capacity = 5

	called := false
	sink := func(vmID string, at time.Time, m models.VMMetrics) {
		if vmID == "test-vm" {
			called = true
		}
	}

	collector.SetSink(sink)
	collector.sampleOnce()

	if !called {
		t.Errorf("expected sink to be called")
	}
}
