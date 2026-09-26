package api

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestAcquireDeployLockSerializes: two goroutines race for the same name
// and the second acquire must not resolve until the first is released.
func TestAcquireDeployLockSerializes(t *testing.T) {
	h := &Handler{}
	release1 := h.acquireDeployLock("vmx")
	second := make(chan struct{})
	go func() {
		release2 := h.acquireDeployLock("vmx") // blocks until release1
		close(second)
		release2()
	}()
	time.Sleep(50 * time.Millisecond)
	release1()
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatalf("second acquire never resolved after first release")
	}
}

// TestAcquireDeployLockHoldsWhileBusy: el lock bloquea mientras hay otro holder.
func TestAcquireDeployLockHoldsWhileBusy(t *testing.T) {
	h := &Handler{}
	release := h.acquireDeployLock("dup")
	acquired := make(chan struct{})
	go func() {
		r2 := h.acquireDeployLock("dup")
		acquired <- struct{}{}
		r2()
	}()
	select {
	case <-acquired:
		t.Fatalf("second lock holder acquired while first still holds")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatalf("after release, waiter should acquire")
	}
}

// TestAcquireDeployLockDifferentNamesIndependent: nombres distintos no compiten.
func TestAcquireDeployLockDifferentNamesIndependent(t *testing.T) {
	h := &Handler{}
	r1 := h.acquireDeployLock("a")
	done := make(chan struct{})
	go func() {
		r2 := h.acquireDeployLock("b")
		r2()
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("independent names must not block each other")
	}
	r1()
	defer func() {}()
}

// TestAcquireDeployLockTrimsMap: al liberar, el entry se purga.
func TestAcquireDeployLockTrimsMap(t *testing.T) {
	h := &Handler{}
	release := h.acquireDeployLock("trimme")
	if _, ok := h.deployLocks["trimme"]; !ok {
		t.Fatalf("expected live entry while holder is active")
	}
	release()
	h.deployMu.Lock()
	_, present := h.deployLocks["trimme"]
	h.deployMu.Unlock()
	if present {
		t.Fatalf("deployLocks entry for released name still present (leak)")
	}
}

// TestAcquireDeployLockReacquireAfterRelease: acquire→release→acquire x3.
func TestAcquireDeployLockReacquireAfterRelease(t *testing.T) {
	h := &Handler{}
	for i := 0; i < 3; i++ {
		release := h.acquireDeployLock("again")
		release()
	}
}

// TestAcquireDeployLockConcurrentExclusivity: N goroutines con el mismo
// nombre — el contador dentro del lock nunca supera 1 (exclusión real) y
// el mapa queda vacío tras todas las liberaciones.
func TestAcquireDeployLockConcurrentExclusivity(t *testing.T) {
	h := &Handler{}
	const n = 32
	var critical int
	var race bool
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release := h.acquireDeployLock("shared")
			mu.Lock()
			critical++
			if critical > 1 {
				race = true
			}
			time.Sleep(time.Millisecond)
			critical--
			mu.Unlock()
			release()
		}(i)
	}
	wg.Wait()
	if race {
		t.Fatalf("two holders inside critical section")
	}
	if critical != 0 {
		t.Fatalf("critical count unbalanced: %d", critical)
	}
	h.deployMu.Lock()
	left := len(h.deployLocks)
	h.deployMu.Unlock()
	if left != 0 {
		t.Fatalf("lock map not trimmed after all releases: %d left", left)
	}
}

// TestDeployDiskCandidates: both supported extensions, in fixed order.
func TestDeployDiskCandidates(t *testing.T) {
	got := fmt.Sprint(deployDiskCandidates("mi-vm"))
	want := fmt.Sprint([]string{"mi-vm.qcow2", "mi-vm.img"})
	if got != want {
		t.Fatalf("deployDiskCandidates: got %s, want %s", got, want)
	}
}
