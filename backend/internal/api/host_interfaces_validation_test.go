package api

import (
	"strings"
	"testing"

	"webkvm/internal/models"
)

func TestValidateHostInterfaceChange(t *testing.T) {
	ptr := func(s string) *string { return &s }

	cases := []struct {
		name    string
		req     models.ConfigureHostInterfaceRequest
		wantErr string
		check   func(t *testing.T, req models.ConfigureHostInterfaceRequest)
	}{
		{
			name: "no changes",
			req:  models.ConfigureHostInterfaceRequest{},
		},
		{
			name: "valid IPv4 is trimmed",
			req:  models.ConfigureHostInterfaceRequest{IPv4: ptr(" 192.168.50.50/24 ")},
			check: func(t *testing.T, req models.ConfigureHostInterfaceRequest) {
				if req.IPv4 == nil || *req.IPv4 != "192.168.50.50/24" {
					t.Fatalf("IPv4 = %v, want trimmed 192.168.50.50/24", req.IPv4)
				}
			},
		},
		{
			name: "empty IPv4 means flush",
			req:  models.ConfigureHostInterfaceRequest{IPv4: ptr("  ")},
			check: func(t *testing.T, req models.ConfigureHostInterfaceRequest) {
				if req.IPv4 == nil || *req.IPv4 != "" {
					t.Fatalf("IPv4 = %v, want empty", req.IPv4)
				}
			},
		},
		{
			name:    "invalid IPv4 is rejected",
			req:     models.ConfigureHostInterfaceRequest{IPv4: ptr("192.168.50.50")},
			wantErr: "invalid CIDR",
		},
		{
			name: "valid gateway is trimmed",
			req:  models.ConfigureHostInterfaceRequest{Gateway: ptr(" 192.168.1.1 ")},
			check: func(t *testing.T, req models.ConfigureHostInterfaceRequest) {
				if req.Gateway == nil || *req.Gateway != "192.168.1.1" {
					t.Fatalf("Gateway = %v, want trimmed 192.168.1.1", req.Gateway)
				}
			},
		},
		{
			name:    "invalid gateway is rejected",
			req:     models.ConfigureHostInterfaceRequest{Gateway: ptr("not-an-ip")},
			wantErr: "invalid gateway IP",
		},
		{
			name: "state is normalized",
			req:  models.ConfigureHostInterfaceRequest{State: ptr(" UP ")},
			check: func(t *testing.T, req models.ConfigureHostInterfaceRequest) {
				if req.State == nil || *req.State != "up" {
					t.Fatalf("State = %v, want up", req.State)
				}
			},
		},
		{
			name:    "invalid state is rejected",
			req:     models.ConfigureHostInterfaceRequest{State: ptr("sideways")},
			wantErr: "state must be",
		},
		{
			name: "invalid field wins over a valid one",
			req: models.ConfigureHostInterfaceRequest{
				Isolate: true,
				IPv4:    ptr("192.168.50.50/24"),
				State:   ptr("sideways"),
			},
			wantErr: "state must be",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateHostInterfaceChange(&c.req)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.check != nil {
				c.check(t, c.req)
			}
		})
	}
}
