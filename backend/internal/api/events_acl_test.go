package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/compute"
	"webkvm/internal/events"
	"webkvm/internal/models"
)

type eventsBackend struct {
	compute.Backend
	mu    sync.Mutex
	metas map[string]models.VMMeta
}

func (b *eventsBackend) ListDomains() ([]models.VM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]models.VM, 0, len(b.metas))
	for id := range b.metas {
		out = append(out, models.VM{ID: id})
	}
	return out, nil
}

func (b *eventsBackend) GetVMMeta(id string) (models.VMMeta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := b.metas[id]; ok {
		return m, nil
	}
	return models.VMMeta{}, nil
}

// A-3: El stream SSE /api/events no debe filtrar eventos de VMs de otros usuarios
// a operadores o viewers que no tienen acceso a esas maquinas.
func TestEventsSSE_FiltraEventosDeOtrasVMs(t *testing.T) {
	hub := events.NewHub()
	defer hub.Close()

	be := &eventsBackend{
		metas: map[string]models.VMMeta{
			"vm-alice": {OwnerID: "alice"},
			"vm-bob":   {OwnerID: "bob"},
		},
	}
	h := &Handler{
		hub:     hub,
		compute: be,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleOperator)

	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.EventsSSE(rec, req)
		close(done)
	}()

	// Esperar que conecte y se registre en el hub
	time.Sleep(50 * time.Millisecond)

	// Emitir un evento de la VM de Bob (ajena) y luego de Alice (propia)
	hub.Broadcast(events.Event{
		Type:  "vm.state",
		VmID:  "vm-bob",
		Name:  "vm-bob-privada",
		State: "running",
	})
	hub.Broadcast(events.Event{
		Type:  "vm.state",
		VmID:  "vm-alice",
		Name:  "vm-alice-propia",
		State: "running",
	})

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	output := rec.Body.String()
	if strings.Contains(output, "vm-bob") {
		t.Fatalf("fuga de eventos en SSE: alice recibio el evento de la VM de bob:\n%s", output)
	}
	if !strings.Contains(output, "vm-alice") {
		t.Fatalf("alice deberia haber recibido el evento de su propia VM:\n%s", output)
	}
}

// Control: El administrador si debe recibir eventos de todas las VMs de la flota.
func TestEventsSSE_AdminRecibeTodasLasVMs(t *testing.T) {
	hub := events.NewHub()
	defer hub.Close()

	be := &eventsBackend{
		metas: map[string]models.VMMeta{
			"vm-alice": {OwnerID: "alice"},
			"vm-bob":   {OwnerID: "bob"},
		},
	}
	h := &Handler{
		hub:     hub,
		compute: be,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("X-User", "root")
	req.Header.Set("X-Role", models.RoleAdmin)

	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.EventsSSE(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	hub.Broadcast(events.Event{
		Type:  "vm.state",
		VmID:  "vm-bob",
		Name:  "vm-bob-privada",
		State: "running",
	})
	hub.Broadcast(events.Event{
		Type:  "vm.state",
		VmID:  "vm-alice",
		Name:  "vm-alice-propia",
		State: "running",
	})

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	output := rec.Body.String()
	if !strings.Contains(output, "vm-bob") || !strings.Contains(output, "vm-alice") {
		t.Fatalf("el admin debe recibir eventos de ambas VMs; got:\n%s", output)
	}
}

// Bug 8: vm.removed arrives after the domain and its metadata are gone,
// so the owner lookup fails. The owner must still receive the removal of
// a VM they could see, while other users' removals stay hidden.
func TestEventsSSE_VMRemovedLlegaAlPropietario(t *testing.T) {
	hub := events.NewHub()
	defer hub.Close()

	be := &eventsBackend{
		metas: map[string]models.VMMeta{
			"vm-alice": {OwnerID: "alice"},
			"vm-bob":   {OwnerID: "bob"},
		},
	}
	h := &Handler{hub: hub, compute: be}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleOperator)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.EventsSSE(rec, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	// Both domains are deleted: metadata disappears before the event.
	be.mu.Lock()
	be.metas = map[string]models.VMMeta{}
	be.mu.Unlock()
	hub.Broadcast(events.Event{Type: "vm.removed", VmID: "vm-bob"})
	hub.Broadcast(events.Event{Type: "vm.removed", VmID: "vm-alice"})

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	output := rec.Body.String()
	if strings.Contains(output, "vm-bob") {
		t.Fatalf("alice recibio la baja de la VM de bob:\n%s", output)
	}
	if !strings.Contains(output, `"vm-alice"`) || !strings.Contains(output, "vm.removed") {
		t.Fatalf("alice deberia recibir vm.removed de su VM:\n%s", output)
	}
}

// A VM the caller could see at connect time and then lost access to
// (ownership moved) must be forgotten: its later vm.removed must not be
// delivered on the strength of the stale `seen` entry.
func TestEventsSSE_AccesoRevocadoOlvidaVM(t *testing.T) {
	hub := events.NewHub()
	defer hub.Close()

	be := &eventsBackend{
		metas: map[string]models.VMMeta{
			"vm-alice": {OwnerID: "alice"},
		},
	}
	h := &Handler{hub: hub, compute: be}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleOperator)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.EventsSSE(rec, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	// Ownership moves to bob: the next state event is dropped for alice.
	be.mu.Lock()
	be.metas["vm-alice"] = models.VMMeta{OwnerID: "bob"}
	be.mu.Unlock()
	hub.Broadcast(events.Event{Type: "vm.state", VmID: "vm-alice", State: "running"})
	time.Sleep(50 * time.Millisecond)

	// Then the VM is deleted.
	be.mu.Lock()
	be.metas = map[string]models.VMMeta{}
	be.mu.Unlock()
	hub.Broadcast(events.Event{Type: "vm.removed", VmID: "vm-alice"})

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if out := rec.Body.String(); strings.Contains(out, "vm-alice") {
		t.Fatalf("alice perdió el acceso y aun así recibió eventos de vm-alice:\n%s", out)
	}
}
