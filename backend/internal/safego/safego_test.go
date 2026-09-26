package safego

import (
	"sync"
	"testing"
)

func TestRecoverSwallowsPanic(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	completed := false
	go func() {
		defer wg.Done()
		defer Recover("test_routine")
		panic("simulated critical crash")
	}()

	wg.Wait()
	completed = true

	if !completed {
		t.Fatal("goroutine did not complete safely")
	}
}

func TestRecoverNoPanic(t *testing.T) {
	recovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
		}()
		defer Recover("normal_routine")
		// Normal execution without panic
	}()

	if recovered {
		t.Fatal("did not expect any panic to be raised")
	}
}
