package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// The following tests pin that a role preload reaches a child only when the
// child's own skillAvailableToSession accepts it. The preload is prepared on the
// creating parent, and a frozen descriptor replays the body frozen at creation,
// so construction is the first point either path can ask the receiving session.
// Fixture bodies are opaque sentinels; assertions read the typed skill-context
// document and the structured lifecycle inventory, never prose.

const (
	followablePreloadName     = "opaque-preload"
	secondPreloadName         = "opaque-second-preload"
	gardeningPreloadSentinel  = "GARDENING_PRELOAD_SENTINEL_4455"
	followablePreloadSentinel = "FOLLOWABLE_PRELOAD_SENTINEL_4455"
	secondPreloadSentinel     = "SECOND_PRELOAD_SENTINEL_4455"
)

// renderedPreloadFixture builds the exact rendered document a role preload
// freezes: the typed skill-context envelope wrapping an opaque body.
func renderedPreloadFixture(name, body string) string {
	return skill.Render(skill.LoadedSkill{
		Descriptor: skill.Descriptor{
			CatalogName: name,
			Meta: skill.SkillMeta{
				Name:        name,
				Description: "fixture " + name,
				SkillFile:   "/fixture/" + name + "/SKILL.md",
				Dir:         "/fixture/" + name,
			},
		},
		Body: body,
	}).Content
}

func preloadMetadataFixture(name string) schema.FrozenSkillPreload {
	return schema.FrozenSkillPreload{
		Name:           name,
		Description:    "fixture " + name,
		Source:         "/fixture/" + name + "/SKILL.md",
		FileDigest:     "file-" + name,
		RenderedDigest: "rendered-" + name,
	}
}

