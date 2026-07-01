package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// This file fuzzes the subagent orchestration core in subagents.go:
//
//   - prepareSubagentRun: assembles the frozen, internally-consistent descriptor
//     of a to-be-launched child (role prompt, tool policy, skills, env, tree-slot
//     reservation). It is pure plumbing over Session state with no model call and
//     no goroutine, so it can be fuzzed directly with adversarial arguments +
//     context values and asserted against three oracles below.
//
//   - driveSubagentNotificationTurn: the parent-side drive-down wake that launches
//     ONE EntryNotification turn on a live, idle child. It is concurrency-heavy
//     (tree-counter reservation, sendersWG, a fire-and-forget goroutine) so its
//     oracle is the guard-return contract plus a tree-slot no-leak invariant.
//
// ORACLES (subagents_fuzz):
//   - never-panic: no decodable input panics either target.
//   - internal consistency: a successfully prepared run's descriptor satisfies the
//     invariants the rest of the system relies on (id/session agreement, non-nil
//     run context + tree reservation, a restorable frozen-skill descriptor, grant
//     tools that actually resolve in the child registry, a known env policy, etc.).
//   - determinism: preparing the same run twice on the same parent yields a
//     byte-identical descriptor projection (and identical error), so the frozen
//     descriptor is a pure function of its inputs.
//   - no tree-slot leak: after a drive turn quiesces, the tree counter returns to
//     baseline — a leaked reservation would strand the tree at capacity.
//
// Anti-collision: every top-level identifier is prefixed safz_. The byte->spec
// decode reuses the in-package seqReader cursor and the newChildResponder /
// buildResponse / numResponseKinds vocabulary from the lifecycle harness rather
// than redefining them.

var safzTasks = []string{"", "do the thing", "résumé task", "step 2"}
var safzModels = []string{"", "gpt-5.3", "gpt-5", "  gpt-5.2  ", "no_such_model_zzz", "anthropic/claude-x", "anthropic/boom"}

// safzResolveProfile is the parent's cross-provider resolver. A "prefix/model"
// ref that the base profile treats as a provider switch reaches this; refs
// carrying "boom" error (driving the resolve-error branch), any other resolves
// (driving the cross-provider WithCommunicateOverridesFrom branch).
func safzResolveProfile(ref string) (*provider.Profile, error) {
	if strings.Contains(ref, "boom") {
		return nil, fmt.Errorf("safz resolve error for %q", ref)
	}
	return NewOpenAIProfile("gpt-5"), nil
}

var safzWorkDirs = []string{"", "sub", "nested/dir"}
var safzAgentTypes = []string{
	"", "subagent", "safz_alltools", "safz_tools", "safz_prompt",
	"safz_builtinprompt", "safz_model", "safz_badmodel", "safz_crossmodel", "safz_crossbad",
	"safz_tasks", "safz_skilled", "unknown_xyz", "  subagent  ",
}

// safzSkillBody is the on-disk SKILL.md a parent registers so the agent-skill
// population branch (ResolveSkillContent -> a non-empty body -> frozen skill
// descriptor) is reachable; without a real body ResolveSkillContent returns ""
// and the branch is skipped.
const safzSkillBody = `---
name: safz-skill
description: a fuzz skill
---

The safz fuzz skill body, exercising frozen-skill population and restore.
`

var safzEfforts = []string{"", "low", "high", "  medium  "}
var safzGrantVocab = []string{"read_file", "glob", "grep", "write_file", "delegate", "job_watch", "task_list", "nonsense_tool", ""}

