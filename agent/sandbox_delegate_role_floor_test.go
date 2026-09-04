package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

func newReadOnlyRoleFloorSession(t *testing.T, lane string, facts sandbox.HostFacts) (*Session, *fakeAdapter) {
	t.Helper()
	parentClient := llm.NewClient()
	parentClient.Register(&fakeAdapter{name: "openai"})
	childAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
		},
	}
	childClient := llm.NewClient()
	childClient.Register(childAdapter)
	registerTestSessionNamer(childClient)
	s := newSession(t,
		withClient(parentClient),
		withDir(lane),
		withConfig(SessionConfig{
			StateDir:         packageFixtureTempDir(t, "role-floor-*"),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				childClientFactory:  func() *llm.Client { return childClient },
				sandboxProber:       sandbox.FakeProber{Facts: facts},
			},
		}),
	)
	t.Cleanup(s.Close)
	s.pluginAgents["test:readonly-shell"] = plugin.Agent{
		Name:        "readonly-shell",
		Description: "structured read-only shell scope",
		Model:       "inherit",
		Tools:       []string{"read_file", "shell"},
		PluginName:  "test",
	}
	s.pluginAgents["test:filtered-mutator"] = plugin.Agent{
		Name:        "filtered-mutator",
		Description: "declares a root-only mutation tool unavailable to this leaf",
		Model:       "inherit",
		Tools:       []string{"read_file", "shell", "manage_worktree"},
		PluginName:  "test",
	}
	s.pluginAgents["test:effective-mutator"] = plugin.Agent{
		Name:        "effective-mutator",
		Description: "retains a registered workspace mutation tool",
		Model:       "inherit",
		Tools:       []string{"read_file", "write_file", "shell"},
		PluginName:  "test",
	}
	return s, childAdapter
}

func assertReadOnlyDelegateBackend(t *testing.T, child *Session, wantMode sandbox.Mode, wantWriteBlocked bool) {
	t.Helper()
	if child == nil {
		t.Fatal("delegate session is nil")
	}
	local, ok := child.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("delegate environment = %T, want local", child.currentEnv())
	}
	if local.Sandbox == nil || !local.Sandbox.Enforced() || local.Wrapper == nil {
		t.Fatalf("delegate boundary = sandbox:%+v wrapper:%v, want enforced kernel backend", local.Sandbox, local.Wrapper)
	}
	if local.Sandbox.Mode != wantMode || local.Sandbox.WriteBlocked != wantWriteBlocked {
		t.Fatalf("delegate sandbox = mode:%s writeBlocked:%t, want mode:%s writeBlocked:%t", local.Sandbox.Mode, local.Sandbox.WriteBlocked, wantMode, wantWriteBlocked)
	}
	if local.Sandbox.Backend == sandbox.BackendNone {
		t.Fatal("delegate sandbox selected no kernel backend")
	}
	if len(local.Sandbox.FileTool.WriteRoots) != 0 || len(local.Sandbox.Spawned.WriteRoots) != 0 {
		t.Fatalf("delegate retained persistent writes: file=%v spawned=%v", local.Sandbox.FileTool.WriteRoots, local.Sandbox.Spawned.WriteRoots)
	}
	if local.Wrapper.SessionTmp() == "" {
		t.Fatal("delegate kernel backend has no private writable scratch")
	}
}

func setWriteBlockedRestrictedParent(t *testing.T, parent *Session, facts sandbox.HostFacts, cwd string) {
	t.Helper()
	network := true
	resolved, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode:         sandbox.ModeRestricted,
		WriteBlocked: true,
		Network:      &network,
	}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve write-blocked parent: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &resolved
	parent.swapEnvAndRefresh(env)
}

