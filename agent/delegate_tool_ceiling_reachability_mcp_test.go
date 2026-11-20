//go:build !short

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A spawned child's delegate tool ceiling must describe what the child can
// actually register. Two parent tools it never can:
//
//   - session_control, which only Manager composition supplies, because every
//     spawn path sets Manager=false;
//   - a tool from an MCP server only the CLI layers declare, because every
//     spawn path clears MCPConfigFiles and MCPInline.
//
// The ceiling reaches the model twice — the <available_agents> roster's
// "Default tools" line and descriptor.ToolNameCeiling — so both must lose the
// same entries, and the fix must not strip them from a root that can hold them
// (TestManagerRootCeilingKeepsCompositionTools, the untagged parent half).
//
// This one uses the package's shared stdio MCP server fixture, the same real
// server the initMCP integration tests drive, so the CLI-only tool comes from a
// real MCP registration rather than a stub. Building a subprocess is why it
// carries the !short tag, like the other fixtures this package compiles.
func TestDelegateToolCeilingOmitsToolsTheChildCannotRegister(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	root := newSession(t, withConfig(SessionConfig{
		Manager:          true,
		StateDir:         t.TempDir(),
		MCPInline:        []string{"ceilingmcp:" + bin},
		MaxSubagentDepth: 1,
		testOnly:         testConfig{minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	defer root.Close()

	const cliTool = "ceilingmcp__echo"
	registered := root.reg.RegisteredNames()
	// Both tools must be real on the parent, or the child half passes for the
	// wrong reason: a ceiling cannot wrongly name what nothing registered.
	if !registered["session_control"] {
		t.Fatal("Manager root did not register session_control, so the child half could pass vacuously")
	}
	if !registered[cliTool] {
		t.Fatalf("Manager root did not register the CLI MCP tool %q, so the child half could pass vacuously", cliTool)
	}

	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-input", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
	for _, name := range []string{"session_control", cliTool} {
		if slices.Contains(descriptor.ToolNameCeiling, name) {
			t.Errorf("child ceiling names %q, which the child can never register: %v", name, descriptor.ToolNameCeiling)
		}
	}
	// Ordinary tools survive: the exclusion is not a blanket strip.
	if !slices.Contains(descriptor.ToolNameCeiling, "read_file") {
		t.Errorf("child ceiling lost an ordinary tool it can register: %v", descriptor.ToolNameCeiling)
	}
	// The roster render loses the same entries, so the prompt and the
	// descriptor cannot disagree.
	root.mu.Lock()
	rendered := root.defaultToolNamesForAgent(root.pluginAgents["default"])
	root.mu.Unlock()
	for _, name := range []string{"session_control", cliTool} {
		if slices.Contains(rendered, name) {
			t.Errorf("<available_agents> roster names %q for a role's delegate: %v", name, rendered)
		}
	}

	sub := root.getSub(result.ChildSessionID)
	if sub == nil {
		t.Fatal("missing child runtime")
	}
	sub.mu.Lock()
	childRegistered := sub.sess.reg.RegisteredNames()
	sub.mu.Unlock()
	for _, name := range []string{"session_control", cliTool} {
		if childRegistered[name] {
			t.Errorf("child registered %q, so the ceiling's exclusion would hide a callable tool", name)
		}
	}
}

// TestDelegateToolCeilingKeepsCollisionWinnerWhenCLIEntryRollsBack is the
// rollback half of the classification, and it is the case a name-level rule gets
// wrong. Two layer entries whose names sanitize to the same tool namespace —
// "ceiling-col" in the project layer, "ceiling_col" on the command line — both
// expose a tool named "ceiling_col__echo". The project server registers it
// first; the CLI server hits the collision, fails, and failRegister rolls back
// every name it had added. Servers() still lists "ceiling_col__echo" under the
// FAILED CLI server, because that is what it discovered.
//
// The tool is still callable — by the project server, which is exactly what the
// child loads after clearing the CLI layer. So the ceiling must keep it, and the
// child must register it. The exclusion may only follow a registration that
// actually happened, never a name that appears in a failed server's list.
//
// The two negative halves fall out of the same fixture and are asserted too: a
// name only the failed CLI server could have served is excluded from nothing,
// and an ordinary tool survives.
func TestDelegateToolCeilingKeepsCollisionWinnerWhenCLIEntryRollsBack(t *testing.T) {
	hermeticInfraEnv(t)
	bin := intg_buildMCPServer(t)

	projectRoot := t.TempDir()
	runInfraGit(t, projectRoot, "init", "-q")
	writeInfraFile(t, filepath.Join(projectRoot, ".evener", "mcp.json"),
		`{"mcpServers":{"ceiling-col":{"command":"`+bin+`"}}}`, 0o644)

	const collided = "ceiling_col__echo"
	root := newSession(t, withDir(projectRoot), withConfig(SessionConfig{
		Manager:          true,
		StateDir:         t.TempDir(),
		MCPInline:        []string{"ceiling_col:" + bin},
		MaxSubagentDepth: 1,
		testOnly:         testConfig{minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	defer root.Close()

	// The fixture only proves anything if the collision really happened: the
	// name is registered (by the winner) and the CLI server really failed.
	if root.reg.Get(collided) == nil {
		t.Fatalf("fixture did not register %q, so no collision was exercised; MCP tools: %v", collided, intg_toolDefNames(root.mcpTools))
	}
	var sawFailed bool
	for _, info := range root.mcpMgr.Servers() {
		if info.Name == "ceiling_col" && info.Status == "failed" && slices.Contains(info.Tools, collided) {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatalf("the CLI entry did not fail registration while still listing %q, so the rollback case never ran", collided)
	}

	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-input", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
	if !slices.Contains(descriptor.ToolNameCeiling, collided) {
		t.Errorf("child ceiling dropped %q, which the child registers through the project layer: %v", collided, descriptor.ToolNameCeiling)
	}
	if !slices.Contains(descriptor.ToolNameCeiling, "read_file") {
		t.Errorf("child ceiling lost an ordinary tool it can register: %v", descriptor.ToolNameCeiling)
	}

	sub := root.getSub(result.ChildSessionID)
	if sub == nil {
		t.Fatal("missing child runtime")
	}
	sub.mu.Lock()
	childRegistered := sub.sess.reg.RegisteredNames()
	sub.mu.Unlock()
	if !childRegistered[collided] {
		t.Errorf("child did not register %q, so the ceiling names a tool it cannot call: %v", collided, childRegistered)
	}
}

// TestShadowedCLIMCPEntryIsNeverContacted is the security half of the same rule,
// and it asserts the CONNECTION ATTEMPT rather than the resulting tool list. A
// test that only compared tool names would still pass if session start dialled
// the shadowed entry and ignored what came back — and dialling is the damage: a
// shadowed stdio entry executes its configured command, a shadowed HTTP entry
// receives expanded credential headers, and this all happens with no delegate
// spawned and the repository's own config as the only input.
//
// The fallback config here is a wrapper script that creates a marker file and
// only then execs the real MCP server, so the marker is a record of the attempt
// itself. Nothing in a correct session start may create it: precedence replaces
// the whole project entry with the CLI one, so the project entry is not the
// effective configuration and is never dialled.
//
// The assertion is scoped to session start on purpose. A delegate DOES load the
// project layer — that is why the shadowed entry's tools stay eligible in the
// ceiling — and a spawn would therefore create the marker legitimately.
func TestShadowedCLIMCPEntryIsNeverContacted(t *testing.T) {
	hermeticInfraEnv(t)
	bin := intg_buildMCPServer(t)

	projectRoot := t.TempDir()
	runInfraGit(t, projectRoot, "init", "-q")
	marker := filepath.Join(projectRoot, "contacted")
	script := writeInfraFile(t, filepath.Join(projectRoot, "fallback.sh"),
		"#!/bin/sh\n: > "+marker+"\nexec "+bin+"\n", 0o755)
	writeInfraFile(t, filepath.Join(projectRoot, ".evener", "mcp.json"),
		`{"mcpServers":{"svc":{"command":"`+script+`"}}}`, 0o644)

	root := newSession(t, withDir(projectRoot), withConfig(SessionConfig{
		Manager:          true,
		StateDir:         t.TempDir(),
		MCPInline:        []string{"svc:" + bin},
		MaxSubagentDepth: 1,
		testOnly:         testConfig{minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	defer root.Close()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("session start contacted the shadowed project entry: %s exists, so its command ran", marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat marker: %v", err)
	}

	// The test must have created a real shadow, or it proves nothing: the name
	// has to be a CLI-supplied server that the PROJECT layer also declares, so the
	// child rediscovers it there, and the parent must be connected to the CLI
	// entry that won.
	if servers := root.cliMCPServersSnapshot(); !servers["svc"] {
		t.Fatalf("fixture did not classify a CLI entry: servers=%v", servers)
	}
	// The project layer's namespace is reported, which is what keeps "svc"'s
	// tools eligible for the child. A merged-view set could not see this server:
	// the CLI entry replaced it.
	if prefixes := root.cliMCPOtherNamespacesSnapshot(); !slices.Contains(prefixes, "svc") {
		t.Fatalf("fixture did not shadow a CLI entry: other-layer namespaces=%v", prefixes)
	}
	if root.reg.Get("svc__echo") == nil {
		t.Fatalf("root did not register the effective CLI entry's tool, so the shadow never existed")
	}

	// The marker must be able to appear, or "no marker" proves nothing. Run the
	// fallback script directly and require it to leave the marker this time: the
	// assertion above is only meaningful once the mechanism is shown live.
	// TRIPWIRE: this bounds a fixture script that writes one file, so 30s sits far
	// above the expected time and only fires if the process is wedged.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	control := exec.CommandContext(ctx, script)
	control.Stdout, control.Stderr = nil, nil
	if err := control.Run(); err != nil && ctx.Err() == nil {
		// The server exits non-zero when its stdin closes, which is expected; a
		// timeout is not, and neither is a missing marker below.
		t.Logf("fallback script exited with %v, checking the marker anyway", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the fallback script did not leave its marker when run directly, so the no-contact assertion is vacuous: %v", err)
	}
}

// TestDelegateToolCeilingKeepsPluginToolACLIEntryShadows is the other direction
// of the same rule. A CLI entry that shadows a plugin MCP server by name wins
// in THIS session, but the child clears the CLI layers and still loads the
// plugin, so it rediscovers a server of that name and registers its tools. The
// ceiling must keep them: excluding them would hide a callable tool.
func TestDelegateToolCeilingKeepsPluginToolACLIEntryShadows(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	pluginDir := makePluginDir(t, "ceilingplug")
	mcpJSON, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{"svc": map[string]any{"command": bin}},
	})
	if err != nil {
		t.Fatalf("marshal .mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), mcpJSON, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	// The plugin's server registers as "plugin_ceilingplug_svc"; the inline
	// spec below reuses that exact name, so the CLI entry shadows it.
	const shadowed = "plugin_ceilingplug_svc"
	const shadowedTool = shadowed + "__echo"
	root := newSession(t, withConfig(SessionConfig{
		Manager:          true,
		StateDir:         t.TempDir(),
		PluginDirs:       []string{pluginDir},
		MCPInline:        []string{shadowed + ":" + bin},
		MaxSubagentDepth: 1,
		testOnly:         testConfig{minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	defer root.Close()

	if root.reg.Get(shadowedTool) == nil {
		t.Fatalf("root did not register %q, so the shadowing case never ran; MCP tools: %v", shadowedTool, intg_toolDefNames(root.mcpTools))
	}

	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-input", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
	if !slices.Contains(descriptor.ToolNameCeiling, shadowedTool) {
		t.Errorf("child ceiling dropped %q, which the child registers through the plugin it still loads: %v", shadowedTool, descriptor.ToolNameCeiling)
	}
	root.mu.Lock()
	rendered := root.defaultToolNamesForAgent(root.pluginAgents["default"])
	root.mu.Unlock()
	if !slices.Contains(rendered, shadowedTool) {
		t.Errorf("<available_agents> roster dropped %q, which the child registers: %v", shadowedTool, rendered)
	}

	// The child's OWN registration of a plugin MCP tool is not asserted here: a
	// default-role child filters MCP tools out of its registry, so this fixture
	// cannot show it. TestIntg_PluginAgentRetainsAndExecutesMappedMCPToolAtSpawn
	// covers that direction, with a plugin agent whose tools map names the tool.
	// What this test owns is the classification: the server the child still
	// discovers must not be bucketed as CLI-only.
}