// safzRegisterAgents installs a spread of plugin agent shapes so a fuzzed
// agent_type exercises every branch of the agent-config lookup: all-tools, an
// explicit tool allow-list, a custom (non-builtin) system prompt, a builtin
// prompt, a model override, and a tasked agent. Called on a freshly built parent
// before any concurrent use, so the direct map write is race-free.
func safzRegisterAgents(sess *Session) {
	add := func(a plugin.Agent) { sess.pluginAgents[a.Name] = a }
	add(plugin.Agent{Name: "safz_alltools", AllTools: true, PluginName: "safz"})
	add(plugin.Agent{Name: "safz_tools", Tools: []string{"read_file", "grep"}, PluginName: "safz"})
	add(plugin.Agent{Name: "safz_prompt", SystemPrompt: "custom role prompt", PluginName: "safz", Description: "d"})
	add(plugin.Agent{Name: "safz_builtinprompt", SystemPrompt: "builtin role prompt", PluginName: "builtin"})
	add(plugin.Agent{Name: "safz_model", Model: "gpt-5.3", SystemPrompt: "model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_badmodel", Model: "no_such_model_zzz", SystemPrompt: "bad model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_crossmodel", Model: "anthropic/claude-x", SystemPrompt: "cross model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_crossbad", Model: "anthropic/boom", SystemPrompt: "cross bad model agent", PluginName: "safz"})
	add(plugin.Agent{Name: "safz_tasks", PluginName: "safz", Tasks: []taskpkg.TaskTemplate{{Title: "t1", Prompt: "do step 1"}}})
	add(plugin.Agent{Name: "safz_skilled", SystemPrompt: "skilled role prompt", PluginName: "safz", Skills: []string{"safz-skill"}})
}

// safzRegisterSkill writes a real SKILL.md and registers it on the parent so a
// skilled agent's Skills resolve to a non-empty body (the map write happens on a
// freshly built parent before any concurrent use, so it is race-free).
func safzRegisterSkill(t *testing.T, sess *Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte(safzSkillBody), 0o600); err != nil {
		t.Fatalf("safzRegisterSkill: write skill: %v", err)
	}
	sess.skills = map[string]skill.SkillMeta{"safz-skill": {Name: "safz-skill", SkillFile: path}}
}

// safzNewParent builds a parent Session wired for offline subagent orchestration:
// a scripted parent adapter, a per-child scripted adapter (via the childClient
// factory seam), an injected fake clock, and an event drain so a spawning run
// never blocks on emit. env selects the execution environment (a Local env when
// the target needs the working_dir override + env-policy branches; a DenyEnv when
// a child actually runs, so no real process/disk is ever touched).
func safzNewParent(t *testing.T, clk *agenttest.FakeClock, maxDepth int, childScript []int, env execenv.ExecutionEnvironment) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("parent")
	}})
	cfg := SessionConfig{
		MaxSubagentDepth:      maxDepth,
		clock:                 clk,
		MaxToolRoundsPerInput: 6,
		LLMSleep:              func(context.Context, time.Duration) error { return nil },
		ResolveProfile:        safzResolveProfile,
	}
	cfg.testOnly.childClientFactory = func() *llm.Client {
		cc := llm.NewClient()
		cc.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: newChildResponder(childScript)})
		return cc
	}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("safzNewParent: NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	t.Cleanup(func() {
		sess.Close()
		<-drainDone
	})
	safzRegisterAgents(sess)
	safzRegisterSkill(t, sess)
	return sess
}

// safzSpec is the decoded, self-contained fuzz program: the prepareSubagentRun
// arguments plus the context values the spawn plumbing reads.
type safzSpec struct {
	maxDepth         int
	task             string
	model            string
	workingDir       string
	maxTurns         int
	agentType        string
	reasoningEffort  string
	grantTools       []string
	parentTasks      []taskpkg.TaskTemplate
	toolCallID       string
	itemID           string
	parentJobID      string
	parentDelegateID string
	setDelegAllow    bool
	delegAllow       int
	watchParent      bool
	schema           map[string]any
	childScript      []int
	forceLeaf        bool
}

