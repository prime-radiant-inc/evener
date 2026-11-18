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
		[]mcpconfig.CLIOnlyServer{{Config: mcpconfig.ServerConfig{Name: cliServer}}, {Config: mcpconfig.ServerConfig{Name: soloServer}}}, nil)
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

// TestCLIOverrideDoesNotHideTheNamespaceItReplaces is the override half of the
// same rule, and it is the case a name-level comparison gets wrong twice over.
//
// The parent's effective configuration is the CLI's: it connects the override.
// The CHILD clears the CLI layers, so it discovers the server they replaced.
// That namespace is therefore still reachable by the child, and a CLI server
// whose registered name collides with it must not take the name away.
//
// Registration sanitizes the whole "<server>__<tool>" join, so the project
// server "team" exposing "docs__lookup" and the CLI server "team__docs"
// exposing "lookup" both register "team__docs__lookup".
func TestCLIOverrideDoesNotHideTheNamespaceItReplaces(t *testing.T) {
	t.Parallel()
	const (
		// The server the CLI overrides: the child discovers the project copy.
		overridden = "team"
		cliServer  = "team__docs"
		collision  = "team__docs__lookup"
	)
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(llm.ToolDefinition{Name: collision, Description: "fixture"})); err != nil {
		t.Fatalf("register %q: %v", collision, err)
	}
	servers := map[string]bool{overridden: true, cliServer: true}
	// The parent registered the collided name from its own connected servers;
	// the CLI server is the one that reports it.
	infos := []mcpconfig.ServerInfo{{Name: cliServer, Status: "connected", Tools: []string{collision}}}

	// The effective configuration is the CLI's, so both entries appear in it.
	// "team" carries a Fallback because another layer declares it.
	fallback := mcpconfig.ServerConfig{Name: overridden}
	cliOnly := []mcpconfig.CLIOnlyServer{
		{Config: mcpconfig.ServerConfig{Name: overridden}, Fallback: &fallback},
		{Config: mcpconfig.ServerConfig{Name: cliServer}},
	}
	prefixes := otherLayerMCPNamespaces(
		[]mcpconfig.ServerConfig{{Name: overridden}, {Name: cliServer}},
		cliOnly, nil)
	if cliMCPToolNames(servers, nil, prefixes, infos, reg)[collision] {
		t.Errorf("%q is excluded, but the project server team that the CLI overrides registers that same name for the child", collision)
	}
	if !slices.Contains(prefixes, overridden) {
		t.Fatalf("%q is missing from %v; an overridden server is one the child rediscovers, so the assertion above proves nothing", overridden, prefixes)
	}

	// Without the fallback there is nothing for the child to rediscover, so the
	// name really is CLI-only. That is what makes the eligibility above a
	// consequence of the override rather than of the name escaping classification.
	unshadowed := otherLayerMCPNamespaces(
		[]mcpconfig.ServerConfig{{Name: overridden}, {Name: cliServer}},
		[]mcpconfig.CLIOnlyServer{
			{Config: mcpconfig.ServerConfig{Name: overridden}},
			{Config: mcpconfig.ServerConfig{Name: cliServer}},
		}, nil)
	if slices.Contains(unshadowed, overridden) {
		t.Fatalf("%q is a prefix with no fallback configured, so nothing the child keeps supplies it", overridden)
	}
	if !cliMCPToolNames(servers, nil, unshadowed, infos, reg)[collision] {
		t.Errorf("%q is not excluded with no overridden server, so the assertion above would hold for any name", collision)
	}
}

// TestShadowedNamespaceSurvivesASeparateCLICollision is the three-server form of
// the override rule, and it is the one a set built from the parent's MERGED view
// gets wrong.
//
// The parent's merged configuration holds the CLI override of "team" and the
// CLI server "team__docs", so a set derived from merged names alone removes
// "team" (shadowed) and cannot see the project server behind it. The child
// clears the CLI layers and discovers the project "team", which exposes
// "docs__lookup" — so "team__docs__lookup", registered by the CLI "team__docs",
// is a name the child can reach through the project server.
func TestShadowedNamespaceSurvivesASeparateCLICollision(t *testing.T) {
	t.Parallel()
	const (
		projectServer = "team"
		overrideOwner = "team"
		cliServer     = "team__docs"
		collision     = "team__docs__lookup"
	)
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(llm.ToolDefinition{Name: collision, Description: "fixture"})); err != nil {
		t.Fatalf("register %q: %v", collision, err)
	}
	servers := map[string]bool{overrideOwner: true, cliServer: true}
	infos := []mcpconfig.ServerInfo{{Name: cliServer, Status: "connected", Tools: []string{collision}}}

	// The parent merges the CLI override of "team" over the project server, and
	// connects "team__docs" as well. The override's tools differ from the
	// project server's; the child sees the project server's.
	fallback := mcpconfig.ServerConfig{Name: projectServer}
	cliOnly := []mcpconfig.CLIOnlyServer{
		{Config: mcpconfig.ServerConfig{Name: overrideOwner}, Fallback: &fallback},
		{Config: mcpconfig.ServerConfig{Name: cliServer}},
	}
	prefixes := otherLayerMCPNamespaces(
		[]mcpconfig.ServerConfig{{Name: overrideOwner}, {Name: cliServer}},
		cliOnly, nil)

	if cliMCPToolNames(servers, nil, prefixes, infos, reg)[collision] {
		t.Errorf("%q is excluded, but the child discovers the project server team behind the CLI override and that server supplies the name", collision)
	}
	if !slices.Contains(prefixes, projectServer) {
		t.Fatalf("%q is missing from %v; the child rediscovers it through the overridden server, so the assertion above proves nothing", projectServer, prefixes)
	}
	if slices.Contains(prefixes, cliServer) {
		t.Errorf("%q is in %v, but nothing the child keeps declares that server", cliServer, prefixes)
	}
}

