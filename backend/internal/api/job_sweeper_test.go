package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"webkvm/internal/models"
)

// resetJobs clears the shared job map so each test starts clean.
func resetJobs() {
	jobsMu.Lock()
	isoJobs = make(map[string]*models.DownloadJob)
	jobsMu.Unlock()
}

// TestPruneExpiredJobs_OnlyTerminalOlderThanTTL: el sweeper solo purga
// jobs en estado terminal y con antigüedad > TTL. queued/running NUNCA se
// tocan, aunque lleven más tiempo que el TTL (V12-OPS-06).
func TestPruneExpiredJobs_OnlyTerminalOlderThanTTL(t *testing.T) {
	resetJobs()
	defer resetJobs()

	now := time.Now()
	old := now.Add(-25 * time.Hour).Unix() // más viejo que el TTL de 24h

	storeJob(&models.DownloadJob{ID: "j-completed-old", Status: "completed"})
	storeJob(&models.DownloadJob{ID: "j-error-old", Status: "error"})
	storeJob(&models.DownloadJob{ID: "j-completed-fresh", Status: "completed"})
	storeJob(&models.DownloadJob{ID: "j-queued-old", Status: "queued"})
	storeJob(&models.DownloadJob{ID: "j-running-old", Status: "running"})

	// Envejecer a propósito (storeJob les puso UpdatedAt=now).
	func() {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		isoJobs["j-completed-old"].UpdatedAt = old
		isoJobs["j-error-old"].UpdatedAt = old
		isoJobs["j-queued-old"].UpdatedAt = old
		isoJobs["j-running-old"].UpdatedAt = old
	}()

	n := pruneExpiredJobs(now, 24*time.Hour)
	if n != 2 {
		t.Fatalf("pruned = %d, want 2 (only old terminal jobs)", n)
	}

	jobsMu.RLock()
	defer jobsMu.RUnlock()
	if _, ok := isoJobs["j-completed-old"]; ok {
		t.Error("old completed job should have been purged")
	}
	if _, ok := isoJobs["j-error-old"]; ok {
		t.Error("old error job should have been purged")
	}
	for _, keep := range []string{"j-completed-fresh", "j-queued-old", "j-running-old"} {
		if _, ok := isoJobs[keep]; !ok {
			t.Errorf("%s should NOT be purged", keep)
		}
	}
}

// TestPruneExpiredJobs_NoUpdatedAt: un job sin UpdatedAt (0) nunca se
// purga — seguridad ante datos antiguos que no tienen marca de tiempo.
func TestPruneExpiredJobs_NoUpdatedAt(t *testing.T) {
	resetJobs()
	defer resetJobs()

	storeJob(&models.DownloadJob{ID: "j-no-ts", Status: "completed"})
	func() {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		isoJobs["j-no-ts"].UpdatedAt = 0
	}()

	if n := pruneExpiredJobs(time.Now(), 24*time.Hour); n != 0 {
		t.Fatalf("pruned = %d, want 0 (no-timestamp jobs are never purged)", n)
	}
}

// TestStartJobSweeper_ConcurrentAndStoppable: el sweeper corre en su
// propia goroutine, purga bajo contienda y se detiene al cancelar el
// contexto (V12-OPS-06 thread-safety).
func TestStartJobSweeper_ConcurrentAndStoppable(t *testing.T) {
	resetJobs()
	defer resetJobs()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var purged int
	var mu sync.Mutex
	logFn := func(msg string, args ...any) {
		mu.Lock()
		purged++
		mu.Unlock()
	}

	// Un job terminal y viejo que debe purgarse.
	storeJob(&models.DownloadJob{ID: "sweep-old", Status: "error"})
	func() {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		isoJobs["sweep-old"].UpdatedAt = time.Now().Add(-25 * time.Hour).Unix()
	}()

	// Contienda: escrituras/lecturas concurrentes mientras el sweeper corre.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			storeJob(&models.DownloadJob{ID: "live", Status: "running"})
			_, _ = getJob("live")
			updateJob("live", float64(i), "running", "")
			select {
			case <-stop:
				return
			default:
			}
		}
	}()

	StartJobSweeper(ctx, 5*time.Millisecond, time.Nanosecond, logFn)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	gotPurged := purged
	mu.Unlock()
	if gotPurged == 0 {
		t.Error("sweeper did not run/purge before cancellation")
	}
	// El job "live" (running) debe seguir vivo pese a la contienda.
	if _, ok := getJob("live"); !ok {
		t.Error("running job was purged — sweeper must never touch non-terminal jobs")
	}
	if _, ok := getJob("sweep-old"); ok {
		t.Error("old terminal job not purged")
	}

	close(stop)
	cancel()
	wg.Wait()
	// Tras la cancelación, ya no debe purgar más (goroutine detenida).
	before := countJobs()
	time.Sleep(50 * time.Millisecond)
	after := countJobs()
	if before != after {
		t.Error("sweeper still running after context cancellation")
	}
}

func countJobs() int {
	jobsMu.RLock()
	defer jobsMu.RUnlock()
	return len(isoJobs)
}
