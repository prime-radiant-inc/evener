package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCliProbeArgsAddsAskResponderWhenPersonSet: a task with a person: block
// gets --ask-responder pointed at this binary's own respond subcommand, and
// the person's brief is written to a file the responder command names.
func TestCliProbeArgsAddsAskResponderWhenPersonSet(t *testing.T) {
	oldSelf := evenerFluencyExecutablePath
	t.Cleanup(func() { evenerFluencyExecutablePath = oldSelf })
	evenerFluencyExecutablePath = func() (string, error) { return "/path/to/evener-fluency", nil }

	dir := t.TempDir()
	res := probeResult{WorkDir: filepath.Join(dir, "work"), StateDir: filepath.Join(dir, "state")}
	probe := probeFile{
		Prompt: "ask the stakeholder",
		Person: &personSpec{Brief: "You are Alex, the product owner. Ship date is fixed."},
	}
	cfg := runConfig{model: "openai/m", fastCheapModel: "openai/cheap"}

	args, err := cliProbeArgs(cfg, probe, res)
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}

	idx := -1
	for i, a := range args {
		if a == "--ask-responder" {
			idx = i
		}
	}
	if idx == -1 || idx+1 >= len(args) {
		t.Fatalf("args = %#v, want --ask-responder followed by a command", args)
	}
	command := args[idx+1]
	for _, want := range []string{"/path/to/evener-fluency", "respond", "--brief-file", "--model", "openai/cheap", "--log"} {
		if !strings.Contains(command, want) {
			t.Errorf("ask-responder command = %q, want it to contain %q", command, want)
		}
	}

	// The brief file the command names must actually hold the brief text.
	briefPath := extractFlagValue(t, command, "--brief-file")
	data, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read brief file %q: %v", briefPath, err)
	}
	if string(data) != probe.Person.Brief {
		t.Errorf("brief file content = %q, want %q", data, probe.Person.Brief)
	}
}

// TestCliProbeArgsDefaultsPersonModelToFastCheapModel: an unset person.model
// falls back to the run's --fast-cheap-model.
func TestCliProbeArgsDefaultsPersonModelToFastCheapModel(t *testing.T) {
	oldSelf := evenerFluencyExecutablePath
	t.Cleanup(func() { evenerFluencyExecutablePath = oldSelf })
	evenerFluencyExecutablePath = func() (string, error) { return "/path/to/evener-fluency", nil }

	dir := t.TempDir()
	res := probeResult{WorkDir: filepath.Join(dir, "work"), StateDir: filepath.Join(dir, "state")}
	probe := probeFile{Prompt: "p", Person: &personSpec{Brief: "brief", Model: "openai/specific"}}
	cfg := runConfig{model: "openai/m", fastCheapModel: "openai/cheap"}

	args, err := cliProbeArgs(cfg, probe, res)
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}
	command := findFlagValue(t, args, "--ask-responder")
	if got := extractFlagValue(t, command, "--model"); got != "openai/specific" {
		t.Errorf("command model = %q, want the person's own model, not the fast-cheap default", got)
	}
}

// TestCliProbeArgsOmitsAskResponderWithoutPerson: a task with no person:
// block runs exactly as it did before this feature.
func TestCliProbeArgsOmitsAskResponderWithoutPerson(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{WorkDir: filepath.Join(dir, "work"), StateDir: filepath.Join(dir, "state")}
	probe := probeFile{Prompt: "p"}
	cfg := runConfig{model: "openai/m"}

	args, err := cliProbeArgs(cfg, probe, res)
	if err != nil {
		t.Fatalf("cliProbeArgs: %v", err)
	}
	for _, a := range args {
		if a == "--ask-responder" {
			t.Fatalf("args = %#v, want no --ask-responder with no person: block", args)
		}
	}
}

// TestPersonSpecDecodesStrictly: the person: block decodes with the same
// strictness as every other probe field — an unknown field is a load-time
// error, not a silently ignored typo.
func TestPersonSpecDecodesStrictly(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "task.yaml"), "schema: 1\nid: prose.x\nprompt: p\nperson:\n  brief: hi\n  bogus: true\n")
	if _, err := loadProbes(dir, "all"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("loadProbes = %v, want an error naming the unknown person field", err)
	}
}

// TestPersonSpecDecodesBriefAndModel: the happy path — a valid person:
// block decodes brief and model.
func TestPersonSpecDecodesBriefAndModel(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "task.yaml"), "schema: 1\nid: prose.x\nprompt: p\nperson:\n  brief: You are Alex.\n  model: openai/gpt-5-mini\n")
	probes, err := loadProbes(dir, "all")
	if err != nil {
		t.Fatalf("loadProbes: %v", err)
	}
	if len(probes) != 1 || probes[0].Person == nil {
		t.Fatalf("probes = %+v, want one probe with a person", probes)
	}
	if probes[0].Person.Brief != "You are Alex." || probes[0].Person.Model != "openai/gpt-5-mini" {
		t.Fatalf("person = %+v", probes[0].Person)
	}
}

// findFlagValue returns the value that follows the given flag name in args.
func findFlagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("args = %#v, want %q", args, flag)
	return ""
}

// extractFlagValue pulls the value that follows "--name" inside a shell
// command string (as opposed to an argv slice).
func extractFlagValue(t *testing.T, command, flag string) string {
	t.Helper()
	fields := strings.Fields(command)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return strings.Trim(fields[i+1], "'")
		}
	}
	t.Fatalf("command = %q, want %q", command, flag)
	return ""
}
