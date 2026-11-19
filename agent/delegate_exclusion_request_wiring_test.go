package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

// TestModelRequestRefreshesTheExclusionCachesWithoutASpawn pins the WIRING, not
// the helper: a project config edited mid-session must reach the next model
// request's cached prompt and delegate schema with no delegate spawned and no
// test call into the refresh path.
//
// The defect it covers is a refresh nobody called. A test that invokes
// refreshCachesForExclusionChangeContext itself proves the helper works and says
// nothing about whether the request path uses it, so this test reaches the
// caches only through prepareModelRequestWithError.
func TestModelRequestRefreshesTheExclusionCachesWithoutASpawn(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(env))
	s.pluginAgents = map[string]plugin.Agent{
		"worker": {Name: "worker", Description: "does the work"},
	}
	// The name must exist in this session's own registry for the classification
	// to have anything to exclude, so it is registered here as a CLI server's
	// tool would be.
	if err := s.reg.Register(syntheticTool(cliTool)); err != nil {
		t.Fatalf("register %s: %v", cliTool, err)
	}

	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})
	// Settle the caches on the startup answer, so the edit below is the only
	// thing that can move them.
	for range 4 {
		s.refreshCachesForExclusionChangeContext(context.Background())
	}
	if !s.cliMCPExclusionApplied {
		t.Fatal("the fixture did not start with the exclusion applied")
	}

	before := prepareRequestSnapshot(t, s)
	if before.exclusionVersion == 0 {
		t.Fatal("the request path stamped no exclusion version, so the fixture cannot observe a rebuild")
	}
	// The WIRE is what this test is for. The tool must be absent from the
	// request before the edit, in both places a model reads it: the role roster
	// in the system prompt the request carries, and the delegate schema's
	// agent_type description inside the request's tool list.
	if strings.Contains(before.systemPrompt, cliTool) {
		t.Fatalf("the startup request's system prompt already advertises %s, so the edit cannot change what the model sees", cliTool)
	}
	if strings.Contains(before.delegateRoster, cliTool) {
		t.Fatalf("the startup request's delegate schema already advertises %s, so the edit cannot change what the model sees", cliTool)
	}

	// The branch switches in place: the project layer now declares the server
	// the CLI entry replaces, which makes the tool reachable for a child. No
	// environment swap runs and no delegate is spawned.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)

	after := prepareRequestSnapshot(t, s)
	if s.cliMCPExclusionApplied {
		t.Error("the request path kept applying the startup exclusion after an in-place config edit")
	}
	if after.exclusionVersion == before.exclusionVersion {
		t.Fatalf("the model request still serves caches built from exclusion version %d after a config edit, so the refresh is not wired into the request path", before.exclusionVersion)
	}
	// Both provider-facing surfaces must now carry the name, read from the
	// RETURNED request. Recomputing the roster from live session state is what
	// let an earlier version of this test pass while the request itself was
	// stale: the session can be right and the wire wrong, and the wire is what
	// the model acts on.
	if !strings.Contains(after.systemPrompt, cliTool) {
		t.Error("the next model request's system prompt still omits the newly reachable tool, so the prompt cache was not rebuilt from the new answer")
	}
	if !strings.Contains(after.delegateRoster, cliTool) {
		t.Error("the next model request's delegate schema still omits the newly reachable tool, so the tool-definition cache was not rebuilt from the new answer")
	}
}

// requestSnapshot is what one prepared model request would send, taken from the
// request prepareModelRequestWithError RETURNED, not from session state.
type requestSnapshot struct {
	systemPrompt   string
	delegateRoster string
	// exclusionVersion is the version the tool-definition cache was built from,
	// which is how a rebuild on the request path is observable.
	exclusionVersion uint64
}

// prepareRequestSnapshot drives one real model request and reads the request it
// produced.
//
// The system prompt and the delegate schema both come from the returned
// request. Reading session caches instead — or recomputing the roster from
// current state — answers whether the SESSION knows the new capability, which
// is not the question: a request built before the refresh carries the old
// roster even though the state behind it has moved.
func prepareRequestSnapshot(t *testing.T, s *Session) requestSnapshot {
	t.Helper()
	var timings events.RoundTimings
	_, sys, _, req, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	snapshot := requestSnapshot{systemPrompt: sys}
	for _, def := range req.Tools {
		if def.Name != "delegate" {
			continue
		}
		properties, _ := def.Parameters["properties"].(map[string]any)
		agentType, _ := properties["agent_type"].(map[string]any)
		description, _ := agentType["description"].(string)
		snapshot.delegateRoster = description
	}
	if snapshot.delegateRoster == "" {
		t.Fatal("the prepared request has no delegate tool definition with an agent_type description, so the schema half cannot be observed")
	}
	s.mu.Lock()
	snapshot.exclusionVersion = s.cachedExclusionVersion
	s.mu.Unlock()
	return snapshot
}

// syntheticTool builds a registry entry for a name this session is meant to
// hold, so the CLI-only classification has a registered name to exclude.
func syntheticTool(name string) tool.RegisteredTool {
	return tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        name,
			Description: "synthetic",
			Parameters:  map[string]any{"type": "object"},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return nil, nil
		},
	}
}