// safzDecode maps the fuzzer's bytes to a safzSpec. The decode is stable (short
// input decodes deterministically; a longer one is a strict superset), so the
// persisted corpus keeps its meaning across edits — append fields, never reorder.
func safzDecode(data []byte) safzSpec {
	r := &seqReader{data: data}
	s := safzSpec{}
	s.maxDepth = r.intn(4) // 0..3 (0 => allowance 0 => early-error branch)
	s.task = safzTasks[r.intn(len(safzTasks))]
	s.model = safzModels[r.intn(len(safzModels))]
	s.workingDir = safzWorkDirs[r.intn(len(safzWorkDirs))]
	s.maxTurns = r.intn(4) * 100 // 0,100,200,300
	s.agentType = safzAgentTypes[r.intn(len(safzAgentTypes))]
	s.reasoningEffort = safzEfforts[r.intn(len(safzEfforts))]
	for n := r.intn(4); n > 0; n-- {
		s.grantTools = append(s.grantTools, safzGrantVocab[r.intn(len(safzGrantVocab))])
	}
	if r.intn(2) == 1 {
		s.parentTasks = []taskpkg.TaskTemplate{{Title: "pt", Prompt: safzTasks[r.intn(len(safzTasks))]}}
	}
	s.toolCallID = safzMaybeID(r, "call")
	s.itemID = safzMaybeID(r, "item")
	s.parentJobID = safzMaybeID(r, "job")
	s.parentDelegateID = safzMaybeID(r, "del")
	if r.intn(2) == 1 {
		s.setDelegAllow = true
		s.delegAllow = r.intn(4) - 1 // -1..2 (0 => leaf child; >0 => delegating child)
	}
	s.watchParent = r.intn(2) == 1
	if r.intn(2) == 1 {
		s.schema = map[string]any{"type": "object", "x": float64(r.intn(5))}
	}
	for n := r.intn(4); n > 0; n-- {
		s.childScript = append(s.childScript, r.intn(int(numResponseKinds)))
	}
	// Trailing field (appended, never reordered, so the persisted corpus keeps its
	// meaning). The trigger is safe-on-zero (out-of-bytes -> next()==0 -> intn(3)==0
	// -> false) so an existing shorter corpus entry keeps forceLeaf=false and its
	// other-branch reach, while a present byte flips it ~1/3 of the time.
	s.forceLeaf = r.intn(3) == 1
	return s
}

func safzMaybeID(r *seqReader, prefix string) string {
	if r.intn(2) == 0 {
		return ""
	}
	return prefix + "_" + strconv.Itoa(r.intn(4))
}

// safzCtx builds the tool-call context prepareSubagentRun reads its spawn
// plumbing from.
func safzCtx(spec safzSpec) context.Context {
	ctx := context.Background()
	if spec.toolCallID != "" {
		ctx = context.WithValue(ctx, ctxToolCallID, spec.toolCallID)
	}
	if spec.itemID != "" {
		ctx = context.WithValue(ctx, ctxToolItemID, spec.itemID)
	}
	if spec.parentJobID != "" {
		ctx = context.WithValue(ctx, ctxParentJobID, spec.parentJobID)
	}
	if spec.parentDelegateID != "" {
		ctx = context.WithValue(ctx, ctxParentDelegateID, spec.parentDelegateID)
	}
	if spec.setDelegAllow {
		ctx = context.WithValue(ctx, ctxDelegationAllowance, spec.delegAllow)
	}
	if spec.watchParent {
		ctx = context.WithValue(ctx, ctxWatchParent, true)
	}
	if len(spec.schema) > 0 {
		ctx = context.WithValue(ctx, ctxCommunicateOutputSchema, spec.schema)
	}
	return ctx
}

