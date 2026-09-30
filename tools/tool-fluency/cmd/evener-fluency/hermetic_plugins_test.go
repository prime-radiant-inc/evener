package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// fakeEvenerWithHelp writes an executable evener stand-in whose output, for
// every invocation, is help.
func fakeEvenerWithHelp(t *testing.T, help string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-evener")
	mustWrite(t, bin, "#!/bin/sh\nprintf '%s\\n' '"+help+"'\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestEvenerSupportsEnabledPluginsReadsHelp drives the real check: a binary
// whose --help lists --enabled-plugins supports it, one whose help does not
// predates it.
func TestEvenerSupportsEnabledPluginsReadsHelp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		help string
		want bool
	}{
		{"  --enabled-plugins <value>   comma-separated plugin names", true},
		{"  --model <value>   provider/model", false},
	} {
		got, err := evenerHelpListsEnabledPlugins(fakeEvenerWithHelp(t, tc.help))
		if err != nil {
			t.Fatalf("help %q: %v", tc.help, err)
		}
		if got != tc.want {
			t.Errorf("help %q: supports = %v, want %v", tc.help, got, tc.want)
		}
	}
}

// TestHermeticRunRefusesEvenerThatPredatesEnabledPlugins: a hermetic CLI run
// against an evener without --enabled-plugins must refuse before any probe
// runs, naming the way out, instead of recording every cell as failed (which
// a version comparison would read as a regression). --inherit-operator-env
// still runs it. Not parallel: it restores the check TestMain stubs and sets
// process env.
func TestHermeticRunRefusesEvenerThatPredatesEnabledPlugins(t *testing.T) {
	stub := evenerSupportsEnabledPlugins
	t.Cleanup(func() { evenerSupportsEnabledPlugins = stub })
	evenerSupportsEnabledPlugins = evenerHelpListsEnabledPlugins
	t.Setenv(envvars.EVENERNoUserSkills.Name, "1")

	dir := t.TempDir()
	probes := filepath.Join(dir, "probes")
	mustWrite(t, filepath.Join(probes, "probe.yaml"), "schema: 1\nid: local\nprompt: hello\nexpect:\n  final_contains: [ok]\n")
	old := fakeEvenerWithHelp(t, "ok")

	out := filepath.Join(dir, "hermetic")
	err := runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", probes, "--out", out, "--evener-bin", old})
	if err == nil || !strings.Contains(err.Error(), "--inherit-operator-env") {
		t.Fatalf("hermetic run on an old evener: err = %v, want a refusal naming --inherit-operator-env", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "results.jsonl")); statErr == nil {
		t.Fatal("refused run wrote results.jsonl")
	}

	if err := runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", probes, "--out", filepath.Join(dir, "inherit"), "--evener-bin", old, "--inherit-operator-env"}); err != nil {
		t.Fatalf("--inherit-operator-env run on an old evener: %v", err)
	}
}
