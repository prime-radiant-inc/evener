package main

import (
	"bytes"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestLaunchCheckDispatchesFromTopLevel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, label, err := dispatchCLICommand([]string{
		"launch-check",
		"--protocol", appwire.ProtocolVersion,
		"--model", "openrouter/free",
		"--json",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("dispatchCLICommand: %v", err)
	}
	if !handled {
		t.Fatal("dispatchCLICommand handled=false, want true")
	}
	if label != "serf launch-check" {
		t.Fatalf("label=%q, want serf launch-check", label)
	}
}
