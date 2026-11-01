package contextmgr

import "testing"

// H2: After L1 masks shell results, L3 checkpoint's parseExitCode must still
// extract exit codes from the masked format [shell: "cmd" → exit N].
func TestParseExitCode_MaskedFormat(t *testing.T) {
	// After L1 masking, shell results look like: [shell: "go test" → exit 0]
	masked := `[shell: "go test" → exit 0]`
	got := parseExitCode(masked)
	if got != "0" {
		t.Fatalf("parseExitCode on masked format: got %q, want %q", got, "0")
	}

	masked2 := `[shell: "go test" → exit 1]`
	got2 := parseExitCode(masked2)
	if got2 != "1" {
		t.Fatalf("parseExitCode on masked format: got %q, want %q", got2, "1")
	}
}
