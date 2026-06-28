package main

import (
	"bytes"
	"encoding/json"
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
	// Verify that RunLaunchCheck actually wrote a valid launch contract to stdout.
	// A no-op implementation returning nil without output would pass the routing
	// checks above but fail here.
	var out struct {
		Protocol string `json:"protocol"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Protocol != appwire.ProtocolVersion || out.Provider != "openrouter" || out.Model != "free" {
		t.Fatalf("launch check output=%+v", out)
	}
}