// FuzzSafz_PrepareSubagentRun drives prepareSubagentRun with fuzzed arguments and
// context values against a real parent Session, asserting the never-panic,
// internal-consistency, and determinism oracles.
func FuzzSafz_PrepareSubagentRun(f *testing.F) {
	seeds := [][]byte{
		{},
		{1},
		{0},                      // maxDepth 0 -> allowance error
		{2, 1, 1, 0, 1, 5, 1, 1}, // typed agent + a grant
		{3, 2, 2, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1},    // watch-parent + schema + ctx ids
		{1, 1, 0, 0, 0, 7, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1}, // tasked agent + parent tasks
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		spec := safzDecode(data)
		// Anchor the working_dir override inside the test sandbox. The fuzzed
		// workingDir is a RELATIVE path; passed as-is it would resolve against the
		// process cwd (the package dir) and the child's task store would write task
		// files into the repo tree. Rooting it under the env's temp dir keeps every
		// write inside t.TempDir(), which the framework cleans up.
		envDir := t.TempDir()
		if spec.workingDir != "" {
			spec.workingDir = filepath.Join(envDir, spec.workingDir)
		}
		clk := agenttest.NewFakeClock()
		parent := safzNewParent(t, clk, spec.maxDepth, spec.childScript,
			execenv.NewLocalExecutionEnvironment(envDir))
		if spec.forceLeaf {
			// Drive the allowance gate (a leaf child may not delegate). Applied once
			// to the shared parent so both determinism prepares see the same 0.
			parent.mu.Lock()
			parent.delegationAllowance = 0
			parent.mu.Unlock()
		}
		ctx := safzCtx(spec)

		p1, err1 := safzPrepare(ctx, parent, spec)
		if err1 != nil {
			if p1 != nil {
				t.Fatalf("prepareSubagentRun returned error %v but a non-nil prepared run", err1)
			}
		} else {
			safzCheckConsistency(t, parent, spec, p1)
		}

		// Determinism: a second prepare on the same parent with identical inputs
		// must reproduce the same error disposition and the same descriptor.
		p2, err2 := safzPrepare(ctx, parent, spec)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("determinism: error disposition differs: %v vs %v", err1, err2)
		}
		if err1 != nil && err2 != nil && err1.Error() != err2.Error() {
			t.Fatalf("determinism: error text differs: %q vs %q", err1.Error(), err2.Error())
		}
		if d1, d2 := safzDigest(p1), safzDigest(p2); d1 != d2 {
			t.Fatalf("determinism: descriptor differs:\n first: %s\nsecond: %s", d1, d2)
		}

		safzCleanupPrepared(p1)
		safzCleanupPrepared(p2)
	})
}

func safzPrepare(ctx context.Context, parent *Session, spec safzSpec) (*preparedSubagentRun, error) {
	return parent.prepareSubagentRun(ctx, spec.task, spec.model, spec.workingDir, spec.maxTurns,
		spec.agentType, spec.reasoningEffort, spec.parentTasks, spec.grantTools)
}

