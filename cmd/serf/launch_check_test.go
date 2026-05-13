package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/appwire"
	_ "primeradiant.com/serf/llm/providers/openrouter"
)

func TestLaunchCheckReportsProtocolAndValidatedModel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "openrouter/free",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
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

func TestLaunchCheckRejectsUnsupportedProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "missing/free",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}

func TestLaunchCheckRejectsProtocolMismatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runLaunchCheck([]string{
		"--protocol", "serf-appwire-v0",
		"--model", "openrouter/free",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected protocol mismatch")
	}
	if !strings.Contains(err.Error(), "unsupported appwire protocol") {
		t.Fatalf("error=%v", err)
	}
}

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
