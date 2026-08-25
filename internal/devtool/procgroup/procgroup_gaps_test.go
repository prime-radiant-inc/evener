package procgroup

import "testing"

// TestExitCodeNilState covers the nil state branch (line 68) that
// returns 1 without dereferencing.
func TestExitCodeNilState(t *testing.T) {
	if got := ExitCode(nil); got != 1 {
		t.Fatalf("ExitCode(nil) = %d, want 1", got)
	}
}
