package api

import (
	"testing"

	"webkvm/internal/compute"
)

// A base cloud image is a golden image, so a template pool is a valid
// home for it alongside the disk pools. This mirrors baseImagePools()
// in frontend/src/lib/purpose.js: the two drifting apart would either
// hide a valid destination or offer one the server then refuses.
func TestBaseImagePoolPurpose(t *testing.T) {
	tests := []struct {
		purpose string
		want    bool
	}{
		{compute.PoolPurposeDisk, true},
		{compute.PoolPurposeTemplate, true},
		{compute.PoolPurposeISO, false},
		{compute.PoolPurposeBackup, false},
		{compute.PoolPurposeContainer, false},
		{"", true}, // an untagged pool defaults to disk
	}
	for _, tc := range tests {
		if got := baseImagePoolPurpose(tc.purpose); got != tc.want {
			t.Errorf("baseImagePoolPurpose(%q) = %v, want %v", tc.purpose, got, tc.want)
		}
	}
}

// The move endpoint widens the allowed destinations to template pools
// only for base images. An ordinary VM disk must not slip through and
// end up filed on the golden-image shelf.
func TestIsBaseImageFilename(t *testing.T) {
	yes := []string{
		"base-alpine-3.24.qcow2",
		"base-ubuntu-24.04.qcow2",
		"base-.qcow2",
	}
	no := []string{
		"alpine-3.24.qcow2", // no prefix: an operator's own volume
		"base-alpine.iso",   // an ISO is never a base image
		"base-alpine",       // no extension
		"vm1.qcow2",
		"",
	}
	for _, n := range yes {
		if !isBaseImageFilename(n) {
			t.Errorf("isBaseImageFilename(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if isBaseImageFilename(n) {
			t.Errorf("isBaseImageFilename(%q) = true, want false", n)
		}
	}
}
