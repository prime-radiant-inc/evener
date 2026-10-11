package dev

import (
	"fmt"
	"os"
	"testing"
)

// evenerDevBinDir holds the evener-dev binary buildEvenerDev compiles once for
// the whole package run; TestMain owns it so the binary outlives any one test
// and is removed when the run ends.
var evenerDevBinDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "evener-dev-test-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "evener-dev TestMain: %v\n", err)
		os.Exit(1)
	}
	evenerDevBinDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
