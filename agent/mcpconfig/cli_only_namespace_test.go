package mcpconfig

import "testing"

// TestCLIOnlyMatchesServerNameNamespaces pins the fallback match against raw
// string equality. MCP registration namespaces a server's tools as
// "<server>__<tool>" after replacing hyphens with underscores, so a project
// server "team-tools" and a CLI server "team_tools" register the SAME tool
// names. Comparing raw names reported the CLI entry as unshadowed, and a caller
// that then excluded its names hid tools a delegate registers through the
// project entry.
//
// The other direction is asserted in the same table: names that genuinely
// differ must NOT match, or the fix would trade an over-exclusion for an
// over-permissive shadow report.
func TestCLIOnlyMatchesServerNameNamespaces(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		cli          string
		project      string
		wantFallback bool
	}{
		{name: "hyphen and underscore are one namespace", cli: "team_tools", project: "team-tools", wantFallback: true},
		{name: "identical names", cli: "svc", project: "svc", wantFallback: true},
		{name: "genuinely different names", cli: "team_tools", project: "other_tools", wantFallback: false},
		{name: "different after normalization", cli: "team_tools_v2", project: "team-tools", wantFallback: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cliLayer := []ServerConfig{{Name: tc.cli, Command: "/bin/cli"}}
			projectLayer := []ServerConfig{{Name: tc.project, Command: "/bin/project"}}
			got := cliOnly(cliLayer, [][]ServerConfig{projectLayer})
			if len(got) != 1 {
				t.Fatalf("cliOnly returned %d entries, want 1", len(got))
			}
			// Precedence still keys on the raw configured name, so the reported
			// config stays the CLI entry's.
			if got[0].Config.Name != tc.cli {
				t.Errorf("reported config name = %q, want the CLI entry %q", got[0].Config.Name, tc.cli)
			}
			if (got[0].Fallback != nil) != tc.wantFallback {
				t.Fatalf("Fallback presence = %v, want %v", got[0].Fallback != nil, tc.wantFallback)
			}
			if tc.wantFallback && got[0].Fallback.Name != tc.project {
				t.Errorf("Fallback names %q, want the other layer's entry %q", got[0].Fallback.Name, tc.project)
			}
		})
	}
}