// TestNamespacePrefixUsesRegistrationSeparator pins the separator in the prefix
// question, which is symmetric and therefore needs both directions.
//
// Registration joins "<server>__<tool>" with a DOUBLE underscore. A single one
// would let a shorter namespace claim a longer server's tools: "github" would
// match "github_enterprise__search", which belongs to a server the child may
// not reach at all, and the roster would advertise a tool that does not exist.
// Requiring an exact match instead would break legitimate compound names.
func TestNamespacePrefixUsesRegistrationSeparator(t *testing.T) {
	t.Parallel()
	const (
		projectServer = "team"
		// Reachable through the project server: it exposes "docs__lookup".
		compound = "team__docs__lookup"
		// A DIFFERENT server whose namespace merely starts with "github".
		neighbour = "github_enterprise__search"
		// The plain form the neighbouring server would produce.
		plainProject = "github"
		cliServer    = "github_enterprise"
	)
	reg := tool.NewRegistry()
	for _, name := range []string{compound, neighbour} {
		if err := reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}

	// The compound name is reachable: "team" is a namespace the child keeps.
	if !suppliedByAnotherLayer(compound, []string{projectServer}) {
		t.Errorf("%q is not reachable through %q, but registration joins them with __ so the project server supplies it", compound, projectServer)
	}

	// The neighbouring server's name is NOT reachable through "github".
	// This is the assertion a single-underscore separator fails.
	if suppliedByAnotherLayer(neighbour, []string{plainProject}) {
		t.Errorf("%q is treated as reachable through %q, which is a different server: the roster would advertise a tool the child cannot discover", neighbour, plainProject)
	}

	// And the exclusion follows: the CLI server's own name is excluded, while a
	// name the child reaches through the project server is not.
	servers := map[string]bool{cliServer: true}
	infos := []mcpconfig.ServerInfo{{Name: cliServer, Status: "connected", Tools: []string{neighbour}}}
	got := cliMCPToolNames(servers, nil, []string{plainProject}, infos, reg)
	if !got[neighbour] {
		t.Errorf("%q is not excluded, but only the CLI server %q supplies it", neighbour, cliServer)
	}
}

// TestPluginNamespaceSurvivesACLIOverride is the plugin half of the override
// rule, and it is the case CLIOnlyServer.Fallback cannot cover.
//
// Discovery runs BEFORE the plugin layers are merged, so a CLI entry that
// overrides a PLUGIN server has no fallback recorded for it. The child clears
// the CLI layers and reloads that plugin itself, so the plugin namespace is
// reachable regardless — and a CLI server whose registered name collides with
// it must not take the name away.
//
// Registration sanitizes the whole "<server>__<tool>" join, so the plugin
// server "plugin_team_svc" exposing "docs__lookup" and the CLI server
// "plugin_team_svc__docs" exposing "lookup" both register
// "plugin_team_svc__docs__lookup".
func TestPluginNamespaceSurvivesACLIOverride(t *testing.T) {
	t.Parallel()
	const (
		pluginServer = "plugin_team_svc"
		cliServer    = "plugin_team_svc__docs"
		collision    = "plugin_team_svc__docs__lookup"
	)
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(llm.ToolDefinition{Name: collision, Description: "fixture"})); err != nil {
		t.Fatalf("register %q: %v", collision, err)
	}
	servers := map[string]bool{pluginServer: true, cliServer: true}
	infos := []mcpconfig.ServerInfo{{Name: cliServer, Status: "connected", Tools: []string{collision}}}

	// The CLI overrides the plugin server, so the effective configs hold the
	// CLI copy. Discovery never saw a fallback for it.
	cliOnly := []mcpconfig.CLIOnlyServer{
		{Config: mcpconfig.ServerConfig{Name: pluginServer}},
		{Config: mcpconfig.ServerConfig{Name: cliServer}},
	}
	pluginConfigs := []mcpconfig.ServerConfig{{Name: pluginServer}}
	effective := []mcpconfig.ServerConfig{{Name: pluginServer}, {Name: cliServer}}

	prefixes := otherLayerMCPNamespaces(effective, cliOnly, pluginConfigs)
	if cliMCPToolNames(servers, nil, prefixes, infos, reg)[collision] {
		t.Errorf("%q is excluded, but the child reloads plugin server %q, which exposes docs__lookup and so registers that same name", collision, pluginServer)
	}
	if !slices.Contains(prefixes, pluginServer) {
		t.Fatalf("%q is missing from %v; the child reloads it from disk, so the assertion above proves nothing", pluginServer, prefixes)
	}

	// Without the plugin layer the name really is CLI-only, so eligibility
	// follows the plugin rather than the name escaping classification.
	bare := otherLayerMCPNamespaces(effective, cliOnly, nil)
	if !cliMCPToolNames(servers, nil, bare, infos, reg)[collision] {
		t.Errorf("%q is not excluded with no plugin config, so the assertion above would hold for any name", collision)
	}
}