func TestCreateDelegate_ReadOnlyRoleSandboxRequestFloor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		allowed bool
	}{
		{name: "explicit off", mode: "off"},
		{name: "explicit workspace-write", mode: "workspace-write"},
		{name: "explicit restricted", mode: "restricted"},
		{name: "explicit read-only", mode: "read-only", allowed: true},
		{name: "omitted", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			s, childAdapter := newReadOnlyRoleFloorSession(t, lane, sbxBwrapFacts(home))
			before := len(s.delegateController.Snapshot().rows)
			result := s.createDelegate(context.Background(), delegateArgs{
				Task:      "inspect without workspace mutation",
				AgentType: "test:readonly-shell",
				Sandbox:   tc.mode,
			})
			if !tc.allowed {
				if result.Err == nil || !strings.Contains(result.Err.Error(), "invalid_request:") {
					t.Fatalf("unsafe role sandbox request error = %v, want structured invalid_request", result.Err)
				}
				if result.DelegateID != "" || result.ChildSessionID != "" {
					t.Fatalf("unsafe role sandbox request minted identity: delegate=%q child=%q", result.DelegateID, result.ChildSessionID)
				}
				if after := len(s.delegateController.Snapshot().rows); after != before {
					t.Fatalf("unsafe role sandbox request changed durable admissions: before=%d after=%d", before, after)
				}
				if got := len(childAdapter.Requests()); got != 0 {
					t.Fatalf("unsafe role sandbox request reached provider %d times", got)
				}
				return
			}
			if result.Err != nil {
				t.Fatalf("compatible role sandbox request: %v", result.Err)
			}
			child := s.getSub(result.ChildSessionID)
			if child == nil {
				t.Fatalf("allowed delegate %q is not admitted", result.ChildSessionID)
			}
			assertReadOnlyDelegateBackend(t, child.sess, sandbox.ModeReadOnly, false)
		})
	}
}

func TestCreateDelegate_FilteredMutationToolUsesEffectiveCeilingFloor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		allowed bool
	}{
		{name: "explicit off", mode: "off"},
		{name: "explicit workspace-write", mode: "workspace-write"},
		{name: "explicit restricted", mode: "restricted"},
		{name: "explicit read-only", mode: "read-only", allowed: true},
		{name: "omitted", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			s, childAdapter := newReadOnlyRoleFloorSession(t, lane, sbxBwrapFacts(home))
			before := len(s.delegateController.Snapshot().rows)
			result := s.createDelegate(context.Background(), delegateArgs{
				Task:                "inspect without workspace mutation",
				AgentType:           "test:filtered-mutator",
				DelegationAllowance: new(0),
				Sandbox:             tc.mode,
			})
			if !tc.allowed {
				if result.Err == nil || !strings.Contains(result.Err.Error(), "invalid_request:") {
					t.Fatalf("unsafe effective-ceiling request error = %v, want structured invalid_request", result.Err)
				}
				if result.DelegateID != "" || result.ChildSessionID != "" {
					t.Fatalf("unsafe effective-ceiling request minted identity: delegate=%q child=%q", result.DelegateID, result.ChildSessionID)
				}
				if after := len(s.delegateController.Snapshot().rows); after != before {
					t.Fatalf("unsafe effective-ceiling request changed durable admissions: before=%d after=%d", before, after)
				}
				if got := len(childAdapter.Requests()); got != 0 {
					t.Fatalf("unsafe effective-ceiling request reached provider %d times", got)
				}
				return
			}
			if result.Err != nil {
				t.Fatalf("compatible effective-ceiling request: %v", result.Err)
			}
			if result.DelegateID == "" || result.ChildSessionID == "" {
				t.Fatalf("compatible effective-ceiling request omitted identity: %+v", result)
			}
			if after := len(s.delegateController.Snapshot().rows); after != before+1 {
				t.Fatalf("compatible effective-ceiling durable admissions: before=%d after=%d", before, after)
			}
			descriptor := delegateAggregateSnapshot(t, s.delegateController, result.DelegateID).Descriptor
			if hasString(descriptor.ToolNameCeiling, "manage_worktree") || hasString(descriptor.ToolNameCeiling, "write_file") || hasString(descriptor.ToolNameCeiling, "edit_file") || hasString(descriptor.ToolNameCeiling, "apply_patch") {
				t.Fatalf("committed effective ceiling retained workspace mutation: %v", descriptor.ToolNameCeiling)
			}
			if !hasString(descriptor.ToolNameCeiling, "read_file") || !hasString(descriptor.ToolNameCeiling, "shell") {
				t.Fatalf("committed effective ceiling lost declared inspection tools: %v", descriptor.ToolNameCeiling)
			}
			if descriptor.Sandbox == nil || descriptor.Sandbox.Mode != sandbox.ModeReadOnly.String() || descriptor.Sandbox.WriteBlocked {
				t.Fatalf("committed effective-ceiling sandbox = %+v, want read-only", descriptor.Sandbox)
			}
			child := s.getSub(result.ChildSessionID)
			if child == nil {
				t.Fatalf("compatible delegate %q is not resident", result.ChildSessionID)
			}
			assertReadOnlyDelegateBackend(t, child.sess, sandbox.ModeReadOnly, false)
			for name := range child.sess.reg.RegisteredNames() {
				if !hasString(descriptor.ToolNameCeiling, name) {
					t.Fatalf("runtime tool %q exceeds committed effective ceiling %v", name, descriptor.ToolNameCeiling)
				}
			}
		})
	}
}

