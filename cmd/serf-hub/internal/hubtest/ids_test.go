package hubtest

import (
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

// The point of these helpers is that their output survives the validation
// PastIndex.Rebuild applies, so each case asserts against the real validator
// rather than against a hand-copied description of the encoding.

func TestSessionIDIsValidAndUnique(t *testing.T) {
	first := SessionID(t)
	if err := identifier.ValidateSessionID(first); err != nil {
		t.Fatalf("SessionID() = %q, ValidateSessionID: %v", first, err)
	}
	if second := SessionID(t); second == first {
		t.Fatalf("SessionID() returned %q twice; ids must be distinct", first)
	}
}

func TestProjectIDIsValid(t *testing.T) {
	cases := []struct {
		name     string
		readable string
		want     string
	}{
		{"plain", "alpha", "alpha-0123456789"},
		{"spaces fold to hyphens", "my project", "my-project-0123456789"},
		{"path separators fold", "/Users/jesse/work", "Users-jesse-work-0123456789"},
		{"runs collapse", "a///b", "a-b-0123456789"},
		{"empty falls back", "", "project-0123456789"},
		{"punctuation only falls back", "///", "project-0123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectID(t, tc.readable)
			if got != tc.want {
				t.Fatalf("ProjectID(%q) = %q, want %q", tc.readable, got, tc.want)
			}
			if err := identifier.ValidateProjectID(got); err != nil {
				t.Fatalf("ProjectID(%q) = %q, ValidateProjectID: %v", tc.readable, got, err)
			}
		})
	}
}

// A readable portion longer than the id's 80-byte ceiling must still yield a
// valid id rather than tripping ProjectID's own validation check.
func TestProjectIDTruncatesOverlongReadable(t *testing.T) {
	got := ProjectID(t, strings.Repeat("x", 200))
	if err := identifier.ValidateProjectID(got); err != nil {
		t.Fatalf("ProjectID(200 bytes) = %q, ValidateProjectID: %v", got, err)
	}
	if len(got) > 80 {
		t.Fatalf("ProjectID(200 bytes) = %q (%d bytes), want <= 80", got, len(got))
	}
}
