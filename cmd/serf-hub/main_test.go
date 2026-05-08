package main

import (
	"testing"
)

func TestVersionString_NotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("Version should not be empty")
	}
}