func TestCreateDelegate_EffectiveMutationToolPreservesWorkspaceWrite(t *testing.T) {
	lane, home := sbxLane(t)
	s, _ := newReadOnlyRoleFloorSession(t, lane, sbxBwrapFacts(home))
	result := s.createDelegate(context.Background(), delegateArgs{
		Task:      "modify the workspace",
		AgentType: "test:effective-mutator",
		Sandbox:   sandbox.ModeWorkspaceWrite.String(),
	})
	if result.Err != nil {
		t.Fatalf("effective mutating role request: %v", result.Err)
	}
	descriptor := delegateAggregateSnapshot(t, s.delegateController, result.DelegateID).Descriptor
	if !hasString(descriptor.ToolNameCeiling, "write_file") {
		t.Fatalf("committed effective ceiling = %v, want write_file", descriptor.ToolNameCeiling)
	}
	if descriptor.Sandbox == nil || descriptor.Sandbox.Mode != sandbox.ModeWorkspaceWrite.String() || descriptor.Sandbox.WriteBlocked {
		t.Fatalf("committed mutating sandbox = %+v, want workspace-write", descriptor.Sandbox)
	}
	child := s.getSub(result.ChildSessionID)
	if child == nil {
		t.Fatalf("mutating delegate %q is not resident", result.ChildSessionID)
	}
	local, ok := child.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("mutating runtime environment = %T, want local", child.sess.currentEnv())
	}
	if local.Sandbox == nil || local.Sandbox.WriteBlocked || local.Sandbox.Mode != sandbox.ModeWorkspaceWrite {
		t.Fatalf("mutating runtime boundary = %+v, want workspace-write", local.Sandbox)
	}
	if !rootGrantsAny(local.Sandbox.FileTool.WriteRoots, lane) || !rootGrantsAny(local.Sandbox.Spawned.WriteRoots, lane) {
		t.Fatalf("mutating runtime omitted workspace write boundary: file=%v spawned=%v", local.Sandbox.FileTool.WriteRoots, local.Sandbox.Spawned.WriteRoots)
	}
}

func TestRestoreDelegate_ReadOnlyRoleSandboxFloor(t *testing.T) {
	for _, tc := range []struct {
		name             string
		snapshot         *delegatestore.SandboxSnapshot
		allowed          bool
		wantMode         sandbox.Mode
		wantWriteBlocked bool
	}{
		{name: "explicit off", snapshot: &delegatestore.SandboxSnapshot{Mode: "off"}},
		{name: "off with unwritable write-blocked option", snapshot: &delegatestore.SandboxSnapshot{Mode: "off", WriteBlocked: true}},
		{name: "explicit workspace-write", snapshot: &delegatestore.SandboxSnapshot{Mode: "workspace-write"}},
		{name: "explicit restricted", snapshot: &delegatestore.SandboxSnapshot{Mode: "restricted"}},
		{name: "explicit read-only", snapshot: &delegatestore.SandboxSnapshot{Mode: "read-only"}, allowed: true, wantMode: sandbox.ModeReadOnly},
		{name: "omitted legacy floor", allowed: true, wantMode: sandbox.ModeReadOnly},
		{name: "restricted write-blocked floor", snapshot: &delegatestore.SandboxSnapshot{Mode: "restricted", WriteBlocked: true}, allowed: true, wantMode: sandbox.ModeRestricted, wantWriteBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
				descriptor.ToolNameCeiling = []string{"communicate", "read_file", "shell"}
				descriptor.Sandbox = tc.snapshot
				descriptor.Config.Sandbox = ""
				descriptor.Config.SandboxNet = nil
				if tc.snapshot != nil {
					descriptor.Config.Sandbox = tc.snapshot.Mode
					if tc.snapshot.Network != nil {
						network := *tc.snapshot.Network
						descriptor.Config.SandboxNet = &network
					}
				}
			})
			facts := sbxBwrapFacts(t.TempDir())
			root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(
				fixture.client,
				fixture.profile,
				fixture.workspace,
				fixture.meta,
				fixture.stateDir,
				testConfig{sandboxProber: sandbox.FakeProber{Facts: facts}},
			)
			if err != nil {
				t.Fatalf("restore root: %v", err)
			}
			defer root.Close()
			reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
			if err != nil {
				t.Fatalf("ReserveStart: %v", err)
			}
			started, err := root.delegateController.CommitStart(reservation)
			if err != nil {
				t.Fatalf("CommitStart: %v", err)
			}
			defer func() {
				_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_complete"))
			}()

			sub, restored, restoreErr := (delegateRuntime{owner: root}).restoreIdle(started)
			if !tc.allowed {
				if restoreErr == nil || !strings.Contains(restoreErr.Error(), "invalid_request:") {
					t.Fatalf("unsafe restored role policy error = %v, want structured invalid_request", restoreErr)
				}
				if sub != nil || restored {
					t.Fatalf("unsafe restored role policy admitted runtime: sub=%v restored=%t", sub, restored)
				}
				if resident := root.subagents.get(fixture.childID); resident != nil {
					t.Fatalf("unsafe restored role policy became resident: %+v", resident)
				}
				if got := len(fixture.adapter.Requests()); got != 0 {
					t.Fatalf("unsafe restored role policy reached provider %d times", got)
				}
				return
			}
			if restoreErr != nil {
				t.Fatalf("restore compatible role policy: %v", restoreErr)
			}
			if sub == nil || !restored {
				t.Fatalf("compatible restored role policy = sub:%v restored:%t", sub, restored)
			}
			defer sub.sess.discardRestoredCandidate(sub.ownsEnv)
			assertReadOnlyDelegateBackend(t, sub.sess, tc.wantMode, tc.wantWriteBlocked)
		})
	}
}

