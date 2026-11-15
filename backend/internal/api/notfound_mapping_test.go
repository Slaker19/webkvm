package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotFoundErrorRecognition(t *testing.T) {
	for _, err := range []error{
		errors.New("domain not found: virError(Code=42)"),
		errors.New("no domain with matching name"),
		errors.New("instance not found"),
		errors.New("storage pool not found: no storage pool with matching name"),
		errors.New("no storage volume with matching name"),
	} {
		if !isNotFoundError(err) {
			t.Errorf("not recognized: %v", err)
		}
	}
	for _, err := range []error{
		errors.New("permission denied"),
		errors.New("connection refused"),
	} {
		if isNotFoundError(err) {
			t.Errorf("wrongly recognized as not found: %v", err)
		}
	}
}

func TestVMActionErrMapsNotFoundTo404(t *testing.T) {
	rec := httptest.NewRecorder()
	h := &Handler{}
	h.vmActionErr(rec, errors.New("domain not found: no domain with matching name"), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
