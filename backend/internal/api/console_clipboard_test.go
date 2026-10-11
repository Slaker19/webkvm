package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func renderConsolePage(t *testing.T) (*httptest.ResponseRecorder, string) {
	t.Helper()
	h := &Handler{}
	r := chi.NewRouter()
	r.Get("/console/{id}", h.ConsolePage)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/console/vm1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	return rr, rr.Body.String()
}

// #19: the noVNC clipboard event must not write navigator.clipboard by
// itself; only a click on the banner's Copy button may.
func TestConsolePage_GuestClipboardNeedsUserClick(t *testing.T) {
	_, body := renderConsolePage(t)

	if !strings.Contains(body, `id="clip-banner"`) || !strings.Contains(body, `id="clip-banner-copy"`) {
		t.Fatal("copy banner missing")
	}
	// syncClipToHost (the 'clipboard' event handler) must not touch the clipboard.
	start := strings.Index(body, "function syncClipToHost")
	if start < 0 {
		t.Fatal("syncClipToHost missing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot delimit syncClipToHost")
	}
	if strings.Contains(body[start:start+end], "navigator.clipboard") {
		t.Fatal("syncClipToHost must not write navigator.clipboard automatically")
	}
	// The only clipboard write is inside the Copy button's onclick.
	if n := strings.Count(body, "navigator.clipboard.writeText"); n != 1 {
		t.Fatalf("expected exactly 1 navigator.clipboard.writeText (the click handler), got %d", n)
	}
	cb := strings.Index(body, "accept.onclick")
	w := strings.Index(body, "navigator.clipboard.writeText")
	if cb < 0 || w < cb {
		t.Fatal("clipboard write must live inside accept.onclick")
	}
}