// safzCheckConsistency asserts the invariants the launch/resume/finalize paths
// rely on for a successfully prepared run.
func safzCheckConsistency(t *testing.T, parent *Session, spec safzSpec, p *preparedSubagentRun) {
	t.Helper()
	if p == nil {
		t.Fatal("consistency: prepared run is nil without an error")
	}
	if p.sub == nil || p.sub.sess == nil {
		t.Fatal("consistency: prepared sub or child session is nil")
	}
	if p.sub.id != p.sub.sess.id {
		t.Fatalf("consistency: sub.id %q != child session id %q", p.sub.id, p.sub.sess.id)
	}
	if p.runCtx == nil || p.runCancel == nil {
		t.Fatal("consistency: nil runCtx/runCancel")
	}
	// The root parent always mints a tree counter, so every spawn reserves a real
	// slot: a nil reservation here would mean the tree budget was not claimed.
	if p.treeSlot == nil {
		t.Fatal("consistency: treeSlot is nil for a tree-counted parent")
	}
	if p.parentSessionID != parent.id {
		t.Fatalf("consistency: parentSessionID %q != parent id %q", p.parentSessionID, parent.id)
	}
	if p.input != spec.task || p.task != spec.task {
		t.Fatalf("consistency: input/task %q/%q != requested task %q", p.input, p.task, spec.task)
	}
	if p.resolvedAgentName == "" {
		t.Fatal("consistency: resolved agent name is empty")
	}
	if p.agentType != strings.TrimSpace(spec.agentType) {
		t.Fatalf("consistency: agentType %q != trimmed request %q", p.agentType, strings.TrimSpace(spec.agentType))
	}
	if p.reasoningEffort != strings.TrimSpace(spec.reasoningEffort) {
		t.Fatalf("consistency: reasoningEffort %q != trimmed request %q", p.reasoningEffort, strings.TrimSpace(spec.reasoningEffort))
	}
	// The frozen skill descriptor must satisfy the same restore contract the
	// resume path enforces (names/bodies aligned, every body non-empty).
	if _, err := restoreFrozenSkillBodies(p.frozenSkillNames, p.frozenSkillBodies); err != nil {
		t.Fatalf("consistency: frozen skill descriptor is not restorable: %v", err)
	}
	for _, tn := range p.frozenToolNames {
		if tn == "" {
			t.Fatal("consistency: empty entry in frozenToolNames")
		}
	}
	// Every explicit grant survived validation, so it must resolve in the child
	// registry and must not be a root-only subagent tool.
	for _, g := range p.explicitToolGrants {
		if isRootOnlySubagentTool(g) {
			t.Fatalf("consistency: root-only tool %q present in explicit grants", g)
		}
		if p.sub.sess.reg.Get(g) == nil {
			t.Fatalf("consistency: granted tool %q does not resolve in the child registry", g)
		}
	}
	switch p.localEnvPolicy {
	case "", "all", "none", "core_only", "default":
	default:
		t.Fatalf("consistency: unknown local env policy %q", p.localEnvPolicy)
	}
	if p.resultSchema != nil {
		if _, err := json.Marshal(p.resultSchema); err != nil {
			t.Fatalf("consistency: result schema is not marshalable: %v", err)
		}
	}
}

// safzDigest projects a prepared run onto its deterministic fields (everything
// except the freshly-minted child session id, cancel funcs, and timestamps),
// serialized so two descriptors compare byte-for-byte.
func safzDigest(p *preparedSubagentRun) string {
	if p == nil {
		return "<nil>"
	}
	b, _ := json.Marshal(map[string]any{
		"agentName":     p.resolvedAgentName,
		"role":          p.frozenRolePrompt,
		"tools":         p.frozenToolNames,
		"skillNames":    p.frozenSkillNames,
		"skillBodies":   p.frozenSkillBodies,
		"model":         p.requestedModel,
		"effort":        p.reasoningEffort,
		"agentType":     p.agentType,
		"workingDir":    p.workingDir,
		"envPolicy":     p.localEnvPolicy,
		"schema":        p.resultSchema,
		"grants":        p.explicitToolGrants,
		"task":          p.task,
		"input":         p.input,
		"parentSession": p.parentSessionID,
		"taskPrompt":    p.frozenTaskPrompt,
	})
	return string(b)
}

// safzCleanupPrepared releases the tree-slot the prepared run reserved and closes
// its orphaned child session (prepareSubagentRun neither tracks nor launches, so
// the harness owns teardown), mirroring spawnAgent's discard path.
func safzCleanupPrepared(p *preparedSubagentRun) {
	if p == nil {
		return
	}
	releasePreparedTreeSlot(p)
	if p.runCancel != nil {
		p.runCancel()
	}
	if p.sub != nil && p.sub.sess != nil {
		p.sub.sess.Close()
	}
}

