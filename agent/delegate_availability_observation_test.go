package agent

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/llm"
)

// terminalPacketWithTools builds a terminal packet whose metadata carries a
// frozen ceiling and, when observed is non-nil, a recorded observation.
func terminalPacketWithTools(t *testing.T, ceiling, observed []string) *delegatestore.TerminalPacket {
	t.Helper()
	metadata := delegateTerminalPacketMetadata{Tools: ceiling}
	if observed != nil {
		metadata.ObservedTools = &observed
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal packet metadata: %v", err)
	}
	return &delegatestore.TerminalPacket{Metadata: raw}
}

// TestSendAndStatusAgreeOnAvailabilityForOneGeneration is the medium 1 review
// finding: a completed send and a timed-out send for the SAME generation used to
// answer differently, because completion overwrote the observed list with the
// packet's frozen ceiling. Availability must be one answer per generation,
// whether the wait completed, timed out, or the runtime was later reclaimed.
func TestSendAndStatusAgreeOnAvailabilityForOneGeneration(t *testing.T) {
	t.Parallel()
	const (
		unregistered = "svc__echo"
		registered   = "svc__lookup"
	)
	ceiling := []string{"read_file", unregistered, registered}
	// The child registered only one of the two same-server names: exactly the
	// case a floor of the ceiling cannot describe.
	observed := []string{"read_file", registered}

	t.Run("a completed send reports the observation, not the ceiling", func(t *testing.T) {
		t.Parallel()
		result := sendMessageResult{Tools: []string{"read_file"}}
		populateDelegateSendResult(&result, *terminalPacketWithTools(t, ceiling, observed))
		if slices.Contains(result.Tools, unregistered) {
			t.Errorf("completed send reported %q, which the child never registered: %v", unregistered, result.Tools)
		}
		if !slices.Contains(result.Tools, registered) {
			t.Errorf("completed send dropped %q, which the child did register: %v", registered, result.Tools)
		}
	})

	t.Run("status after reclamation reads the same observation", func(t *testing.T) {
		t.Parallel()
		// The runtime is gone, so the fold's durable record of what this
		// generation registered is what answers — tagged with the generation it
		// belongs to, which is the same generation the row reports.
		snapshot := delegateSnapshot{
			descriptor:         delegatestore.Descriptor{ToolNameCeiling: ceiling},
			generation:         2,
			latestPacket:       terminalPacketWithTools(t, ceiling, observed),
			settledObservation: observed,
			observedGeneration: 2,
		}
		status := projectDelegateStatus(time.Now(), snapshot)
		if slices.Contains(status.Tools, unregistered) {
			t.Errorf("status after reclamation reported %q, which the child never registered: %v", unregistered, status.Tools)
		}
		if !slices.Contains(status.Tools, registered) {
			t.Errorf("status after reclamation dropped %q, which the child did register: %v", registered, status.Tools)
		}
	})

	t.Run("a completed send and a timed-out send agree", func(t *testing.T) {
		t.Parallel()
		// Timed out: the pre-packet path, which reads the live observation.
		timedOut := sendMessageResult{Tools: append([]string(nil), observed...)}
		// Completed: the same generation, now carrying its packet.
		completed := sendMessageResult{}
		populateDelegateSendResult(&completed, *terminalPacketWithTools(t, ceiling, observed))
		if !reflect.DeepEqual(timedOut.Tools, completed.Tools) {
			t.Errorf("the same generation answered differently: timed out %v, completed %v", timedOut.Tools, completed.Tools)
		}
	})

	t.Run("a packet with no observation falls back to its ceiling", func(t *testing.T) {
		t.Parallel()
		result := sendMessageResult{}
		populateDelegateSendResult(&result, *terminalPacketWithTools(t, ceiling, nil))
		if !slices.Contains(result.Tools, unregistered) {
			t.Errorf("a legacy packet lost its ceiling: %v", result.Tools)
		}
	})

	t.Run("an observed empty registry reports nothing", func(t *testing.T) {
		t.Parallel()
		result := sendMessageResult{}
		populateDelegateSendResult(&result, *terminalPacketWithTools(t, ceiling, []string{}))
		if len(result.Tools) != 0 {
			t.Errorf("a generation that registered nothing reported %v", result.Tools)
		}
	})
}

