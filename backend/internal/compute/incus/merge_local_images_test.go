package incus

import (
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/models"
)

// The Images page renders one card per entry of this list, keyed on
// ref. Svelte 5 throws each_key_duplicate when a key repeats and
// ABORTS the whole render pass, so a single ambiguous entry does not
// degrade one card — it leaves the entire page blank: zero cached
// images, zero ISOs and zero base disks, while every API call behind it
// returned HTTP 200 with data.
//
// That is not hypothetical. On the audit host, two Ubuntu "resolute"
// builds and two Debian "trixie" builds were cached with no aliases.
// localByAlias keeps only the last image per (os, release), so the
// others fell through to the append path and were given a ref derived
// from (os, release) — identical to the catalog entry their sibling had
// just claimed.
//
// Every test here asserts the invariant directly: refs are unique.

func localImage(fp, os, release string, aliases ...string) api.Image {
	img := api.Image{
		Fingerprint:  fp,
		Size:         123456,
		Architecture: "x86_64",
		CreatedAt:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		ImagePut: api.ImagePut{
			Properties: map[string]string{},
		},
	}
	if os != "" {
		img.Properties["os"] = os
	}
	if release != "" {
		img.Properties["release"] = release
	}
	for _, a := range aliases {
		img.Aliases = append(img.Aliases, api.ImageAlias{Name: a})
	}
	return img
}

func assertRefsUnique(t *testing.T, items []models.IncusImageItem) {
	t.Helper()
	seen := make(map[string]int, len(items))
	for i, it := range items {
		key := it.Ref
		if key == "" {
			t.Errorf("entry %d (%q) has an empty ref; the UI keys on it", i, it.Label)
			continue
		}
		seen[key]++
	}
	for ref, n := range seen {
		if n > 1 {
			t.Errorf("ref %q appears %d times; Svelte would abort the render", ref, n)
		}
	}
}

func catalogOf(refs ...string) []models.IncusImageItem {
	out := make([]models.IncusImageItem, 0, len(refs))
	for _, r := range refs {
		out = append(out, models.IncusImageItem{Ref: r, Label: r})
	}
	return out
}

// The exact shape found on the audit host: two builds of the same
// release, no aliases, catalog carries that release.
func TestMergeLocalImages_SiblingBuildsDoNotCollide(t *testing.T) {
	catalog := catalogOf("images:ubuntu/resolute", "images:debian/trixie")
	locals := []api.Image{
		localImage("165cb32bf736aaa", "Ubuntu", "resolute"),
		localImage("4b24b84e202dbbb", "Ubuntu", "resolute"),
		localImage("c4a690f5e1d1ccc", "Debian", "trixie"),
		localImage("9fe03de4c231ddd", "Debian", "trixie"),
	}

	got := mergeLocalImages(catalog, locals)
	assertRefsUnique(t, got)

	// Nothing may be lost: four local images plus the catalog entries
	// they did not claim.
	if len(got) < 4 {
		t.Fatalf("got %d entries, want at least 4 (one per local image)", len(got))
	}
	// Every local fingerprint must still be addressable, since that is
	// what the delete button uses.
	byFP := map[string]bool{}
	for _, it := range got {
		if it.Fingerprint != "" {
			byFP[it.Fingerprint] = true
		}
	}
	for _, img := range locals {
		if !byFP[img.Fingerprint[:12]] {
			t.Errorf("local image %s missing from the merged list", img.Fingerprint[:12])
		}
	}
}

// The ordinary case must keep working: one cached image marks its
// catalog entry and is not duplicated as a separate card.
func TestMergeLocalImages_MatchedEntryIsNotDuplicated(t *testing.T) {
	catalog := catalogOf("images:ubuntu/24.04", "images:alpine/3.21")
	locals := []api.Image{localImage("aaaa1111bbbbcccc", "Ubuntu", "24.04")}

	got := mergeLocalImages(catalog, locals)
	assertRefsUnique(t, got)

	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (the catalog, unchanged in size)", len(got))
	}
	marked := 0
	for _, it := range got {
		if it.IsLocal {
			marked++
			if it.Ref != "images:ubuntu/24.04" {
				t.Errorf("local entry has ref %q, want the catalog ref", it.Ref)
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d entries marked local, want exactly 1", marked)
	}
}

// An alias match must win over the os/release heuristic.
func TestMergeLocalImages_AliasMatch(t *testing.T) {
	catalog := catalogOf("images:debian/13")
	locals := []api.Image{
		localImage("ffff0000eeee1111", "", "", "my-own-image"),
	}
	// The alias does not correspond to any catalog ref, so this becomes
	// its own entry under its alias.
	got := mergeLocalImages(catalog, locals)
	assertRefsUnique(t, got)
	if got[0].Ref != "my-own-image" {
		t.Errorf("ref = %q, want the alias %q", got[0].Ref, "my-own-image")
	}
}

// An image with no alias and no os/release falls back to its
// fingerprint, which is already unique.
func TestMergeLocalImages_NoMetadataUsesFingerprint(t *testing.T) {
	catalog := catalogOf("images:ubuntu/24.04")
	locals := []api.Image{localImage("0123456789abcdef", "", "")}

	got := mergeLocalImages(catalog, locals)
	assertRefsUnique(t, got)
	if !strings.HasPrefix(got[0].Ref, "0123456789ab") {
		t.Errorf("ref = %q, want the short fingerprint", got[0].Ref)
	}
}

// The invariant must survive a pile-up, not just a pair.
func TestMergeLocalImages_ManySiblings(t *testing.T) {
	catalog := catalogOf("images:ubuntu/resolute")
	var locals []api.Image
	for _, fp := range []string{
		"1111111111111111", "2222222222222222", "3333333333333333",
		"4444444444444444", "5555555555555555",
	} {
		locals = append(locals, localImage(fp, "Ubuntu", "resolute"))
	}

	got := mergeLocalImages(catalog, locals)
	assertRefsUnique(t, got)

	// One catalog entry absorbs the sibling it matched, so the five
	// local images occupy 1 catalog slot + 4 appended entries. What must
	// hold is that every fingerprint is represented EXACTLY once — no
	// image dropped, none counted twice.
	byFP := map[string]int{}
	for _, it := range got {
		if it.Fingerprint != "" {
			byFP[it.Fingerprint]++
		}
	}
	if len(byFP) != 5 {
		t.Errorf("%d distinct local fingerprints in the list, want 5", len(byFP))
	}
	for fp, n := range byFP {
		if n != 1 {
			t.Errorf("fingerprint %s appears %d times, want 1", fp, n)
		}
	}
	if len(got) != 5 {
		t.Errorf("got %d entries, want 5 (1 catalog slot + 4 siblings)", len(got))
	}
}

func TestMergeLocalImages_EmptyInputs(t *testing.T) {
	assertRefsUnique(t, mergeLocalImages(nil, nil))
	assertRefsUnique(t, mergeLocalImages(catalogOf("images:alpine/3.21"), nil))

	// A local image whose fingerprint would collide as a ref is still
	// the only thing identifying it, so it must survive the round trip.
	got := mergeLocalImages(catalogOf("1111111111111111"), []api.Image{localImage("1111111111111111", "", "")})
	assertRefsUnique(t, got)
	if len(got) == 0 {
		t.Fatal("local image was dropped")
	}
}