// preloadTestConfig binds a real memory state root so a root session in these
// tests can save memory, which is the premise the role preload must pass on the
// creating parent before the child's own capability is checked.
func preloadTestConfig(t *testing.T) SessionConfig {
	t.Helper()
	return SessionConfig{
		StateDir:         t.TempDir(),
		MemoryStateRoot:  t.TempDir(),
		MemoryProjectID:  "fixture-project",
		MaxSubagentDepth: 1,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
}

func writePreloadSkill(t *testing.T, root, name, sentinel string) {
	t.Helper()
	writeSkillMD(t, root, name, "---\nname: "+name+"\ndescription: fixture\n---\n"+sentinel+"\n")
}

func spawnedAgentID(t *testing.T, result any) string {
	t.Helper()
	raw, ok := result.(string)
	if !ok {
		t.Fatalf("spawn result type = %T, want string", result)
	}
	var spawned struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(raw), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	return spawned.AgentID
}

// requirePreloadEnvelopes asserts the provider request carries exactly the
// named typed skill-context documents, by canonical name.
func requirePreloadEnvelopes(t *testing.T, req llm.Request, want ...string) {
	t.Helper()
	var got []string
	for _, envelope := range requestSkillEnvelopes(t, req) {
		got = append(got, envelope.Doc.Name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("delivered skill envelopes = %v, want %v", got, want)
	}
}

// requirePreloadRecords asserts the lifecycle inventory's preload half names
// exactly want and no other preload is present.
func requirePreloadRecords(t *testing.T, s *Session, want ...string) {
	t.Helper()
	var got []string
	for name, entry := range lifecycleInventory(s) {
		if entry.Preload != nil {
			got = append(got, name)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("inventory preload records = %v, want %v", got, want)
	}
}

// A save-capable root prepares a gardening-memory role preload on itself, so
// the parent gate passes. The child delegate never has the save tools (#4190),
// so the preload must not reach it; the followable preload beside it must.
func TestRolePreloadDropsUnfollowableSkillForIncapableDelegate(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	writePreloadSkill(t, root, followablePreloadName, followablePreloadSentinel)
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		return finalResponse("done")
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
		withConfig(preloadTestConfig(t)))
	if !s.memorySaveInstructionsEnabled() {
		t.Fatal("fixture parent must be able to save memory, or the role preload never passes the parent gate")
	}
	s.pluginAgents["preload-role"] = plugin.Agent{
		Name: "preload-role", PluginName: "test", AllTools: true,
		SystemPrompt: "preload role",
		Skills:       []string{gardeningMemorySkillName, followablePreloadName},
	}
	result, err := s.spawnAgent(context.Background(), "child task", "", "", 0, "preload-role", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	sub := s.getSub(spawnedAgentID(t, result))
	if sub == nil {
		t.Fatalf("spawned subagent is not tracked")
	}
	select {
	case <-sub.done:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted in-process adapter, no real I/O.
		t.Fatal("timed out waiting for the preload child")
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly the child's one request", len(requests))
	}
	if sub.sess.skillAvailableToSession(gardeningMemorySkillName) {
		t.Fatal("fixture delegate unexpectedly can save memory")
	}
	requirePreloadEnvelopes(t, requests[0], followablePreloadName)
	requirePreloadRecords(t, sub.sess, followablePreloadName)
}

// A frozen descriptor restores the body verbatim through
// prepareSubagentRunFromSelection. The restoring delegate cannot follow
// gardening-memory, so the body and its preload record must both be dropped
// while the followable frozen preload survives.
func TestFrozenRolePreloadDropsUnfollowableSkillForIncapableDelegate(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("done") },
	}}
	root := newSession(t, withAdapter(adapter), withoutGitSnapshot(), withConfig(preloadTestConfig(t)))
	if !root.memorySaveInstructionsEnabled() {
		t.Fatal("fixture root must be able to save memory")
	}
	gardenBody := renderedPreloadFixture(gardeningMemorySkillName, gardeningPreloadSentinel)
	followableBody := renderedPreloadFixture(followablePreloadName, followablePreloadSentinel)
	profile := root.currentProfile()
	childID := identifier.MustNewSessionID()
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		Task:              "frozen preload task",
		AgentType:         "default",
		ResolvedProfileID: profile.ID(),
		ResolvedModel:     profile.Model(),
		FrozenRolePrompt:  "frozen role",
		FrozenSkillNames:  []string{gardeningMemorySkillName, followablePreloadName},
		FrozenSkillBodies: []string{gardenBody, followableBody},
		FrozenSkillMetadata: []schema.FrozenSkillPreload{
			preloadMetadataFixture(gardeningMemorySkillName),
			preloadMetadataFixture(followablePreloadName),
		},
		ToolNameCeiling: []string{"read_file", "use_skill", "communicate"},
		WorkingDir:      root.currentEnv().WorkingDirectory(),
		LocalEnvPolicy:  localEnvPolicyName(root.currentEnv()),
		Config:          root.cfg.toSnapshot(),
	}
	selection := subagentModelSelection{profile: profile}
	ctx := context.WithValue(context.Background(), delegateChildSessionIDContextKey{}, childID)
	ctx = context.WithValue(ctx, delegatePreparedEnvironmentContextKey{}, delegatePreparedEnvironment{
		env:              root.currentEnv(),
		stableController: true,
	})
	prepared, err := root.prepareStableDelegateRun(ctx, descriptor, false, selection, nil)
	if err != nil {
		t.Fatalf("prepareStableDelegateRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	})
	child := prepared.sub.sess
	if child.skillAvailableToSession(gardeningMemorySkillName) {
		t.Fatal("fixture delegate unexpectedly can save memory")
	}
	if err := root.trackAndLaunchPreparedSubagent(prepared); err != nil {
		t.Fatalf("trackAndLaunchPreparedSubagent: %v", err)
	}
	select {
	case <-prepared.sub.done:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted in-process adapter, no real I/O.
		t.Fatal("timed out waiting for the frozen preload child")
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly the child's one request", len(requests))
	}
	requirePreloadEnvelopes(t, requests[0], followablePreloadName)
	requirePreloadRecords(t, child, followablePreloadName)
}

// A delegate created before this fix can have a gardening-memory preload record
// in its persisted SessionMeta.Skills. Restoring it through the exact
// delegateRuntime.restoreIdle path must drop both the body and the preload
// record, while an ordinary activation record for the same name stays as the
// retained-but-unavailable record #4458 established.
func TestRestoredDelegateDropsUnfollowablePreloadRecord(t *testing.T) {
	t.Parallel()
	gardenMetadata := preloadMetadataFixture(gardeningMemorySkillName)
	followableMetadata := preloadMetadataFixture(followablePreloadName)
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.FrozenSkillNames = []string{gardeningMemorySkillName, followablePreloadName}
		descriptor.FrozenSkillBodies = []string{
			renderedPreloadFixture(gardeningMemorySkillName, gardeningPreloadSentinel),
			renderedPreloadFixture(followablePreloadName, followablePreloadSentinel),
		}
		descriptor.FrozenSkillMetadata = []schema.FrozenSkillPreload{gardenMetadata, followableMetadata}
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return toolCallResponse(communicateCall("done-1", "ok")) },
	}
	ordinary := schema.OrdinarySkillActivation{
		Identity: schema.SkillContentIdentity{
			Name:           gardeningMemorySkillName,
			DeclaredName:   gardeningMemorySkillName,
			Source:         gardenMetadata.Source,
			FileDigest:     gardenMetadata.FileDigest,
			RenderedDigest: gardenMetadata.RenderedDigest,
		},
		Description:  gardenMetadata.Description,
		Route:        "user_selection",
		InvocationID: "skill-op-1",
	}
	childMeta, err := schema.LoadSessionMeta(fixture.stateDir, fixture.childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	childMeta.Skills = &schema.SkillLifecycleSnapshot{
		Revision: 7,
		Inventory: map[string]schema.SkillInventoryEntry{
			gardeningMemorySkillName: {Preload: &gardenMetadata, Ordinary: &ordinary},
			followablePreloadName:    {Preload: &followableMetadata},
		},
	}
	if err := schema.SaveSessionMeta(fixture.stateDir, childMeta); err != nil {
		t.Fatalf("SaveSessionMeta(child): %v", err)
	}
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	// The restored child's construction reads this test config from the parent.
	root.cfg.testOnly.minimalSystemPrompt = false
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	defer func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	}()
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("cold idle delegate was reported retained")
	}
	defer sub.sess.discardRestoredCandidate()
	if sub.sess.skillAvailableToSession(gardeningMemorySkillName) {
		t.Fatal("restored delegate unexpectedly can save memory")
	}
	if _, err := sub.sess.ProcessInput(context.Background(), "restored task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := fixture.adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	requirePreloadEnvelopes(t, requests[0], followablePreloadName)
	requirePreloadRecords(t, sub.sess, followablePreloadName)
	inventory := lifecycleInventory(sub.sess)
	entry, ok := inventory[gardeningMemorySkillName]
	if !ok || entry.Ordinary == nil {
		t.Fatalf("ordinary activation record for %s was not preserved: %+v", gardeningMemorySkillName, entry)
	}
	if entry.Preload != nil {
		t.Fatalf("restored inventory kept the unfollowable preload record: %+v", entry.Preload)
	}
	if inventory[followablePreloadName].Preload == nil {
		t.Fatalf("followable preload record was not preserved: %+v", inventory[followablePreloadName])
	}
}

