package backupstore

import (
	"fmt"
	"sort"
	"strings"
)

// Discrepancy represents a file whose size in the job index doesn't match storage.
type Discrepancy struct {
	Filename string `json:"filename"`
	Expected int64  `json:"expected"`
	Actual   int64  `json:"actual"`
}

// ReconcileReport is the result of analyzing storage vs the jobs index.
type ReconcileReport struct {
	TargetID      string        `json:"target_id"`
	TargetName    string        `json:"target_name"`
	ScannedFiles  int           `json:"scanned_files"`
	OrphanFiles   []BackupFile  `json:"orphan_files"`   // files on storage not referenced by any job
	GhostFiles    []string      `json:"ghost_files"`    // filenames in jobs index but missing on storage
	Discrepancies []Discrepancy `json:"discrepancies"`  // size mismatches
	AdoptedJobs   int           `json:"adopted_jobs"`   // number of synthetic jobs created (when apply=true)
	CleanedGhosts int           `json:"cleaned_ghosts"` // number of ghost entries cleaned (when apply=true)
}

// parseJobFileKind parses whether a backup archive is a config tar or a vm archive.
func parseJobFileKind(filename string) (kind string, vmID string) {
	if strings.HasSuffix(filename, ".qcow2") {
		name := strings.TrimSuffix(filename, ".qcow2")
		if strings.HasPrefix(name, "webkvm-restore-") {
			return "vm", ""
		}
		// Format: webkvm-<host>-<ts26>-<randHex>-<name>-<device>.qcow2
		parts := strings.Split(name, "-")
		if len(parts) >= 5 {
			return "vm", parts[len(parts)-2]
		}
		return "vm", ""
	}
	name := filename
	for _, ext := range []string{".tar.zst", ".tar.gz"} {
		name = strings.TrimSuffix(name, ext)
	}
	if strings.HasSuffix(name, "-config") {
		return "config", ""
	}
	parts := strings.Split(name, "-")
	if len(parts) >= 4 {
		return "vm", parts[len(parts)-1]
	}
	return "vm", ""
}

// Reconcile compares files found on the target against the job records in store.
// If apply is false (dry-run), it only reports discrepancies.
// If apply is true, orphan files are grouped by run suffix and adopted as synthetic
// completed jobs, and missing ghost references in existing jobs are updated.
func (s *Store) Reconcile(tgt Target, apply bool) (ReconcileReport, error) {
	storageFiles, err := ListBackupsOnTarget(tgt)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("list target backups: %w", err)
	}

	report := ReconcileReport{
		TargetID:      tgt.ID,
		TargetName:    tgt.Name,
		ScannedFiles:  len(storageFiles),
		OrphanFiles:   make([]BackupFile, 0),
		GhostFiles:    make([]string, 0),
		Discrepancies: make([]Discrepancy, 0),
	}

	storageMap := make(map[string]BackupFile, len(storageFiles))
	for _, f := range storageFiles {
		storageMap[f.Filename] = f
	}

	s.mu.RLock()
	allJobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if j != nil {
			allJobs = append(allJobs, *j)
		}
	}
	s.mu.RUnlock()

	// Build indexed file map for this target
	indexedFiles := make(map[string]int64) // filename -> size
	for _, j := range allJobs {
		if j.TargetID != tgt.ID {
			continue
		}
		for _, f := range j.Files {
			indexedFiles[f.Filename] = f.Size
		}
		if len(j.Files) == 0 && j.Filename != "" {
			indexedFiles[j.Filename] = j.Size
		}
	}

	// 1. Find orphans & discrepancies
	for _, sf := range storageFiles {
		expectedSize, found := indexedFiles[sf.Filename]
		if !found {
			if ValidBackupFilename(sf.Filename) {
				report.OrphanFiles = append(report.OrphanFiles, sf)
			}
		} else if expectedSize > 0 && expectedSize != sf.Size {
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Filename: sf.Filename,
				Expected: expectedSize,
				Actual:   sf.Size,
			})
		}
	}

	// 2. Find ghost files
	for filename := range indexedFiles {
		if _, exists := storageMap[filename]; !exists {
			report.GhostFiles = append(report.GhostFiles, filename)
		}
	}
	sort.Strings(report.GhostFiles)

	if !apply {
		return report, nil
	}

	// Apply mode: adopt orphans by grouping them by run suffix
	orphanGroups := make(map[string][]BackupFile)
	for _, orphan := range report.OrphanFiles {
		suffix := runSuffixFromFilename(orphan.Filename)
		if suffix == "" {
			suffix = orphan.Modified.UTC().Format("20060102T150405.000000000Z") + "-recon"
		}
		orphanGroups[suffix] = append(orphanGroups[suffix], orphan)
	}

	adoptedCount := 0
	for suffix, group := range orphanGroups {
		if len(group) == 0 {
			continue
		}
		var totalSize int64
		files := make([]JobFile, 0, len(group))
		filenames := make([]string, 0, len(group))
		newestMod := group[0].Modified

		for _, f := range group {
			totalSize += f.Size
			filenames = append(filenames, f.Filename)
			kind, vmID := parseJobFileKind(f.Filename)
			files = append(files, JobFile{
				Filename: f.Filename,
				Size:     f.Size,
				Kind:     kind,
				VMID:     vmID,
			})
			if f.Modified.After(newestMod) {
				newestMod = f.Modified
			}
		}

		syntheticJob := Job{
			TargetID:  tgt.ID,
			Mode:      "full",
			Status:    "success",
			StartedAt: newestMod,
			EndedAt:   newestMod,
			Filename:  primaryOf(files),
			Filenames: filenames,
			Files:     files,
			Size:      totalSize,
			Message:   fmt.Sprintf("Reconciled from storage (%s)", suffix),
		}

		if _, err := s.RecordJob(syntheticJob); err == nil {
			adoptedCount++
		}
	}
	report.AdoptedJobs = adoptedCount

	// Ghost cleanup: update job records that reference ghost files
	cleanedGhosts := 0
	if len(report.GhostFiles) > 0 {
		ghostSet := make(map[string]bool, len(report.GhostFiles))
		for _, g := range report.GhostFiles {
			ghostSet[g] = true
		}

		s.mu.Lock()
		modified := false
		for i := range s.jobs {
			if s.jobs[i].TargetID != tgt.ID {
				continue
			}
			newFiles := make([]JobFile, 0, len(s.jobs[i].Files))
			newFilenames := make([]string, 0, len(s.jobs[i].Filenames))
			var newSize int64

			for _, f := range s.jobs[i].Files {
				if !ghostSet[f.Filename] {
					newFiles = append(newFiles, f)
					newFilenames = append(newFilenames, f.Filename)
					newSize += f.Size
				} else {
					cleanedGhosts++
				}
			}

			if len(newFiles) != len(s.jobs[i].Files) {
				s.jobs[i].Files = newFiles
				s.jobs[i].Filenames = newFilenames
				s.jobs[i].Size = newSize
				if len(newFiles) == 0 {
					s.jobs[i].Status = "error"
					s.jobs[i].Error = "All backup files missing on storage"
				}
				modified = true
			}
		}
		if modified {
			_ = s.saveJobs()
		}
		s.mu.Unlock()
	}
	report.CleanedGhosts = cleanedGhosts

	return report, nil
}
