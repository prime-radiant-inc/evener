package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// These tests cover the dispose-only manage_worktree surface (delegate-lane
// disposal spec §P1 "Availability"; test 17):
//
//   - leaf delegate (allowance 0)                 -> no manage_worktree at all
//   - non-isolated coordinator (allowance>0)      -> full tool incl. dispose
//   - isolated coordinator (allowance>0, worktree) -> dispose-only variant:
//       schema op enum lists only "dispose"; every other op refused in-handler
//       with the isolation rationale; dispose itself proceeds to its ladder.
//
// Both the spawn and restore paths run initSessionState, so a subagent session
// built directly with the spawn config exercises the same chokepoint.

func newSubagentSessionForAvailability(t *testing.T, isolation string, allowance int) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	cfg := SessionConfig{
		MaxSubagentDepth: 3,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}
	cfg.spawn.parentSessionID = "parent-session-id"
	cfg.spawn.depth = 1
	cfg.spawn.delegationAllowance = allowance
	cfg.spawn.isolation = isolation
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func manageWorktreeOpEnum(t *testing.T, def llm.ToolDefinition) []string {
	t.Helper()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("manage_worktree parameters missing properties: %+v", def.Parameters)
	}
	op, ok := props["operation"].(map[string]any)
	if !ok {
		t.Fatalf("manage_worktree missing operation property: %+v", props)
	}
	rawEnum, ok := op["enum"].([]string)
	if !ok {
		t.Fatalf("manage_worktree operation enum not []string: %+v", op["enum"])
	}
	return rawEnum
}

func TestWorktreeAvailability_LeafDelegateHasNoTool(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "", 0)
	if s.reg.Get("manage_worktree") != nil {
		t.Error("leaf delegate (allowance 0) must not have manage_worktree at all")
	}
}

func TestWorktreeAvailability_LeafIsolatedDelegateHasNoTool(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "worktree", 0)
	if s.reg.Get("manage_worktree") != nil {
		t.Error("isolated leaf delegate (allowance 0) must not have manage_worktree at all")
	}
}

func TestWorktreeAvailability_NonIsolatedCoordinatorHasFullTool(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "", 1)
	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("non-isolated coordinator (allowance>0) must have the full manage_worktree tool")
	}
	enum := manageWorktreeOpEnum(t, rt.Definition)
	for _, want := range []string{"create", "list", "switch", "exit", "remove", "prune", "dispose"} {
		if !containsString(enum, want) {
			t.Errorf("full tool op enum %v missing %q", enum, want)
		}
	}
}

func TestWorktreeAvailability_IsolatedCoordinatorHasDisposeOnlyVariant(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "worktree", 1)
	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("isolated coordinator (allowance>0) must have the dispose-only manage_worktree surface")
	}
	enum := manageWorktreeOpEnum(t, rt.Definition)
	if len(enum) != 1 || enum[0] != "dispose" {
		t.Fatalf("dispose-only variant op enum = %v, want exactly [\"dispose\"]", enum)
	}
}

func TestWorktreeAvailability_IsolatedCoordinatorRefusesNonDisposeOps(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "worktree", 1)
	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("isolated coordinator must have the dispose-only surface")
	}
	env := s.currentEnv()
	for _, op := range []string{"create", "list", "switch", "exit", "remove", "prune"} {
		_, err := rt.Exec(context.Background(), env, map[string]any{"operation": op, "name": "x"})
		if err == nil {
			t.Errorf("op %q on the dispose-only surface must be refused in-handler", op)
			continue
		}
		if !strings.Contains(err.Error(), "isolation worktree lane") {
			t.Errorf("op %q refusal = %q, want the isolation rationale", op, err.Error())
		}
	}
}

func TestWorktreeAvailability_IsolatedCoordinatorDisposePassesGate(t *testing.T) {
	t.Parallel()
	s := newSubagentSessionForAvailability(t, "worktree", 1)
	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("isolated coordinator must have the dispose-only surface")
	}
	// dispose with an unknown id must reach the dispose ladder (not the surface
	// gate). Assert the error is NOT the surface refusal.
	_, err := rt.Exec(context.Background(), s.currentEnv(), map[string]any{"operation": "dispose", "id": "dlg_nonexistent"})
	if err != nil && strings.Contains(err.Error(), "isolation worktree lane") {
		t.Errorf("dispose must pass the surface gate; got surface refusal: %v", err)
	}
}

// initSessionState is the shared chokepoint for spawn AND restore, so a
// restored worktree-isolated coordinator must be served the same dispose-only
// variant (spec §P1 "Availability"; test 17 restore-path case).
func TestWorktreeAvailability_IsolatedCoordinatorDisposeOnlyAfterRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 3,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}
	cfg.spawn.parentSessionID = "parent-session-id"
	cfg.spawn.depth = 1
	cfg.spawn.delegationAllowance = 1
	cfg.spawn.isolation = "worktree"
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	meta := s.Meta()
	s.Close()

	restoreCfg := RestoreSessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}
	restoreCfg.spawn.parentSessionID = "parent-session-id"
	restoreCfg.spawn.depth = 1
	restoreCfg.spawn.delegationAllowance = 1
	restoreCfg.spawn.isolation = "worktree"
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, restoreCfg)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	rt := restored.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("restored isolated coordinator must have the dispose-only manage_worktree surface")
	}
	enum := manageWorktreeOpEnum(t, rt.Definition)
	if len(enum) != 1 || enum[0] != "dispose" {
		t.Fatalf("restored dispose-only variant op enum = %v, want exactly [\"dispose\"]", enum)
	}
	_, err = rt.Exec(context.Background(), restored.currentEnv(), map[string]any{"operation": "create", "name": "x"})
	if err == nil || !strings.Contains(err.Error(), "isolation worktree lane") {
		t.Errorf("restored surface must refuse create with the isolation rationale, got: %v", err)
	}
}

// Guard the pure schema shape independently of session wiring.
func TestDefManageWorktreeDisposeOnly_SchemaListsOnlyDispose(t *testing.T) {
	t.Parallel()
	def := tool.DefManageWorktreeDisposeOnly()
	if def.Name != "manage_worktree" {
		t.Errorf("dispose-only variant name = %q, want manage_worktree (same tool name)", def.Name)
	}
	enum := manageWorktreeOpEnum(t, def)
	if len(enum) != 1 || enum[0] != "dispose" {
		t.Fatalf("dispose-only variant op enum = %v, want exactly [\"dispose\"]", enum)
	}
}