// A restored inventory record can outlive the spawn carrier that delivered it.
// Construction prunes it by the same capability answer even when no carrier
// names it, so the durable record cannot claim a permanent body this session
// does not have.
func TestRestoredInventoryPreloadPrunedWithoutCarrier(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := NewOpenAIProfile("gpt-5.2")
	parentID := identifier.MustNewSessionID()
	childID := identifier.MustNewSessionID()
	gardenMetadata := preloadMetadataFixture(gardeningMemorySkillName)
	meta := schema.SessionMeta{
		ID:              childID,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		IsSubagent:      true,
		ParentSessionID: parentID,
		Config:          SessionConfig{MaxSubagentDepth: 1}.toSnapshot(),
		Skills: &schema.SkillLifecycleSnapshot{
			Inventory: map[string]schema.SkillInventoryEntry{
				gardeningMemorySkillName: {Preload: &gardenMetadata},
			},
		},
	}
	restored, err := RestoreSessionFromMetaWithConfig(client, profile,
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
			StateDir:        t.TempDir(),
			MemoryStateRoot: t.TempDir(),
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			testOnly: testConfig{
				skipGitSnapshot: true,
				noSyncJobStore:  true,
			},
			spawn: spawnConfig{parentSessionID: parentID},
		})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	if restored.skillAvailableToSession(gardeningMemorySkillName) {
		t.Fatal("restored delegate unexpectedly can save memory")
	}
	if entry, ok := lifecycleInventory(restored)[gardeningMemorySkillName]; ok {
		t.Fatalf("preload record survived without a carrier: %+v", entry)
	}
}

