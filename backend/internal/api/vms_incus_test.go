package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

type mockIncusBackend struct {
	compute.Backend
}

func (m *mockIncusBackend) ListIncusImages() ([]models.IncusImageItem, error) {
	return []models.IncusImageItem{
		{
			Ref:         "ubuntu:24.04",
			Label:       "Ubuntu 24.04 LTS (Noble)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "LTS más reciente de Ubuntu.",
			Arch:        "x86_64",
			Badge:       "LTS",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:alpine/3.21",
			Label:       "Alpine Linux 3.21",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Ultraligera.",
			Arch:        "x86_64",
			Badge:       "5 MB",
			IsLocal:     true,
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
	}, nil
}

func TestListIncusImages_Endpoint(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			IncusEnabled: true,
		},
		compute: &mockIncusBackend{},
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/vms/incus-images", nil)
	h.ListIncusImages(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}

	var resp models.IncusImagesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !resp.IncusEnabled {
		t.Errorf("expected IncusEnabled to be true")
	}
	if len(resp.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(resp.Images))
	}
	if resp.Images[0].Ref != "ubuntu:24.04" || resp.Images[1].Ref != "images:alpine/3.21" {
		t.Errorf("unexpected images in response: %+v", resp.Images)
	}
	if !resp.Images[1].IsLocal {
		t.Errorf("expected alpine to have IsLocal = true")
	}
}
