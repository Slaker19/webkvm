package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

type mockStorageBackend struct {
	compute.Backend
	created []models.CreatePoolRequest
}

func (m *mockStorageBackend) CreateStoragePool(_ context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	m.created = append(m.created, req)
	return models.StoragePool{
		Name:         req.Name,
		Type:         req.Type,
		Path:         req.Path,
		Purpose:      req.Purpose,
		SourceHost:   req.SourceHost,
		SourcePort:   req.SourcePort,
		SourceDevice: req.SourceDevice,
		State:        "active",
	}, nil
}

func (m *mockStorageBackend) ListStoragePools() ([]models.StoragePool, error) {
	return nil, nil
}

func TestCreatePoolISCSI(t *testing.T) {
	tmp := t.TempDir()
	auditLogger, err := audit.New(tmp + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockStorageBackend{}
	h := &Handler{
		compute: mock,
		cfg:     &config.Config{DataDir: tmp},
		audit:   auditLogger,
	}

	tests := []struct {
		name       string
		body       models.CreatePoolRequest
		wantStatus int
	}{
		{
			name: "valid iscsi pool with defaults",
			body: models.CreatePoolRequest{
				Name:         "san-pool",
				Type:         "iscsi",
				SourceHost:   "10.0.0.100",
				SourceDevice: "iqn.2026-01.com.storage:san.lun1",
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "valid iscsi pool with custom port and chap auth",
			body: models.CreatePoolRequest{
				Name:           "san-chap",
				Type:           "iscsi",
				SourceHost:     "10.0.0.100",
				SourcePort:     3261,
				SourceDevice:   "iqn.2026-01.com.storage:san.lun2",
				SourceUsername: "admin",
				SourcePassword: "secretpassword123",
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "missing source host",
			body: models.CreatePoolRequest{
				Name:         "san-bad",
				Type:         "iscsi",
				SourceDevice: "iqn.2026-01.com.storage:san.lun1",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing source device / target IQN",
			body: models.CreatePoolRequest{
				Name:       "san-bad",
				Type:       "iscsi",
				SourceHost: "10.0.0.100",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "mismatched CHAP credentials (user without password)",
			body: models.CreatePoolRequest{
				Name:           "san-bad",
				Type:           "iscsi",
				SourceHost:     "10.0.0.100",
				SourceDevice:   "iqn.2026-01.com.storage:san.lun1",
				SourceUsername: "admin",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/storage/pools", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.CreatePool(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("CreatePool() status = %d (%s), want %d", w.Code, w.Body.String(), tc.wantStatus)
			}
		})
	}

	if len(mock.created) != 2 {
		t.Fatalf("expected 2 pools created, got %d", len(mock.created))
	}
	if mock.created[0].Path != "/dev/disk/by-path" {
		t.Errorf("expected default iscsi path /dev/disk/by-path, got %q", mock.created[0].Path)
	}
}
