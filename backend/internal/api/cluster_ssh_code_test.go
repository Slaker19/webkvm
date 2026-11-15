package api

import (
	"errors"
	"fmt"
	"testing"
)

func TestNodeErrorCodeHandlesForeignErrors(t *testing.T) {
	got := nodeErrorCode(fmt.Errorf("wrapped: %w", errors.New("plain failure")))
	if got == "" {
		t.Fatal("nodeErrorCode returned an empty code")
	}
}