func TestCreateDelegate_WriteBlockedParentCannotBeRelaxed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       string
		network    *bool
		wantReject bool
	}{
		{name: "explicit write-capable mode", mode: "restricted", wantReject: true},
		{name: "net-only preserves write block", network: new(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane, home := sbxLane(t)
			facts := sbxBwrapFacts(home)
			parent, _ := newReadOnlyRoleFloorSession(t, lane, facts)
			setWriteBlockedRestrictedParent(t, parent, facts, lane)
			parent.pluginAgents["test:mutating"] = plugin.Agent{
				Name:       "mutating",
				Model:      "inherit",
				Tools:      []string{"read_file", "write_file", "shell"},
				PluginName: "test",
			}
			before := len(parent.delegateController.Snapshot().rows)
			result := parent.createDelegate(context.Background(), delegateArgs{
				Task:       "exercise the inherited sandbox floor",
				AgentType:  "test:mutating",
				Sandbox:    tc.mode,
				SandboxNet: tc.network,
			})
			if tc.wantReject {
				if result.Err == nil || !strings.Contains(result.Err.Error(), "invalid_request:") {
					t.Fatalf("write-block relaxation error = %v, want structured invalid_request", result.Err)
				}
				if result.DelegateID != "" || result.ChildSessionID != "" || len(parent.delegateController.Snapshot().rows) != before {
					t.Fatalf("write-block relaxation was admitted: %+v", result)
				}
				return
			}
			if result.Err != nil {
				t.Fatalf("compatible net-only request: %v", result.Err)
			}
			child := parent.getSub(result.ChildSessionID)
			if child == nil {
				t.Fatal("net-only delegate was not admitted")
			}
			assertReadOnlyDelegateBackend(t, child.sess, sandbox.ModeRestricted, true)
			if child.sess.currentEnv().(*execenv.LocalExecutionEnvironment).Sandbox.Network {
				t.Fatal("net-only tightening did not disable network")
			}
		})
	}
}

func TestRestoreDelegate_WriteBlockedParentCannotBeRelaxed(t *testing.T) {
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.ToolNameCeiling = []string{"communicate", "read_file", "shell", "write_file"}
		descriptor.Sandbox = &delegatestore.SandboxSnapshot{Mode: "restricted", Network: new(true)}
		descriptor.Config.Sandbox = "restricted"
		descriptor.Config.SandboxNet = new(true)
	})
	facts := sbxBwrapFacts(t.TempDir())
	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(
		fixture.client,
		fixture.profile,
		fixture.workspace,
		fixture.meta,
		fixture.stateDir,
		testConfig{sandboxProber: sandbox.FakeProber{Facts: facts}},
	)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	setWriteBlockedRestrictedParent(t, root, facts, fixture.workspace)
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	defer func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_complete"))
	}()
	sub, restored, restoreErr := (delegateRuntime{owner: root}).restoreIdle(started)
	if restoreErr == nil || !strings.Contains(restoreErr.Error(), "invalid_request:") {
		t.Fatalf("restored write-block relaxation error = %v, want structured invalid_request", restoreErr)
	}
	if sub != nil || restored || root.subagents.get(fixture.childID) != nil {
		t.Fatalf("restored write-block relaxation was admitted: sub=%v restored=%t", sub, restored)
	}
}
