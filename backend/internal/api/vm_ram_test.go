package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func i64(v int64) *int64 { return &v }

// 8.29 #4: one RAM rule (mirrors frontend/src/lib/utils/ramValidation.js).
func TestCheckRAMRule(t *testing.T) {
	cases := []struct {
		name            string
		ram             *int64
		minRAM, current int64
		allow           bool
		want            string
	}{
		{"512 ok", i64(512), 0, 0, false, ""},
		{"256 refused", i64(256), 0, 0, false, "ram_below_minimum"},
		{"0 refused when set", i64(0), 0, 0, false, "ram_below_minimum"},
		{"256 with override", i64(256), 0, 0, true, ""},
		{"128 with override", i64(128), 0, 0, true, ""},
		{"64 even with override", i64(64), 0, 0, true, "ram_below_floor"},
		{"floor above ram", i64(1024), 2048, 0, false, "min_ram_above_ram"},
		{"floor equal ram", i64(1024), 1024, 0, false, ""},
		{"floor above current ram", nil, 512, 256, false, "min_ram_above_ram"},
		{"floor only, current unknown", nil, 512, 0, false, ""},
		{"nothing set", nil, 0, 0, false, ""},
	}
	for _, c := range cases {
		got := ""
		if e := checkRAMRule(c.ram, c.minRAM, c.current, c.allow); e != nil {
			got = e.code
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestUpdateVMRejectsLowRAMWithTypedError(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct{ body, code string }{
		{`{"ram_mb":256}`, "ram_below_minimum"},
		{`{"ram_mb":64,"allow_low_ram":true}`, "ram_below_floor"},
		{`{"ram_mb":1024,"min_ram_mb":2048}`, "min_ram_above_ram"},
	} {
		r := httptest.NewRequest(http.MethodPatch, "/api/vms/v1", strings.NewReader(tc.body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "v1")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		r.Header.Set("X-Role", "admin")
		w := httptest.NewRecorder()
		h.UpdateVM(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400 (%s)", tc.body, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out["code"] != tc.code || out["min_ram_mb"] != float64(512) {
			t.Errorf("%s: body %v, want code %s", tc.body, out, tc.code)
		}
	}
}

func TestCreateVMRejectsLowRAMWithTypedError(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/api/vms", strings.NewReader(`{"name":"t1","ram_mb":256}`))
	r.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()
	h.CreateVM(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"ram_below_minimum"`) {
		t.Fatalf("status %d body %s, want 400 ram_below_minimum", w.Code, w.Body.String())
	}
}