// No production delegate can save memory (#4190), so the capability control
// uses the same construction seam on the shape the product permits: a
// save-capable root whose preload must survive.
func TestRolePreloadKeepsFollowableSkillForSaveCapableSession(t *testing.T) {
	t.Parallel()
	gardenMetadata := preloadMetadataFixture(gardeningMemorySkillName)
	followableMetadata := preloadMetadataFixture(followablePreloadName)
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{
		StateDir:        t.TempDir(),
		MemoryStateRoot: t.TempDir(),
		MemoryProjectID: "fixture-project",
		AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		spawn: spawnConfig{
			activatedSkillNames:  []string{gardeningMemorySkillName, followablePreloadName},
			activatedSkillBodies: []string{renderedPreloadFixture(gardeningMemorySkillName, gardeningPreloadSentinel), renderedPreloadFixture(followablePreloadName, followablePreloadSentinel)},
			frozenSkillMetadata:  []schema.FrozenSkillPreload{gardenMetadata, followableMetadata},
		},
	}))
	if !s.memorySaveInstructionsEnabled() {
		t.Fatal("fixture root must be able to save memory")
	}
	requirePreloadEnvelopes(t, llm.Request{Messages: []llm.Message{llm.System(s.cachedSystemPrompt)}},
		gardeningMemorySkillName, followablePreloadName)
	requirePreloadRecords(t, s, gardeningMemorySkillName, followablePreloadName)
	if got := s.cfg.spawn.activatedSkillBodies; len(got) != 2 {
		t.Fatalf("save-capable session preload bodies = %d, want 2", len(got))
	}
}

// A save-capable root's preload slices are inherited by a fresh subagent, and
// the child's own role preload is appended to the SAME slices. The incapable
// child must still receive only the followable preloads, which pins that the
// parallel name and body slices stay aligned through inheritance and append.
func TestInheritedRolePreloadStaysAlignedForIncapableDelegate(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	writePreloadSkill(t, root, secondPreloadName, secondPreloadSentinel)
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		return finalResponse("done")
	}}
	cfg := preloadTestConfig(t)
	cfg.spawn = spawnConfig{
		activatedSkillNames:  []string{gardeningMemorySkillName, followablePreloadName},
		activatedSkillBodies: []string{renderedPreloadFixture(gardeningMemorySkillName, gardeningPreloadSentinel), renderedPreloadFixture(followablePreloadName, followablePreloadSentinel)},
		frozenSkillMetadata:  []schema.FrozenSkillPreload{preloadMetadataFixture(gardeningMemorySkillName), preloadMetadataFixture(followablePreloadName)},
	}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(), withConfig(cfg))
	if !s.memorySaveInstructionsEnabled() {
		t.Fatal("fixture root must be able to save memory")
	}
	s.pluginAgents["second-preload-role"] = plugin.Agent{
		Name: "second-preload-role", PluginName: "test", AllTools: true,
		SystemPrompt: "second preload role",
		Skills:       []string{secondPreloadName},
	}
	result, err := s.spawnAgent(context.Background(), "child task", "", "", 0, "second-preload-role", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	sub := s.getSub(spawnedAgentID(t, result))
	if sub == nil {
		t.Fatalf("spawned subagent is not tracked")
	}
	select {
	case <-sub.done:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted in-process adapter, no real I/O.
		t.Fatal("timed out waiting for the inherited-preload child")
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	requirePreloadEnvelopes(t, requests[0], followablePreloadName, secondPreloadName)
	requirePreloadRecords(t, sub.sess, followablePreloadName, secondPreloadName)
}
