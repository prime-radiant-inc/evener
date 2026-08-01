package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/plugins"
)

type fuzzErrorWriter struct{}

func (fuzzErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func FuzzRun(f *testing.F) {
	// The seeds run each subcommand with NO --state-dir, so a subcommand that
	// reads the DEFAULT root reads the machine's real one. "turnids" is left out
	// deliberately for that reason: it sweeps every session under the state root,
	// and seeding it would point the fuzzer at the developer's own ~/.local/state.
	for _, subcommand := range []string{"", "help", "--help", "locate", "transcript", "apilog", "jobs", "watches", "tree", "plugins", "unknown"} {
		f.Add(subcommand, false)
	}
	f.Add("unknown", true)
	// "plugins" is the one seeded subcommand that needs no selector, so it runs
	// its health check for real against the DEFAULT store root: the developer's
	// own ~/.config/serf/plugins. Pinning the config home lands that read in a
	// fixture instead, which keeps the target off the machine's live state and
	// makes its result reproducible. The assertion in the body fails the run if
	// the pin is ever lost.
	fixture := f.TempDir()
	f.Setenv(envvars.XDGConfigHome.Name, fixture)
	f.Fuzz(func(t *testing.T, subcommand string, failWrites bool) {
		if root := plugins.DefaultRoot(); !strings.HasPrefix(root, fixture+string(os.PathSeparator)) {
			t.Fatalf("plugin store root %q is outside the fixture %q", root, fixture)
		}
		if len(subcommand) > 256 {
			return
		}
		var stdout, stderr bytes.Buffer
		if failWrites {
			_ = run([]string{subcommand}, fuzzErrorWriter{}, fuzzErrorWriter{})
			return
		}
		_ = run([]string{subcommand}, &stdout, &stderr)
	})
}
