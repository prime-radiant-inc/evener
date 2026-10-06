package textutil

import "testing"

func TestInitials(t *testing.T) {
	if got := Initials("portable network graphics"); got != "PNG" {
		t.Fatalf("Initials = %q", got)
	}
}
