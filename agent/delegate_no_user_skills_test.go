package agent

import (
	"context"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// TestNoUserSkillsSuppressionKeepsCLIExclusionWorking pins that discovery and
// freshness honor the same global-config suppression.
//
// With EVENER_NO_USER_SKILLS=1, discovery deliberately skips the global MCP layer
// and records no path for it, so the freshness check must skip it too. When the
// two disagree about whether a layer exists, the recorded empty path never
// matches a path freshness resolves, every check answers "not current", and the
// CLI-only exclusion is disabled for the whole session — both the roster and the
// frozen descriptor keep listing tools the child can never register.
//
// The rule is that discovery and freshness resolve the SAME path under the SAME
// suppression. This test states both halves: the evidence verifies (so the
// exclusion applies), and it stops verifying when the project layer changes (so
// the agreement is not a blanket "always current").
func TestNoUserSkillsSuppressionKeepsCLIExclusionWorking(t *testing.T) {
	// Not parallel: hermeticInfraEnv and t.Setenv both need exclusive env.
	hermeticInfraEnv(t)
	t.Setenv("EVENER_NO_USER_SKILLS", "1")
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := writeInfraFile(t, root+"/.evener/mcp.json",
		`{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// The precondition: the global layer is suppressed, so discovery resolved no
	// path for it. Without this the test would not exercise the suppression.
	if evidence.Global.Path != "" {
		t.Fatalf("fixture is wrong: discovery recorded a global path %q under EVENER_NO_USER_SKILLS=1", evidence.Global.Path)
	}

	s := &Session{env: env}
	s.setManagerOnlyTools(nil)
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.refreshCLIEvidenceFreshnessContext(context.Background())

	got := s.removeChildUnreachableTools([]string{"svc__echo", "read_file"}, "")
	if slices.Contains(got, "svc__echo") {
		t.Errorf("the suppressed global layer was resolved anyway, so the evidence never verified and %q stayed advertised; discovery and freshness must apply the same suppression: %v", "svc__echo", got)
	}

	// The other direction: agreement must not become "always current". A real
	// project-layer edit still invalidates the answer.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)
	s.refreshCLIEvidenceFreshnessContext(context.Background())
	got = s.removeChildUnreachableTools([]string{"svc__echo", "read_file"}, "")
	if !slices.Contains(got, "svc__echo") {
		t.Errorf("the project layer changed, so reachability is unknown and %q must stay eligible: %v", "svc__echo", got)
	}
}
