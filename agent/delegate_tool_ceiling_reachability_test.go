package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/llm"
)

// TestManagerRootCeilingKeepsCompositionTools is the parent half of the
// reachability contract: a Manager root still ADVERTISES session_control to its
// own model, so an exclusion that stripped it from everyone would fail here
// rather than pass a child-only assertion.
//
// The child half — a spawned child's ceiling and descriptor naming neither
// session_control nor a CLI-only MCP tool — drives a real MCP server subprocess
// and lives in delegate_tool_ceiling_reachability_mcp_test.go.
func TestManagerRootCeilingKeepsCompositionTools(t *testing.T) {
	t.Parallel()
	root := newSession(t, withConfig(SessionConfig{Manager: true, testOnly: testConfig{minimalSystemPrompt: true}}))
	defer root.Close()
	root.mu.Lock()
	root.rebuildToolDefsCache()
	root.mu.Unlock()
	if !root.reg.RegisteredNames()["session_control"] {
		t.Fatal("Manager root does not register session_control")
	}
	if !slices.Contains(toolDefinitionNames(root.ToolDefinitions()), "session_control") {
		t.Fatal("Manager root's own model-facing tools omit session_control, so the child assertion proves nothing")
	}
}

func toolDefinitionNames(defs []llm.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

// TestCLIMCPToolNamesFollowsTheRegistryNotTheStatus pins the CLI-only MCP
// classification against every way of keying it on the wrong fact:
//
//   - degraded: a dropped transport keeps the tools it registered, so they stay
//     names a delegate cannot call. Counting only "connected" servers would
//     drop the exclusion and advertise unreachable tools.
//   - failed: failRegister rolls back every name that server added, but
//     Servers() still reports the names it DISCOVERED. When another layer's
//     server registered a colliding name first, the registry holds that name;
//     attributing it to the failed CLI server would hide a tool the child can
//     call. A failed registration contributes nothing.
//   - shadowed: a CLI entry that replaces another layer's server of the same
//     name leaves the name discoverable to a child, so its tools stay eligible.
//     Excluding them on that guess hides tools the child can register — and
//     nothing may be connected to find out.
//
// Everything is fed in as data, so this needs no MCP subprocess and runs in the
// default (short) lane that CI's nonroot job drives.
func TestCLIMCPToolNamesFollowsTheRegistryNotTheStatus(t *testing.T) {
	t.Parallel()
	const (
		server       = "ceilingdeg"
		registered   = server + "__echo"
		unregistered = server + "__missing"
		otherServer  = "elsewhere"
		otherTool    = otherServer + "__echo"
		// A shared server's tool that a same-named CLI-only server also
		// discovers: the shared server won the registration.
		sharedTool = server + "__lookup"
	)
	reg := tool.NewRegistry()
	for _, name := range []string{registered, otherTool, sharedTool} {
		if err := reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}

	for _, status := range []string{"connected", "degraded"} {
		servers := map[string]bool{server: true}
		infos := []mcpconfig.ServerInfo{
			{Name: server, Status: status, Tools: []string{registered, unregistered, sharedTool}},
			{Name: otherServer, Status: "connected", Tools: []string{otherTool}},
		}
		got := cliMCPToolNames(servers, nil, nil, infos, reg)
		if !got[registered] {
			t.Errorf("status %q: %q is registered but not excluded, so a delegate would advertise it", status, registered)
		}
		if !got[sharedTool] {
			t.Errorf("status %q: %q is registered but not excluded, so a delegate would advertise it", status, sharedTool)
		}
		if got[unregistered] {
			t.Errorf("status %q: %q never registered but was excluded", status, unregistered)
		}
		if got[otherTool] {
			t.Errorf("status %q: %q belongs to a server the child still discovers", status, otherTool)
		}
	}

	// A failed registration added nothing: every name it discovered belongs to
	// whichever server actually registered it, so the CLI-only server claims
	// none of them.
	failed := cliMCPToolNames(
		map[string]bool{server: true},
		nil,
		nil,
		[]mcpconfig.ServerInfo{{Name: server, Status: "failed", Tools: []string{registered, unregistered, sharedTool}}},
		reg,
	)
	if len(failed) != 0 {
		t.Errorf("a failed CLI-only registration excluded %v; it registered nothing, and every one of those names is another server's tool", failed)
	}

	// A CLI entry that shadows another layer's server: the child may rediscover
	// that name, so nothing the entry registered is excluded. The exclusion must
	// not guess, and it may not connect the shadowed server to stop guessing.
	shadowed := cliMCPToolNames(
		map[string]bool{server: true},
		map[string]bool{server: true},
		nil,
		[]mcpconfig.ServerInfo{{Name: server, Status: "connected", Tools: []string{registered, sharedTool}}},
		reg,
	)
	if len(shadowed) != 0 {
		t.Errorf("a shadowed CLI entry excluded %v; a child may rediscover that name and register those tools", shadowed)
	}
}

// TestCLIToolAnotherLayerAlsoSuppliesStaysEligible pins the case where a
// CLI-only server's NAME is not the origin of its registered tool name.
//
// A project server "team" exposing "docs__lookup" and a CLI server "team__docs"
// exposing "lookup" both register "team__docs__lookup": registration sanitizes
// the whole "<server>__<tool>" join as one string. The CLI server really did
// register that name here, so keying eligibility on the server name alone calls
// it CLI-only and strips it from the child's ceiling — while the child
// rediscovers the same name from the project server.
//
// The exclusion therefore asks whether any namespace the child keeps could
// supply the name. That question is coarser than the truth on purpose: a name
// cannot be split to recover its origin, so a namespace that could produce the
// name keeps it eligible. The permissive direction is the correct one, because
// an over-narrow ceiling hides a tool the child can call.
func TestCLIToolAnotherLayerAlsoSuppliesStaysEligible(t *testing.T) {
	t.Parallel()
	const (
		cliServer = "team__docs"
		// Also produced by a project server named "team" exposing "docs__lookup".
		collidingTool = "team__docs__lookup"
		// The CLI server's own namespace, which no other layer supplies.
		soloServer = "solo"
		soloTool   = soloServer + "__run"
	)
	reg := tool.NewRegistry()
	for _, name := range []string{collidingTool, soloTool} {
		if err := reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}
	servers := map[string]bool{cliServer: true, soloServer: true}
	infos := []mcpconfig.ServerInfo{
		{Name: cliServer, Status: "connected", Tools: []string{collidingTool}},
		{Name: soloServer, Status: "connected", Tools: []string{soloTool}},
	}

	// The effective configuration a child would load: both CLI entries are
	// classified CLI-only, and the project layer contributes "team".
	prefixes := otherLayerMCPNamespaces(
		[]mcpconfig.ServerConfig{{Name: cliServer}, {Name: soloServer}, {Name: "team"}},
		[]mcpconfig.CLIOnlyServer{{Config: mcpconfig.ServerConfig{Name: cliServer}}, {Config: mcpconfig.ServerConfig{Name: soloServer}}},
	)
	if !slices.Contains(prefixes, "team") {
		t.Fatalf("the project namespace is missing from %v; this test would pass without exercising the collision", prefixes)
	}

	got := cliMCPToolNames(servers, nil, prefixes, infos, reg)
	if got[collidingTool] {
		t.Errorf("%q is excluded, but a project server named team exposing docs__lookup registers that same name, so the child can call it", collidingTool)
	}
	if !got[soloTool] {
		t.Errorf("%q is not excluded, but no layer the child keeps supplies the %q namespace", soloTool, soloServer)
	}

	// Without the project layer the name really is CLI-only. This is what makes
	// the eligibility above a consequence of the namespace rather than of any
	// name escaping classification.
	bare := cliMCPToolNames(servers, nil, nil, infos, reg)
	if !bare[collidingTool] {
		t.Errorf("%q is not excluded with no other layer configured, so the assertion above would hold for any name", collidingTool)
	}
}
