package api

import (
	"bytes"
	"io"
	"testing"
	"time"

	"webkvm/internal/models"
)

// TestProgressReportingWriterReportsCumulativeBytes verifies the writer
// reports every cumulative byte count to its callback, regardless of
// throttling (throttling lives in the caller's closure).
func TestProgressReportingWriterReportsCumulativeBytes(t *testing.T) {
	var buf bytes.Buffer
	var reports []int64
	pw := &progressReportingWriter{
		w:      &buf,
		total:  1000,
		report: func(n int64) { reports = append(reports, n) },
	}

	data := bytes.Repeat([]byte("x"), 1000)
	for i := 0; i < 4; i++ {
		n, err := pw.Write(data[i*250 : (i+1)*250])
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if n != 250 {
			t.Fatalf("write %d returned %d, want 250", i, n)
		}
	}

	want := []int64{250, 500, 750, 1000}
	if len(reports) != len(want) {
		t.Fatalf("got %d reports %v, want %d", len(reports), reports, len(want))
	}
	for i, w := range want {
		if reports[i] != w {
			t.Fatalf("report[%d] = %d, want %d", i, reports[i], w)
		}
	}
	if buf.Len() != 1000 {
		t.Fatalf("underlying writer received %d bytes, want 1000", buf.Len())
	}
}

// TestDownloadProgressUpdatesJob mirrors the throttled closure used by
// doDownloadISO and checks that a partial write lands an intermediate
// percentage on the tracked job.
func TestDownloadProgressUpdatesJob(t *testing.T) {
	jobID := "dl_progress_test"
	storeJob(&models.DownloadJob{ID: jobID, Status: "queued", Progress: 0})
	defer func() {
		jobsMu.Lock()
		delete(isoJobs, jobID)
		jobsMu.Unlock()
	}()

	total := int64(1000)
	var lastReport time.Time
	pw := &progressReportingWriter{
		w:     io.Discard,
		total: total,
		report: func(n int64) {
			if n < total && time.Since(lastReport) < 250*time.Millisecond {
				return
			}
			lastReport = time.Now()
			pct := float64(0)
			if total > 0 {
				pct = float64(n) / float64(total) * 100
				if pct > 100 {
					pct = 100
				}
			}
			updateJob(jobID, pct, "downloading", "")
		},
	}

	if _, err := pw.Write(make([]byte, 500)); err != nil {
		t.Fatalf("write: %v", err)
	}

	job, ok := getJob(jobID)
	if !ok {
		t.Fatal("job not found after write")
	}
	if job.Progress != 50 {
		t.Fatalf("progress = %v, want 50", job.Progress)
	}
	if job.Status != "downloading" {
		t.Fatalf("status = %q, want downloading", job.Status)
	}
}

// TestUpdateJobSeparatesMessageFromError pins down which field a job's
// text lands in. Every download path calls updateJob with a string that
// is sometimes narration and sometimes a failure; filing narration as
// an error left finished jobs carrying one, and hid the text from
// clients, which only read Error when the job failed.
func TestUpdateJobSeparatesMessageFromError(t *testing.T) {
	id := "job_update_test"
	storeJob(&models.DownloadJob{ID: id, Status: "queued"})
	t.Cleanup(func() {
		jobsMu.Lock()
		delete(isoJobs, id)
		jobsMu.Unlock()
	})

	updateJob(id, 30, "downloading", "Finalizing and converting image...")
	j, ok := getJob(id)
	if !ok {
		t.Fatal("job vanished")
	}
	if j.Message != "Finalizing and converting image..." {
		t.Errorf("Message = %q, want the progress commentary", j.Message)
	}
	if j.Error != "" {
		t.Errorf("Error = %q, want empty while the job is healthy", j.Error)
	}

	updateJob(id, 99, "error", "mirror unreachable")
	j, _ = getJob(id)
	if j.Error != "mirror unreachable" {
		t.Errorf("Error = %q, want the failure reason", j.Error)
	}

	// A retry that succeeds must not keep showing the earlier failure.
	updateJob(id, 100, "completed", "cached in pool Lexar-Discos")
	j, _ = getJob(id)
	if j.Error != "" {
		t.Errorf("Error = %q, want it cleared once the job recovered", j.Error)
	}
	if j.Message != "cached in pool Lexar-Discos" {
		t.Errorf("Message = %q, want the success text", j.Message)
	}
}
