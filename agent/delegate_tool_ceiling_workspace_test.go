package agent

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// TestChildUnreachableToolsFollowTheDelegateWorkspace is the workspace half of
// the ceiling rule: the exclusion may only rest on what the startup snapshot can
// ESTABLISH about the delegate's own target workspace. Two ways it cannot:
//
//   - a worktree spawn cuts a fresh lane, and the child spawns inside it, where
//     a <lane>/.evener/mcp.json this session never read may declare any server.
//     The MCP half of the exclusion therefore does not apply to that spawn.
//   - the session's own workspace changed (a lane entered, a project config
//     added). The classification was derived from the old project layer and
//     says nothing about the new one, so it is dropped rather than carried.
//
// Both directions are asserted: the Manager half still applies to a worktree
// spawn, because no spawn path composes the hub whatever the workspace.
func TestChildUnreachableToolsFollowTheDelegateWorkspace(t *testing.T) {
	hermeticInfraEnv(t)
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	const (
		managerTool = "session_control"
		cliTool     = "svc__echo"
		ordinary    = "read_file"
	)
	// A real workspace whose project layer says what the classification was
	// derived from. The exclusion rests on the evidence verifying, and evidence
	// now covers every layer a child reads, so the fixture records what discovery
	// actually read rather than a hand-built project-only value that could never
	// verify.
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	writeInfraFile(t, filepath.Join(root, ".evener", "mcp.json"),
		`{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	newSession := func() *Session {
		s := &Session{env: env}
		s.setManagerOnlyTools(map[string]bool{managerTool: true})
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
		s.refreshCLIEvidenceFreshnessContext(context.Background())
		return s
	}
	ceiling := []string{managerTool, cliTool, ordinary}

	t.Run("same workspace excludes both", func(t *testing.T) {
		t.Parallel()
		got := newSession().removeChildUnreachableTools(ceiling, "")
		if slices.Contains(got, managerTool) || slices.Contains(got, cliTool) {
			t.Errorf("same-workspace ceiling kept an unreachable tool: %v", got)
		}
		if !slices.Contains(got, ordinary) {
			t.Errorf("the exclusion dropped an ordinary tool: %v", got)
		}
	})

	t.Run("worktree spawn keeps the MCP tool and still drops the manager tool", func(t *testing.T) {
		t.Parallel()
		got := newSession().removeChildUnreachableTools(ceiling, "worktree")
		if !slices.Contains(got, cliTool) {
			t.Errorf("a worktree spawn excluded %q, but its lane may declare that server: %v", cliTool, got)
		}
		if slices.Contains(got, managerTool) {
			t.Errorf("a worktree spawn kept %q, which no spawn path can compose: %v", managerTool, got)
		}
	})

	// A workspace change invalidates the VERDICT, not the classification. The
	// classification is what the CLI layers supply, which a swap does not change;
	// clearing it too meant an environment swap erased the exclusion for the rest
	// of the session, so returning to the unchanged original workspace could not
	// recover it and CLI-only tools stayed advertised.
	t.Run("a workspace change invalidates the verdict but keeps the classification", func(t *testing.T) {
		t.Parallel()
		moved := newSession()
		moved.invalidateCLIMCPClassification()
		got := moved.removeChildUnreachableTools(ceiling, "")
		if !slices.Contains(got, cliTool) {
			t.Errorf("the invalidated verdict left %q excluded in an environment it does not describe: %v", cliTool, got)
		}
		if slices.Contains(got, managerTool) {
			t.Errorf("a workspace change must not re-admit the Manager-only tool %q: %v", managerTool, got)
		}
		// And the classification survives, so the exclusion can come back.
		if servers, _ := moved.cliMCPServersSnapshot(); len(servers) == 0 {
			t.Errorf("the workspace change erased the classification, so returning to the original workspace could not recover it")
		}
		if names := moved.cliMCPToolNamesSnapshot(); len(names) == 0 {
			t.Errorf("the workspace change erased the CLI tool names")
		}
	})
}