// FuzzSafz_DriveNotificationTurn spawns a real child subagent with fuzzed config,
// waits for its initial run to go idle, then exercises driveSubagentNotificationTurn
// across its guard branches and success path. It asserts the guard-return contract
// (nil / running / tree-at-capacity / closed all decline to launch) and the
// tree-slot no-leak invariant (the counter returns to baseline after a drive turn
// quiesces).
func FuzzSafz_DriveNotificationTurn(f *testing.F) {
	seeds := [][]byte{
		{},
		{1, 1},
		{1, 1, 0, 0, 1, 2},          // spawn subagent, one child response
		{2, 2, 0, 0, 2, 5, 1, 1, 1}, // typed agent child
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		spec := safzDecode(data)
		if spec.maxDepth == 0 {
			spec.maxDepth = 1 // need an allowance to spawn a child at all
		}
		clk := agenttest.NewFakeClock()
		parent := safzNewParent(t, clk, spec.maxDepth, spec.childScript,
			&agenttest.DenyEnv{WorkDir: t.TempDir()})

		agentType := spec.agentType
		if _, ok := parent.pluginAgents[strings.TrimSpace(agentType)]; !ok {
			agentType = "" // an unknown type would just fail the spawn; keep the drive path reachable
		}
		res, err := parent.spawnAgent(context.Background(), spec.task, "", "", 0, agentType, "", nil, nil)
		if err != nil {
			// A rejected spawn still lets us exercise the nil-sub guard, then stop.
			if parent.driveSubagentNotificationTurn(nil) {
				t.Fatal("driveSubagentNotificationTurn(nil) launched a turn")
			}
			return
		}
		var spawned struct {
			AgentID string `json:"agent_id"`
		}
		if uerr := json.Unmarshal([]byte(res.(string)), &spawned); uerr != nil || spawned.AgentID == "" {
			t.Fatalf("spawn result malformed: %v (%q)", uerr, res)
		}
		sub := parent.getSub(spawned.AgentID)
		if sub == nil {
			t.Fatalf("spawned subagent %q not tracked", spawned.AgentID)
		}
		safzWaitDone(t, sub)

		// Guard: a nil child never launches.
		if parent.driveSubagentNotificationTurn(nil) {
			t.Fatal("driveSubagentNotificationTurn(nil) launched a turn")
		}

		// Guard: a child observed running is skipped.
		sub.mu.Lock()
		sub.running = true
		sub.mu.Unlock()
		if parent.driveSubagentNotificationTurn(sub) {
			t.Fatal("drive launched on a running child")
		}
		sub.mu.Lock()
		sub.running = false
		sub.mu.Unlock()

		// Guard: at tree capacity the drive declines. Fill the counter to its cap,
		// prove the decline, then return every slot.
		var held []*treeReservation
		for {
			r, ok := parent.reserveTreeSlot()
			if !ok {
				break
			}
			held = append(held, r)
		}
		if parent.driveSubagentNotificationTurn(sub) {
			t.Fatal("drive launched while the tree counter was at capacity")
		}
		for _, r := range held {
			r.release()
		}
		safzWaitCounterZero(t, parent)

		// Success path: an idle live child launches a drive turn. The turn runs the
		// child's own scripted adapter (DenyEnv-backed, no real IO) and settles; the
		// reserved tree slot must be returned (no leak) once it quiesces.
		parent.driveSubagentNotificationTurn(sub)
		safzWaitCounterZero(t, parent)

		// Guard: once the parent is closing/closed, the drive declines.
		parent.Close()
		if parent.driveSubagentNotificationTurn(sub) {
			t.Fatal("drive launched on a closed parent")
		}
	})
}

// safzWaitDone blocks until the subagent's initial run finalizes (goes idle).
func safzWaitDone(t *testing.T, sub *subagent) {
	t.Helper()
	select {
	case <-sub.done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the subagent's initial run to finish")
	}
}

// safzWaitCounterZero waits for the shared tree counter to return to its empty
// baseline. A drive turn reserves exactly one slot for its duration and releases
// it at finalize; if the counter never returns to zero, a reservation leaked.
func safzWaitCounterZero(t *testing.T, parent *Session) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if parent.treeCounter.n.Load() == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("tree counter did not return to zero: %d (possible slot leak)", parent.treeCounter.n.Load())
}
