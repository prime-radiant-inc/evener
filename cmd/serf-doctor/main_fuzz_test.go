package main

import (
	"bytes"
	"errors"
	"testing"
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
	f.Fuzz(func(t *testing.T, subcommand string, failWrites bool) {
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
