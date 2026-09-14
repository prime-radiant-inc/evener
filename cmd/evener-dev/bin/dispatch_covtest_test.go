package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	devcmd "primeradiant.com/evener/cmd/evener-dev"
)

const devHelp = `Usage: evener-dev <subcommand> [flags]

Subcommands:
  dev                        Dev tooling (agent-shards, bounded-list, covstmt, list-build-flags, module-lint)
  module-lint              Run golangci-lint across workspace modules in parallel waves
  agent-shards             Run agent test shards in parallel
  bounded-list             Run a command under a time bound, stopping its process group
  list-build-flags       Print the flags a package enumeration needs from a go test invocation
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
		if err := r.Close(); err != nil {
			t.Fatalf("closing the reader: %v", err)
		}
		return code, string(out)
	}

	// Each alias, run both ways on one fixed invocation, with the exit code
	// and the output compared. The dev family writes the process's own
	// streams, so those are what this captures.
	t.Setenv("DISPATCH_PRINTING_HELPER", "1")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "bounded-list",
			// The command is this test binary re-executed, the repo's
			// helper-process idiom, so nothing ambient is involved.
			args: []string{"-timeout", "30s", "-attempts", "1", "--", os.Args[0], "-test.run=TestDispatchPrintingHelper$"},
			want: helperLine,
		},
		{
			name: "list-build-flags",
			args: []string{"--", "-race", "-short"},
			want: "-race\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefixedCode, prefixedOut := run(append([]string{"dev", tc.name}, tc.args...)...)
			bareCode, bareOut := run(append([]string{tc.name}, tc.args...)...)
			if prefixedCode != 0 || bareCode != 0 {
				t.Fatalf("exit codes = %d (dev %s) and %d (%s), want 0 from both", prefixedCode, tc.name, bareCode, tc.name)
			}
			if !strings.Contains(prefixedOut, tc.want) || bareOut != prefixedOut {
				t.Fatalf("stdout = %q (dev %s) and %q (%s), want %q from both", prefixedOut, tc.name, bareOut, tc.name, tc.want)
			}
		})
	}

	// The other two aliases are not run: module-lint would lint the repository
	// and agent-shards would shard it, both of which this test would then be
	// waiting for. What matters is the resolution, and that is data: every
	// aliased name is a dev subcommand, so both spellings reach the same
	// handler.
	dev := map[string]bool{}
	for _, name := range devcmd.SubcommandNames() {
		dev[name] = true
	}
	for name := range aliasedDevSubcommands {
		if !dev[name] {
			t.Errorf("%s aliases nothing: the dev package has no such subcommand, so the bare spelling reaches a different answer from the prefixed one", name)
		}
	}
	for _, name := range []string{"bounded-list", "list-build-flags", "module-lint", "agent-shards"} {
		if !aliasedDevSubcommands[name] {
			t.Errorf("%s is documented in the usage text but does not alias", name)
		}
	}
}

// helperLine is what the helper prints, and what both spellings of the alias
// have to deliver unchanged.
const helperLine = "from-the-subcommand"

// TestDispatchPrintingHelper is not a test: it is the command the alias
// comparison runs, re-executed from this test binary so nothing ambient is
// involved.
func TestDispatchPrintingHelper(t *testing.T) {
	if os.Getenv("DISPATCH_PRINTING_HELPER") == "" {
		t.Skip("helper process entry point; runs only under the alias comparison")
	}
	fmt.Println(helperLine)
}