// TestJobListAndStatusReportOneToolAnswer is the review's low: job_list listed a
// tool that job_status omitted for the same delegate. Both must read the one
// status projection.
func TestJobListAndStatusReportOneToolAnswer(t *testing.T) {
	t.Parallel()
	ceiling := []string{"read_file", "svc__echo"}
	observed := []string{"read_file"}
	snapshot := delegateSnapshot{
		descriptor:    delegatestore.Descriptor{Task: "same answer", ToolNameCeiling: ceiling},
		observedTools: observed,
	}
	status := projectDelegateStatus(time.Now(), snapshot)
	item := projectDelegateListItem(time.Now(), delegateVisibleRow{snapshot: snapshot})
	if !reflect.DeepEqual(status.Tools, item.Tools) {
		t.Errorf("job_status reported %v while job_list reported %v for one delegate", status.Tools, item.Tools)
	}
	if slices.Contains(item.Tools, "svc__echo") {
		t.Errorf("job_list reported %q, which the child never registered: %v", "svc__echo", item.Tools)
	}
}

// TestCLIMCPClassificationRequiresItsOwnProjectEvidence is the medium 2 review
// finding: the startup exclusion was applied forever, so a session that switched
// branches IN PLACE — no environment swap — kept denying a tool the fresh child
// discovered through the new project config.
func TestCLIMCPClassificationRequiresItsOwnProjectEvidence(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	// A canonical root: git reports the resolved path, so a fixture built on a
	// symlinked temp directory (Darwin's /var) would compare a canonical path
	// against an unresolved one and fail for a reason the test is not about.
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)

	const (
		cliTool  = "svc__echo"
		ordinary = "read_file"
	)
	ceiling := []string{cliTool, ordinary}
	newClassified := func() *Session {
		// The evidence names a project path, and the check now confirms that path
		// is still the one this workspace resolves to. A session with no
		// environment resolves to none, so the fixture must carry the workspace its
		// evidence describes — which is also what makes the "in place" cases below
		// an in-place change rather than a different workspace.
		s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
		s.setManagerOnlyTools(nil)
		// The classification recorded against the file as it stands now.
		// The evidence comes from discovery, as production records it: both
		// layers, each with the bytes that were parsed. A hand-built project-only
		// evidence would leave the global layer unrecorded, and an unrecorded
		// layer never verifies — which is the correct rule, since the snapshot
		// cannot claim a layer it never read.
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(root), nil, nil)
		if err != nil {
			panic(err)
		}
		s.setCLIMCPServers(nil, nil, discovered)
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		// The ceiling build reads a verdict that was resolved outside s.mu rather
		// than doing the check itself under the lock, so the fixture resolves it
		// the way both production entry points do. A verdict never resolved
		// answers "not fresh", which would make every case below pass for a
		// reason the test is not about.
		s.refreshCLIEvidenceFreshness()
		return s
	}
	// ceiling mirrors the order a model request uses: revalidate the evidence
	// outside the session lock, then build the ceiling from the stored verdict.
	// Calling removeChildUnreachableTools alone would sample a verdict resolved
	// before the file changed, which production never does.
	ceilingOf := func(s *Session, names []string) []string {
		s.refreshCLIEvidenceFreshness()
		return s.removeChildUnreachableTools(names, "")
	}

	t.Run("unchanged project config still excludes", func(t *testing.T) {
		got := ceilingOf(newClassified(), ceiling)
		if slices.Contains(got, cliTool) {
			t.Errorf("the classification's own evidence still describes the workspace, so %q must stay excluded: %v", cliTool, got)
		}
	})

	t.Run("an in-place project config change leaves the name eligible", func(t *testing.T) {
		s := newClassified()
		// The branch switched under the same git root: the file now declares a
		// server the startup snapshot never saw, and no env swap ran.
		writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)
		got := ceilingOf(s, ceiling)
		if !slices.Contains(got, cliTool) {
			t.Errorf("the project config changed, so reachability is unknown and %q must stay eligible: %v", cliTool, got)
		}
		if !slices.Contains(got, ordinary) {
			t.Errorf("the exclusion dropped an ordinary tool: %v", got)
		}
	})

	t.Run("a deleted project config leaves the name eligible", func(t *testing.T) {
		s := newClassified()
		if err := os.Remove(projectPath); err != nil {
			t.Fatalf("remove project config: %v", err)
		}
		got := ceilingOf(s, ceiling)
		if !slices.Contains(got, cliTool) {
			t.Errorf("the project config was removed, so %q must stay eligible: %v", cliTool, got)
		}
	})

	// The Manager-only exclusion is unaffected: it rests on composition, not on
	// any project configuration.
	t.Run("the manager half ignores project evidence", func(t *testing.T) {
		s := newClassified()
		s.setManagerOnlyTools(map[string]bool{"session_control": true})
		writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/c"}}}`, 0o644)
		got := ceilingOf(s, []string{"session_control", cliTool})
		if slices.Contains(got, "session_control") {
			t.Errorf("a project-config change re-admitted the Manager-only tool: %v", got)
		}
	})
}

