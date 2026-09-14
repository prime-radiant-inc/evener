package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

const devHelp = `Usage: evener-dev <subcommand> [flags]

Subcommands:
  dev                        Dev tooling (agent-shards, bounded-list, covstmt, list-build-flags, module-lint)
  module-lint              Run golangci-lint across workspace modules in parallel waves
  agent-shards             Run agent test shards in parallel
  fuzz-harvest             Harvest fuzz seed corpora from recorded traffic
  fuzzcov                  Static fuzz gap gate
  fuzzregistry             Audit the fuzz target registry
  internalcheck            Check public packages don't leak internal types
  tomlcheck                Enforce TOML wire-format naming conventions
  transcript-v2-upgrade  Convert legacy transcript v1 files to v2
`

func TestDispatchUsageOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "missing subcommand", wantCode: 2, wantStderr: devHelp},
		{name: "short help", args: []string{"-h"}, wantCode: 0, wantStdout: devHelp},
		{name: "long help", args: []string{"--help"}, wantCode: 0, wantStdout: devHelp},
		{name: "help subcommand", args: []string{"help"}, wantCode: 0, wantStdout: devHelp},
		{
			name:       "unknown subcommand",
			args:       []string{"nonexistent-cmd"},
			wantCode:   2,
			wantStderr: "evener-dev: unknown subcommand \"nonexistent-cmd\"\n" + devHelp,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := dispatch(tc.args, nil, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("dispatch(%q) code = %d, want %d", tc.args, code, tc.wantCode)
			}
			if got := stdout.String(); got != tc.wantStdout {
				t.Fatalf("dispatch(%q) stdout = %q, want %q", tc.args, got, tc.wantStdout)
			}
			if got := stderr.String(); got != tc.wantStderr {
				t.Fatalf("dispatch(%q) stderr = %q, want %q", tc.args, got, tc.wantStderr)
			}
		})
	}
}

// TestDispatchAliasesReachTheSameSubcommand covers the top-level aliases: the
// usage text spells bounded-list without the dev prefix, so both spellings
// have to reach the same subcommand and do the same thing.
//
// The subcommands write to the process's own streams, as the whole dev family
// does, so the comparison captures os.Stdout around one fixed invocation and
// checks that the two spellings agree on the exit code and on what the command
// printed.
func TestDispatchAliasesReachTheSameSubcommand(t *testing.T) {
	run := func(args ...string) (int, string) {
		t.Helper()
		saved := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdout = w
		defer func() { os.Stdout = saved }()
		var discard strings.Builder
		code := dispatch(args, nil, &discard, &discard)
		if err := w.Close(); err != nil {
			t.Fatalf("closing the pipe: %v", err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("reading the pipe: %v", err)
		}
		return code, string(out)
	}

	// One fixed invocation, run both ways: a command that prints and exits 0.
	prefixed, prefixedOut := run("dev", "bounded-list", "-timeout", "10s", "-attempts", "1", "--", "echo", "from-the-subcommand")
	bare, bareOut := run("bounded-list", "-timeout", "10s", "-attempts", "1", "--", "echo", "from-the-subcommand")
	if prefixed != 0 || bare != 0 {
		t.Fatalf("exit codes = %d (dev bounded-list) and %d (bounded-list), want 0 from both", prefixed, bare)
	}
	if prefixedOut != "from-the-subcommand\n" || bareOut != prefixedOut {
		t.Fatalf("stdout = %q (dev bounded-list) and %q (bounded-list), want the same line from both", prefixedOut, bareOut)
	}
	// The other aliases are here because the same line dispatches them.
	for _, args := range [][]string{{"module-lint", "-h"}, {"agent-shards", "-h"}} {
		var stdout, stderr strings.Builder
		dispatch(args, nil, &stdout, &stderr)
		if strings.Contains(stderr.String(), "unknown subcommand") {
			t.Fatalf("dispatch(%q) = unknown subcommand; the alias is missing from the dispatch", args)
		}
	}
}
