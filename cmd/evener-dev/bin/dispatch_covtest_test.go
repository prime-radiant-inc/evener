package main

import (
	"slices"
	"strings"
	"testing"

	devcmd "primeradiant.com/evener/cmd/evener-dev"
)

// topLevelSubcommands are the commands dispatch routes directly, beside
// `dev`'s own registry.
var topLevelSubcommands = []string{
	"dev", "module-lint", "agent-shards", "hub-shards", "cli-shards", "list-build-flags",
	"fuzz-harvest", "fuzzcov", "fuzzregistry", "internalcheck", "tomlcheck", "transcript-v2-upgrade",
}

// assertUsageNamesEverySubcommand checks that the usage offers every command
// dispatch routes and every registered `dev` subcommand. It reads the names,
// which are the command line's contract, not the descriptions.
func assertUsageNamesEverySubcommand(t *testing.T, usage string) {
	t.Helper()
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(usage, func(r rune) bool { return r == ' ' || r == '\n' || r == ',' || r == '(' || r == ')' }) {
		words[w] = true
	}
	for _, name := range append(slices.Clone(topLevelSubcommands), devcmd.Subcommands()...) {
		if !words[name] {
			t.Errorf("usage does not offer %q:\n%s", name, usage)
		}
	}
}

func TestDispatchUsageOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		// usageOnStdout is where the usage goes: stdout when it was asked
		// for, stderr when the arguments were wrong.
		usageOnStdout bool
	}{
		{name: "missing subcommand", wantCode: 2},
		{name: "short help", args: []string{"-h"}, wantCode: 0, usageOnStdout: true},
		{name: "long help", args: []string{"--help"}, wantCode: 0, usageOnStdout: true},
		{name: "help subcommand", args: []string{"help"}, wantCode: 0, usageOnStdout: true},
		{name: "unknown subcommand", args: []string{"nonexistent-cmd"}, wantCode: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := dispatch(tc.args, nil, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("dispatch(%q) code = %d, want %d", tc.args, code, tc.wantCode)
			}
			usage, other := stderr.String(), stdout.String()
			if tc.usageOnStdout {
				usage, other = other, usage
			}
			if other != "" {
				t.Fatalf("dispatch(%q) wrote to the other stream: %q", tc.args, other)
			}
			assertUsageNamesEverySubcommand(t, usage)
			if len(tc.args) > 0 && !tc.usageOnStdout && !strings.Contains(usage, tc.args[0]) {
				t.Errorf("dispatch(%q) does not name the unknown subcommand: %q", tc.args, usage)
			}
		})
	}
}