// TestPluginShadowComparisonUsesTheMCPNamespace is the medium 3 review finding: a
// plugin server and a CLI alias whose names differ only by hyphenation register
// the same tool names, so comparing raw names left the entry unshadowed and the
// ceiling excluded a name the child registers through the plugin.
func TestPluginShadowComparisonUsesTheMCPNamespace(t *testing.T) {
	t.Parallel()
	const (
		pluginServer = "plugin_team-tools_svc"
		cliAlias     = "plugin_team_tools_svc"
	)
	// Registration replaces hyphens with underscores before namespacing, so these
	// two configured names are one namespace. Stated here so the fixture cannot
	// silently stop exercising the collision.
	if strings.ReplaceAll(pluginServer, "-", "_") != strings.ReplaceAll(cliAlias, "-", "_") {
		t.Fatalf("fixture is wrong: %q and %q do not share a namespace", pluginServer, cliAlias)
	}
	// The name registration actually derives: hyphens become underscores before
	// the server name is namespaced onto the tool.
	cliTool := strings.ReplaceAll(cliAlias, "-", "_") + "__echo"

	s := &Session{pluginMCPConfigs: []mcpconfig.ServerConfig{{Name: pluginServer, Command: "/bin/plugin"}}}
	s.recordCLIMCPServers([]mcpconfig.CLIOnlyServer{{Config: mcpconfig.ServerConfig{Name: cliAlias, Command: "/bin/cli"}}}, mcpconfig.ConfigEvidence{})
	servers, shadowed := s.cliMCPServersSnapshot()
	if !servers[cliAlias] {
		t.Fatalf("fixture did not record the CLI alias: %v", servers)
	}
	if !shadowed[cliAlias] {
		t.Fatalf("the CLI alias shadows a plugin server in the same namespace but was not marked shadowed: %v", shadowed)
	}

	reg := tool.NewRegistry()
	if err := reg.Register(regTool(llm.ToolDefinition{Name: cliTool, Description: "fixture"})); err != nil {
		t.Fatalf("register %q: %v", cliTool, err)
	}
	excluded := cliMCPToolNames(servers, shadowed, nil, []mcpconfig.ServerInfo{
		{Name: cliAlias, Status: "connected", Tools: []string{cliTool}},
	}, reg)
	if excluded[cliTool] {
		t.Errorf("the ceiling excluded %q, which the child registers through the plugin it still loads", cliTool)
	}

	// The other direction: a plugin server in a DIFFERENT namespace must not
	// shadow the CLI entry.
	other := &Session{pluginMCPConfigs: []mcpconfig.ServerConfig{{Name: "plugin_other_svc", Command: "/bin/plugin"}}}
	other.recordCLIMCPServers([]mcpconfig.CLIOnlyServer{{Config: mcpconfig.ServerConfig{Name: cliAlias, Command: "/bin/cli"}}}, mcpconfig.ConfigEvidence{})
	_, otherShadowed := other.cliMCPServersSnapshot()
	if otherShadowed[cliAlias] {
		t.Errorf("a plugin server in another namespace shadowed %q, which would hide a tool the child cannot reach", cliAlias)
	}
}

