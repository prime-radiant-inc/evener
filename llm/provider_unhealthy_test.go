package llm

import (
	"errors"
	"testing"
	"time"
)

func TestProviderUnhealthyError_ErrorAndUnwrap(t *testing.T) {
	last := errors.New("boom")
	e := &ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 12 * time.Second, LastErr: last}
	want := "provider unhealthy after 4 stream failures (12s): boom"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(e, last) {
		t.Fatal("errors.Is(e, last) = false, want true")
	}
}
