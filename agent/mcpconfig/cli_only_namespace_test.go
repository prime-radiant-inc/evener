package mcpconfig

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/toolname"
)

// TestCLIReachabilityReportsBothLayerSets pins what discovery reports about the
// CLI layers and the layers a child keeps.
//
// The match is on the namespace MCP registration derives, not on the raw
// configured string: registration namespaces every tool as "<server>__<tool>"
// after replacing hyphens with underscores, so a project server "team-tools" and
// a CLI server "team_tools" register the SAME tool names. A set built from raw
// names reported such a CLI entry as unshadowed, and a caller that then excluded
// its names hid tools a delegate registers through the project entry.
//
// The other direction is asserted too: a namespace that genuinely differs must
// NOT appear, or the fix would trade an over-exclusion for an over-permissive
// answer.
func TestCLIReachabilityReportsBothLayerSets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		cli     string
		project string
	}{
		{name: "hyphen and underscore are one namespace", cli: "team_tools", project: "team-tools"},
		{name: "identical names", cli: "svc", project: "svc"},
		{name: "genuinely different names", cli: "team_tools", project: "other_tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reach CLIReachability
			reach.addOtherLayer([]ServerConfig{{Name: tc.project, Command: "/bin/project"}})
			reach.CLIServers = append(reach.CLIServers, tc.cli)

			if !slices.Equal(reach.CLIServers, []string{tc.cli}) {
				t.Errorf("CLIServers = %v, want the CLI entry %q", reach.CLIServers, tc.cli)
			}
			want := []string{toolname.SanitizeMCPToolName(tc.project)}
			if !slices.Equal(reach.OtherLayerNamespaces, want) {
				t.Errorf("OtherLayerNamespaces = %v, want the project layer's namespace %v", reach.OtherLayerNamespaces, want)
			}
		})
	}
}

// TestCLIReachabilityDedupesNamespacesAcrossLayers pins that the same-namespace
// server declared by the global AND the project layer is reported once: the set
// answers "could this namespace supply the name", not "how many layers said so".
func TestCLIReachabilityDedupesNamespacesAcrossLayers(t *testing.T) {
	t.Parallel()
	var reach CLIReachability
	reach.addOtherLayer([]ServerConfig{{Name: "team-tools"}})
	reach.addOtherLayer([]ServerConfig{{Name: "team_tools"}, {Name: "solo"}})

	want := []string{"team_tools", "solo"}
	if !slices.Equal(reach.OtherLayerNamespaces, want) {
		t.Errorf("OtherLayerNamespaces = %v, want %v (one entry per namespace)", reach.OtherLayerNamespaces, want)
	}
}