// TestTerminalMetadataOmitsAnAbsentObservation pins the wire shape of the
// terminal packet's observation. This metadata is also the text of a delegate
// notification, so the three states must be distinguishable while a generation
// with no observation adds no key at all to that text:
//
//   - no observation: the key is absent, so the notification reads exactly as it
//     did before the field existed;
//   - registered nothing: the key is present as [], which is a fact worth
//     reporting and must not decay into "no observation";
//   - registered tools: the key lists them.
//
// A regression here is invisible to a test that only reads the decoded struct,
// because a null and an absent key both decode to a nil pointer.
func TestTerminalMetadataOmitsAnAbsentObservation(t *testing.T) {
	t.Parallel()
	const absentToken = `"observed_tools"`
	encode := func(observed *[]string) string {
		t.Helper()
		raw, err := json.Marshal(delegateTerminalPacketMetadata{ObservedTools: observed})
		if err != nil {
			t.Fatalf("marshal terminal metadata: %v", err)
		}
		return string(raw)
	}

	if got := encode(nil); strings.Contains(got, absentToken) {
		t.Errorf("metadata for a generation with no observation carries %s, which reaches the notification text: %s", absentToken, got)
	}

	empty := []string{}
	got := encode(&empty)
	if !strings.Contains(got, `"observed_tools":[]`) {
		t.Errorf("metadata for a child that registered nothing lost the present empty list: %s", got)
	}

	tools := []string{"svc__lookup"}
	if got := encode(&tools); !strings.Contains(got, `"observed_tools":["svc__lookup"]`) {
		t.Errorf("metadata for a child that registered a tool did not list it: %s", got)
	}
}

// TestCallableToolsSnapshotExcludesUnfilteredRegistrations pins that availability
// readers report the post-policy set, not raw registrations.
//
// An MCP retry registers a recovered server's tools before retryFailedMCP
// removes the ones this session may not call. A parent status read or steering
// receipt taken inside that interval would list a tool the child drops before
// its next model request, which is a report of a capability the child does not
// have.
//
// The snapshot is published after filtering, so the contract is: a name added to
// the registry without a publish stays invisible; a publish makes the filtered
// set visible.
func TestCallableToolsSnapshotExcludesUnfilteredRegistrations(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{}))
	register := func(name string) {
		t.Helper()
		sess.mu.Lock()
		defer sess.mu.Unlock()
		if err := sess.reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{Name: name, Description: name},
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				return "ok", nil
			},
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	sess.mu.Lock()
	sess.publishCallableTools()
	before := sess.callableToolNames()
	sess.mu.Unlock()

	// The retry's unfiltered interval: registered, not yet published.
	register("svc__recovered")
	sess.mu.Lock()
	during := sess.callableToolNames()
	sess.mu.Unlock()
	if during["svc__recovered"] {
		t.Error("a tool registered but not yet filtered was reported as callable, so a status read can name a capability the child is about to lose")
	}
	if !maps.Equal(before, during) {
		t.Errorf("an unpublished registration changed what availability reports: %v then %v", before, during)
	}

	// Once the retry finishes its filtering and publishes, the name appears.
	sess.mu.Lock()
	sess.reg.Remove("svc__recovered")
	sess.publishCallableTools()
	after := sess.callableToolNames()
	sess.mu.Unlock()
	if after["svc__recovered"] {
		t.Error("a tool removed by policy was published as callable")
	}
	register("svc__kept")
	sess.mu.Lock()
	sess.publishCallableTools()
	kept := sess.callableToolNames()
	sess.mu.Unlock()
	if !kept["svc__kept"] {
		t.Error("a tool the policy kept is missing from the published set, so a real capability is hidden")
	}
}

