package main

import (
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
// usage text and docs/developing-evener/testing.md both spell bounded-list
// without the dev prefix, so both spellings have to be recognised here.
//
// The subcommand itself writes to the process's own streams, as the other dev
// subcommands do, so what this can see is that neither spelling came back as
// an unknown subcommand -- which is exactly what the missing alias did.
func TestDispatchAliasesReachTheSameSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"bounded-list"},
		{"dev", "bounded-list"},
		{"module-lint", "-h"},
		{"agent-shards", "-h"},
	} {
		var stdout, stderr strings.Builder
		dispatch(args, nil, &stdout, &stderr)
		if strings.Contains(stderr.String(), "unknown subcommand") {
			t.Fatalf("dispatch(%q) = unknown subcommand; the alias is missing from the dispatch", args)
		}
	}
}
