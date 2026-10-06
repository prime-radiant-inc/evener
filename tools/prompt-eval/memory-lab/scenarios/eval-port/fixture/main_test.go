package main

import "testing"

func TestNormalizeVisible(t *testing.T) {
	if got := normalize("  visible alpha \n"); got != "visible alpha" {
		t.Fatalf("normalize=%q", got)
	}
}