// TestPublishedCallableOrderIsSortedAndShared proves the order a caller reads is
// the one computed at PUBLISH time, not at read time.
//
// The tree controller reads these names while holding its own mutex, and it
// builds a snapshot per live delegate per update. Sorting there repeats work
// that grows with an MCP-heavy child's registry, and starts, stops and
// settlements wait behind the same lock. Publishing the order makes those reads
// a copy.
//
// The assertion is that two successive reads return the SAME backing array. A
// read-time sort would allocate a fresh slice each call, so identity is the
// property that distinguishes the two designs — a test that only checked sorted
// order would pass either way.
func TestPublishedCallableOrderIsSortedAndShared(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{}))
	names := []string{"svc__zeta", "svc__alpha", "zeta_plain", "alpha_plain"}
	sess.mu.Lock()
	for _, name := range names {
		if err := sess.reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{Name: name, Description: name},
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				return "ok", nil
			},
		}); err != nil {
			sess.mu.Unlock()
			t.Fatalf("register %s: %v", name, err)
		}
	}
	sess.publishCallableTools()
	sess.mu.Unlock()

	sess.mu.Lock()
	first := sess.callableToolOrder()
	sess.mu.Unlock()
	if !slices.IsSorted(first) {
		t.Fatalf("the published order is not sorted: %v", first)
	}
	for _, name := range names {
		if !slices.Contains(first, name) {
			t.Fatalf("the published order omitted %q: %v", name, first)
		}
	}

	sess.mu.Lock()
	second := sess.callableToolOrder()
	sess.mu.Unlock()
	if len(first) != len(second) || (len(first) > 0 && &first[0] != &second[0]) {
		t.Errorf("each order read allocated a new slice, so the sort is still happening at read time rather than publish time")
	}
}

// TestPublishedEmptyObservationIsNotUnavailable pins low 3's distinction, which
// publishing a nil order collapsed.
//
// An observed empty registry is a FACT: this generation registered nothing. An
// unpublished snapshot is the absence of any observation. Readers use nil for
// the second, so a nil order for the first makes a child that registered nothing
// fall back to the frozen ceiling and report tools it never had — the original
// bug, arriving through the availability path instead of the ceiling path.
func TestPublishedEmptyObservationIsNotUnavailable(t *testing.T) {
	t.Parallel()
	// Before anything is published the observation is unavailable. A bare
	// session, not a started one, so nothing has published on its behalf.
	unobserved := &Session{reg: tool.NewRegistry()}
	unobserved.mu.Lock()
	unpublished := unobserved.callableTools.Load()
	unobserved.mu.Unlock()
	if unpublished != nil {
		t.Fatalf("fixture is wrong: a snapshot was published before the test published one")
	}

	// Publish an EMPTY registry and confirm the order is present and empty,
	// not absent. A session built with no tools registered is the honest
	// fixture for "this generation registered nothing".
	sess := &Session{reg: tool.NewRegistry()}
	sess.mu.Lock()
	sess.publishCallableTools()
	order := sess.callableToolOrder()
	names := sess.callableToolNames()
	sess.mu.Unlock()

	if order == nil {
		t.Error("an observed empty registry published a nil order, so readers cannot tell it from an unavailable observation")
	}
	if len(order) != 0 {
		t.Errorf("an empty registry published names: %v", order)
	}
	if names == nil {
		t.Error("an observed empty registry published a nil set")
	}
	if len(names) != 0 {
		t.Errorf("an empty registry published names: %v", names)
	}
}
