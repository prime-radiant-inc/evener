# Align Engine with Reference Dotfiles Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Bring the Attractor engine code into alignment with the attractor-spec and the reference dotfiles in `docs/strongdm/dot specs/`, resolving 8 identified drift items between the implementation and the spec.

**Architecture:** The changes span parser/model (attribute aliasing), handler routing (shape recognition), engine loop (loop_restart), validator (shape acceptance), and the english-to-dotfile skill (documentation alignment). Each drift item is a self-contained fix with its own tests. Changes are additive — no existing valid dotfiles will break.

**Tech Stack:** Go, Graphviz DOT, english-to-dotfile skill (Markdown)

---

## Drift Items Summary

| # | Drift | Spec Says | Code Does | Fix |
|---|-------|-----------|-----------|-----|
| 1 | `node_type` ignored | Not in spec — production dotfiles invented it | Stored but ignored | No code change needed — document that `node_type` is not a spec attribute |
| 2 | `loop_restart` rejected | Spec §3.2 Step 7: `restart_run(graph, config, start_at=next_edge.target)` | `return nil, fmt.Errorf("loop_restart not supported in v1")` | Implement loop_restart per spec |
| 3 | `circle`/`doublecircle` not recognized | Spec §2.8: only `Mdiamond`/`Msquare` | Validator accepts `Mdiamond`/`Msquare` OR id-matching | No shape change needed — but validator id-matching is correct per spec. Document that reference dotfiles should use `Mdiamond`/`Msquare` |
| 4 | `llm_prompt` vs `prompt` | Spec §2.6: attribute is `prompt` | Code reads `prompt` | Add `llm_prompt` as alias for `prompt` so reference dotfiles work |
| 5 | `context_fidelity_default`/`context_thread_default` wrong names | Spec §2.5: `default_fidelity`, §5.4 thread resolution uses `thread_id` | Code uses `default_fidelity` | Add aliases so reference dotfile attribute names also resolve |
| 6 | `is_codergen` not meaningful | Not in spec | Ignored (stored in attrs map) | No code change — cosmetic attribute in reference dotfiles |
| 7 | Per-node `llm_model`/`llm_provider` vs stylesheet | Spec supports both (§8.5) | Code supports both | No code change — both approaches are valid |
| 8 | `timeout` on codergen nodes | Spec §2.6: `timeout` is a Duration, applies to any node | Code only uses timeout for tool nodes (10s default) | Wire timeout support for codergen nodes |

**Net code changes needed: Items 2, 4, 5, 8.** Items 1, 3, 6, 7 are documentation/skill-only fixes.

---

### Task 1: Create feature branch

**Files:**
- None (git operation)

**Step 1: Create and switch to the branch**

```bash
git checkout -b align-engine-with-reference-dotfiles
```

**Step 2: Commit**

No commit needed — empty branch.

---

### Task 2: Add `llm_prompt` as alias for `prompt` (Drift #4)

This is the highest-impact fix. The reference dotfiles in `docs/strongdm/dot specs/` all use `llm_prompt` instead of `prompt`. The engine reads `prompt`. Without this alias, every prompt in those pipelines is silently ignored.

**Files:**
- Test: `internal/attractor/dot/model_test.go`
- Modify: `internal/attractor/dot/model.go`

**Step 1: Write the failing test**

Add a test to `model_test.go`:

```go
func TestNode_Prompt_FallsBackToLLMPrompt(t *testing.T) {
	n := model.NewNode("test")
	n.Attrs["llm_prompt"] = "Do the thing"

	if got := n.Prompt(); got != "Do the thing" {
		t.Errorf("Prompt() = %q, want %q", got, "Do the thing")
	}
}

func TestNode_Prompt_PrefersPromptOverLLMPrompt(t *testing.T) {
	n := model.NewNode("test")
	n.Attrs["prompt"] = "Canonical prompt"
	n.Attrs["llm_prompt"] = "Alternate prompt"

	if got := n.Prompt(); got != "Canonical prompt" {
		t.Errorf("Prompt() = %q, want %q", got, "Canonical prompt")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/dot/ -run TestNode_Prompt_FallsBackToLLMPrompt -v`
Expected: FAIL — `Prompt()` returns `""` because it only checks `prompt` key.

**Step 3: Write minimal implementation**

In `model.go`, modify the `Prompt()` method:

```go
func (n *Node) Prompt() string {
	if p := n.Attr("prompt", ""); p != "" {
		return p
	}
	return n.Attr("llm_prompt", "")
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/attractor/dot/ -run TestNode_Prompt -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/dot/model.go internal/attractor/dot/model_test.go
git commit -m "feat(attractor): alias llm_prompt to prompt for reference dotfile compat

The reference dotfiles in docs/strongdm/dot specs/ use llm_prompt as their
prompt attribute name. The spec (§2.6) defines the canonical name as 'prompt'.
Add fallback: Prompt() checks 'prompt' first, then 'llm_prompt'."
```

---

### Task 3: Add `context_fidelity_default`/`context_thread_default` as graph-level aliases (Drift #5)

The reference dotfiles use `context_fidelity_default` and `context_thread_default` at the graph level. The spec uses `default_fidelity` and `thread_id`. Add aliasing so both work.

**Files:**
- Test: `internal/attractor/engine/fidelity_test.go` (or wherever fidelity resolution is tested)
- Modify: `internal/attractor/engine/fidelity.go` (or wherever graph-level fidelity defaults are read)

**Step 1: Find where `default_fidelity` is read from graph attrs**

Search for `default_fidelity` in the engine code. The fidelity resolution reads graph attributes to determine the default fidelity mode. We need to also check `context_fidelity_default`.

**Step 2: Write the failing test**

```go
func TestFidelity_ResolvesContextFidelityDefault(t *testing.T) {
	g := &model.Graph{
		Attrs: map[string]string{
			"context_fidelity_default": "truncate",
		},
		Nodes: map[string]*model.Node{},
	}
	// Verify that the graph-level default fidelity resolves to "truncate"
	// when using the alias attribute name
}
```

(Exact test structure depends on how fidelity resolution is exposed — adapt after reading the code.)

**Step 3: Implement the alias**

Where the engine reads `graph.Attr("default_fidelity", "")`, add a fallback:

```go
fidelity := graph.Attr("default_fidelity", "")
if fidelity == "" {
	fidelity = graph.Attr("context_fidelity_default", "")
}
```

Same for `thread_id` / `context_thread_default`:

```go
threadID := graph.Attr("thread_id", "")
if threadID == "" {
	threadID = graph.Attr("context_thread_default", "")
}
```

**Step 4: Run tests**

Run: `go test ./internal/attractor/engine/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/fidelity.go internal/attractor/engine/fidelity_test.go
git commit -m "feat(attractor): alias context_fidelity_default and context_thread_default

Reference dotfiles use context_fidelity_default and context_thread_default
at graph level. The spec uses default_fidelity and thread_id. Add fallback
so both naming conventions work."
```

---

### Task 4: Implement `loop_restart` on edges (Drift #2)

The spec (§3.2 Step 7) defines `loop_restart` behavior: when an edge with `loop_restart=true` is selected, the engine terminates the current run and re-launches with a fresh log directory, starting at the edge's target node. The current code rejects this with an error.

**Files:**
- Test: `internal/attractor/engine/engine_test.go`
- Modify: `internal/attractor/engine/engine.go`

**Step 1: Write the failing test**

```go
func TestEngine_LoopRestart_RelauncsWithFreshLogs(t *testing.T) {
	// Build a graph: start -> work -> check
	//   check -> exit [condition="outcome=success"]
	//   check -> work [condition="outcome=fail", loop_restart=true]
	//
	// On first execution, work returns fail. The engine should
	// restart the run with a fresh log directory, starting at "work".
	// On second execution, work returns success.
	//
	// Verify: two log directories exist, final outcome is success.
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine/ -run TestEngine_LoopRestart -v`
Expected: FAIL with `loop_restart not supported in v1`

**Step 3: Implement loop_restart**

In `engine.go`, replace the error:

```go
// Before (lines 529-532):
if strings.EqualFold(next.Attr("loop_restart", "false"), "true") {
	return nil, fmt.Errorf("loop_restart not supported in v1")
}

// After:
if strings.EqualFold(next.Attr("loop_restart", "false"), "true") {
	return e.restartRun(graph, next.To, config)
}
```

Implement `restartRun()`:

```go
func (e *Engine) restartRun(graph *model.Graph, startNodeID string, config *RunConfig) (*RunResult, error) {
	// 1. Create fresh log directory (new timestamp-based subdirectory)
	// 2. Copy the original graph.dot to the new log directory
	// 3. Re-invoke the run loop starting at startNodeID instead of the start node
	// 4. Return the result from the restarted run
}
```

The exact implementation depends on how `RunConfig` and log directory creation work. The key behavioral change: instead of erroring, call back into the run function with a new logs_root and a specified start node.

**Step 4: Run tests**

Run: `go test ./internal/attractor/engine/ -run TestEngine_LoopRestart -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `go test ./internal/attractor/...`
Expected: All existing tests still pass.

**Step 6: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/engine_test.go
git commit -m "feat(attractor): implement loop_restart edge attribute per spec §3.2

When an edge with loop_restart=true is selected, the engine now terminates
the current run and re-launches with a fresh log directory starting at the
edge's target node. Previously this was rejected with an error."
```

---

### Task 5: Wire `timeout` for codergen nodes (Drift #8)

The spec (§2.6) defines `timeout` as a Duration attribute on any node. The current code only uses timeout for `parallelogram` (tool) nodes. Codergen nodes should respect timeout too.

**Files:**
- Test: `internal/attractor/engine/handlers_test.go` (or `codergen_test.go`)
- Modify: `internal/attractor/engine/handlers.go` (CodergenHandler)

**Step 1: Write the failing test**

```go
func TestCodergenHandler_RespectsTimeout(t *testing.T) {
	// Create a node with timeout="2s" and a backend that takes 5 seconds
	// Verify the handler returns a FAIL/RETRY outcome due to timeout
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — the handler runs to completion (or forever) because timeout is ignored.

**Step 3: Implement timeout**

In the CodergenHandler's `Execute` method, parse the node's `timeout` attribute and create a context with deadline:

```go
if timeoutStr := node.Attr("timeout", ""); timeoutStr != "" {
	dur, err := parseDuration(timeoutStr)
	if err == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dur)
		defer cancel()
	}
}
```

Pass this context through to the backend's `Run()` call. The backend should respect context cancellation.

**Step 4: Run tests**

Run: `go test ./internal/attractor/engine/ -run TestCodergenHandler_RespectsTimeout -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/handlers.go internal/attractor/engine/handlers_test.go
git commit -m "feat(attractor): wire timeout attribute for codergen nodes

The spec defines timeout as applicable to any node. Previously only tool
nodes (parallelogram) respected timeout. Codergen nodes now parse the
timeout attribute and apply it as a context deadline to the backend call."
```

---

### Task 6: Overhaul english-to-dotfile skill (all drifts + new patterns from reference dotfiles)

The skill currently only teaches linear build pipelines. The reference dotfiles demonstrate richer patterns the skill should support: looping workflows, multi-outcome steering, fan-out/fan-in, relaxed node patterns, and per-node tuning attributes. This task brings the skill into alignment with both the spec and the production dotfiles.

**Files:**
- Modify: `skills/english-to-dotfile/SKILL.md`

#### Step 1: Update the DSL Quick Reference — Node attributes (line ~418)

Replace:

```
`label`, `shape`, `prompt`, `max_retries`, `goal_gate`, `retry_target`, `class`, `timeout`, `llm_model`, `llm_provider`, `reasoning_effort`, `allow_partial`, `fidelity`, `thread_id`
```

With:

```
| Attribute | Description |
|-----------|-------------|
| `label` | Display name (defaults to node ID) |
| `shape` | Handler type: `Mdiamond` (start), `Msquare` (exit), `box` (codergen), `diamond` (conditional), `hexagon` (wait.human), `component` (parallel fan-out), `tripleoctagon` (fan-in), `parallelogram` (tool/shell) |
| `type` | Explicit handler override (takes precedence over shape) |
| `prompt` | LLM instruction. Supports `$goal` expansion. Also accepted as `llm_prompt` (alias). |
| `class` | Comma-separated classes for model stylesheet targeting (e.g., `"hard"`, `"verify"`, `"review"`) |
| `max_retries` | Additional attempts beyond initial execution. `max_retries=3` = 4 total. |
| `goal_gate` | `true` = node must succeed before pipeline can exit |
| `retry_target` | Node ID to jump to if this goal_gate fails |
| `fallback_retry_target` | Secondary retry target |
| `allow_partial` | `true` = accept PARTIAL_SUCCESS when retries exhausted instead of FAIL. Use on long-running nodes where partial progress is valuable. |
| `max_agent_turns` | Max LLM turn count for this node's agent session. Use to right-size effort per task (e.g., 4 for a simple check, 25 for complex implementation). |
| `timeout` | Duration (e.g., `"300s"`, `"15m"`, `"2h"`). Applies to any node type. |
| `auto_status` | `true` = auto-generate SUCCESS outcome if handler writes no status.json. Only use on `expand_spec`. |
| `llm_model` | Override model for this node (overrides stylesheet) |
| `llm_provider` | Override provider for this node |
| `reasoning_effort` | `low`, `medium`, `high` |
| `fidelity` | Context fidelity: `full`, `truncate`, `compact`, `summary:low`, `summary:medium`, `summary:high` |
| `thread_id` | Thread key for LLM session reuse under `full` fidelity |
```

#### Step 2: Update the DSL Quick Reference — Edge attributes (line ~422)

Replace:

```
`label`, `condition`, `weight`, `fidelity`, `thread_id`, `loop_restart`
```

With:

```
| Attribute | Description |
|-----------|-------------|
| `label` | Display caption and preferred-label routing key |
| `condition` | Boolean guard: `outcome=success`, `outcome=fail`, `outcome=skip`, etc. AND-only (`&&`). |
| `weight` | Numeric priority for edge selection (higher wins among equally eligible edges) |
| `fidelity` | Override fidelity mode for the target node |
| `thread_id` | Override thread key for session reuse at target node |
| `loop_restart` | When `true`, terminates the current run and re-launches with a fresh log directory starting at the edge's target node. Use on edges that loop back to much-earlier nodes where accumulated context/logs would be stale. |
```

#### Step 3: Update the DSL Quick Reference — Shapes table (line ~408)

Replace:

```
| Shape | Handler | Use |
|-------|---------|-----|
| `Mdiamond` | start | Entry point. Exactly one. |
| `Msquare` | exit | Exit point. Exactly one. |
| `box` | codergen | LLM task (default). |
| `diamond` | conditional | Routes on edge conditions. |
| `hexagon` | wait.human | Human approval gate (only for interactive runners; do not rely on this for disambiguation). |
```

With:

```
| Shape | Handler | Use |
|-------|---------|-----|
| `Mdiamond` | start | Entry point. Exactly one. |
| `Msquare` | exit | Exit point. Exactly one. |
| `box` | codergen | LLM task (default for all nodes). |
| `diamond` | conditional | Pass-through routing point. Routes based on edge conditions against current context. |
| `hexagon` | wait.human | Human approval gate (only for interactive runners). |
| `component` | parallel | Fan-out: executes outgoing branches concurrently. |
| `tripleoctagon` | parallel.fan_in | Fan-in: waits for branches, selects best result. |
| `parallelogram` | tool | Shell command execution (uses `tool_command` attribute). |
```

#### Step 4: Add new section — "Advanced Graph Patterns" (after Phase 3's "Review node" section, before Phase 4)

Insert a new section:

```markdown
#### Custom multi-outcome steering

The skill's default impl→verify→check pattern uses binary `outcome=success`/`outcome=fail`. But prompts can define any custom outcome values, and edges can route on them. Use this for workflows with skip/acknowledge/escalate paths:

```
analyze [
    shape=box,
    prompt="Analyze the commit. If it's relevant to our Go codebase, use outcome=port. If it's Python-only or docs-only, use outcome=skip.\n\nWrite status.json: outcome=port or outcome=skip with reasoning."
]

analyze -> plan_port  [condition="outcome=port", label="port"]
analyze -> fetch_next [condition="outcome=skip", label="skip", loop_restart=true]
```

When using custom outcomes, the prompt MUST tell the agent exactly which outcome values to write and when.

#### Looping/cyclic workflows

Not all pipelines are linear build-then-review chains. Some workflows process items in a loop until done (e.g., processing commits, handling a queue, iterating on feedback). The key pattern:

```
start -> fetch_next
fetch_next -> process [condition="outcome=process"]
fetch_next -> exit    [condition="outcome=done"]
process -> validate
validate -> finalize  [condition="outcome=success"]
validate -> fix       [condition="outcome=fail"]
fix -> validate
finalize -> fetch_next [loop_restart=true]
```

Key elements:
- A **fetch/check** node at the loop head that returns `outcome=done` when there's nothing left
- `loop_restart=true` on the edge that loops back, so each iteration gets a fresh log directory
- The loop body follows the same impl→verify pattern as linear pipelines

#### Fan-out / fan-in (parallel consensus)

When you need multiple models to independently tackle the same task and then consolidate:

```
// Fan-out: one node fans to 3 parallel workers
consolidate_input -> plan_a
consolidate_input -> plan_b
consolidate_input -> plan_c

// Fan-in: all 3 converge on a synthesis node
plan_a -> synthesize
plan_b -> synthesize
plan_c -> synthesize

synthesize [
    shape=box,
    prompt="Read .ai/plan_a.md, .ai/plan_b.md, .ai/plan_c.md. Synthesize the best elements into .ai/plan_final.md."
]
```

Each parallel worker writes its output to a uniquely-named `.ai/` file. The synthesis node reads all of them. This pattern is used for:
- Definition of Done proposals (3 models propose, 1 consolidates)
- Implementation planning (3 plans, 1 debate/consolidate)
- Code review (3 reviewers, 1 consensus)

#### Relaxed node patterns (2-node vs 3-node)

The mandatory 3-node pattern (impl → verify → diamond check) is the **default for build pipelines**. But for non-build workflows (analysis, review, processing loops), a 2-node pattern is acceptable:

**Use 3-node (impl → verify → check) when:**
- The node produces code that must compile/pass tests
- There's a concrete build/test command to run

**Use 2-node (work → check) when:**
- The node is analytical (review, planning, triage) with no build step
- The node's prompt already includes outcome routing instructions
- The verify step would just be "read what the previous node wrote"

In the 2-node pattern, the work node acts as its own steer — its prompt instructs the agent to write `outcome=success`/`outcome=fail`/`outcome=<custom>` directly.

#### File-based inter-node communication

Nodes communicate through the filesystem, not through context variables. Each node writes its output to a named file under `.ai/`, and downstream nodes' prompts tell them which files to read:

```
plan [
    shape=box,
    prompt="Create an implementation plan. Write to .ai/plan.md."
]

implement [
    shape=box,
    prompt="Follow the plan in .ai/plan.md. Implement all items. Log changes to .ai/impl_log.md."
]

review [
    shape=box,
    prompt="Read .ai/plan.md and .ai/impl_log.md. Review implementation against the plan. Write review to .ai/review.md."
]
```

This pattern is mandatory because each node runs in a fresh agent session with no memory of prior nodes. The filesystem is the only shared state.
```

#### Step 5: Update anti-patterns (lines ~439-454)

Replace anti-pattern #1:

```
1. **No verification after implementation.** Every impl node MUST have a verify node after it. Never chain impl → impl → impl. This includes `impl_setup`.
```

With:

```
1. **No verification after implementation (in build pipelines).** Every impl node that produces code MUST have a verify node after it. Never chain impl → impl → impl. Exception: analytical/triage nodes in non-build workflows may use the 2-node pattern (see "Relaxed node patterns" above).
```

Replace anti-pattern #8:

```
8. **Wrong shapes.** Start is `Mdiamond` not `circle`. Exit is `Msquare` not `doublecircle`.
```

With:

```
8. **Wrong shapes.** Start must be `Mdiamond`. Exit must be `Msquare`. The validator also accepts nodes with id `start`/`exit` regardless of shape, but always use the canonical shapes.
```

Replace anti-pattern #9:

```
9. **Timeouts.** Do NOT include node-level `timeout` by default. Only add timeouts when explicitly requested; a single CLI run can legitimately take hours.
```

With:

```
9. **Unnecessary timeouts.** Do NOT add timeouts to simple impl/verify nodes in linear pipelines — a single CLI run can legitimately take hours. DO add timeouts to nodes in looping pipelines (to prevent infinite hangs) or nodes calling external services. When adding timeouts, use generous values (`"900s"` for normal work, `"1800s"` for complex implementation, `"2400s"` for integration).
```

Add new anti-pattern #15:

```
15. **Missing file-based handoff.** Every node that produces output for downstream nodes must write it to a named `.ai/` file. Every node that consumes prior output must be told which files to read. Relying on context variables for large data (plans, reviews, logs) does not work — use the filesystem.
```

Add new anti-pattern #16:

```
16. **Binary-only outcomes in steering nodes.** If a workflow has more than two paths (e.g., process/skip/done), define custom outcome values in the prompt and route on them with conditions. Don't force everything into success/fail.
```

#### Step 6: Update Phase 4 prompt guidance (lines ~318-361)

After the existing verification prompt template, add:

```markdown
#### Steering/analysis prompt template (for multi-outcome nodes)

For nodes that route to different paths based on analysis (not just success/fail):

```
Goal: $goal

Analyze [SUBJECT].

Read: [INPUT_FILES]

Evaluate against these criteria:
- [CRITERION_1]: if true, use outcome=[VALUE_1]
- [CRITERION_2]: if true, use outcome=[VALUE_2]
- [CRITERION_3]: if true, use outcome=[VALUE_3]

Write your analysis to .ai/[ANALYSIS_FILE].md.
Write status.json with the appropriate outcome value.
```

#### Prompt complexity scaling

Simple impl/verify prompts (5-10 lines) are fine for straightforward tasks. But prompts for complex workflows should be substantially richer:

- **Simple tasks** (create a file, run a test): 5-10 line prompt
- **Moderate tasks** (implement a module per spec): 15-25 line prompt with spec references, file lists, acceptance criteria
- **Complex tasks** (multi-step with external tools, conditional logic): 30-60 line prompt with numbered steps, embedded commands, examples of expected output, and explicit conditional logic

The reference dotfiles in `docs/strongdm/dot specs/` demonstrate production-quality prompts with multi-paragraph instructions, embedded shell commands with examples, numbered steps, and conditional branches within a single prompt.
```

#### Step 7: Add note about non-spec attributes (after the Anti-Patterns section)

Insert:

```markdown
## Notes on Reference Dotfile Conventions

Some reference dotfiles in `docs/strongdm/dot specs/` use attributes not defined in the Attractor spec. These are harmless (stored but ignored by the engine) and should NOT be emitted by this skill:

- `node_type` (e.g., `stack.steer`, `stack.observe`) — handler type is determined by `shape` or explicit `type` attribute, not by `node_type`
- `is_codergen` — codergen handler is determined by shape, not by this flag
- `context_fidelity_default` — use `default_fidelity` (the spec-canonical name; the engine accepts both)
- `context_thread_default` — use graph-level `thread_id` (the engine accepts both)
```

#### Step 8: Commit

```bash
git add skills/english-to-dotfile/SKILL.md
git commit -m "feat(english-to-dotfile): major skill overhaul from reference dotfile patterns

- Add multi-outcome steering pattern with custom outcome values
- Add looping/cyclic workflow pattern with loop_restart
- Add fan-out/fan-in consensus pattern with file-based coordination
- Add relaxed 2-node pattern for non-build workflows
- Document file-based inter-node communication as mandatory pattern
- Expand shapes table with parallel, fan-in, and tool handlers
- Expand node/edge attributes to full reference tables
- Add allow_partial, max_agent_turns, type override documentation
- Add prompt complexity scaling guidance and steering prompt template
- Update anti-patterns: relax verification rule, improve timeout guidance
- Add anti-patterns for missing file handoff and binary-only outcomes
- Document non-spec attributes from reference dotfiles"
```

---

### Task 7: Add integration test with a reference dotfile

Validate end-to-end that a reference-style dotfile (using `llm_prompt`, `context_fidelity_default`, `loop_restart`, and `timeout`) parses, validates, and simulates correctly.

**Files:**
- Create: `internal/attractor/engine/reference_compat_test.go`

**Step 1: Write the integration test**

```go
func TestEngine_ReferenceStyleDotfile(t *testing.T) {
	dot := `digraph Workflow {
		graph [
			goal="Test reference compatibility",
			context_fidelity_default="truncate",
			context_thread_default="test-thread",
			default_max_retry="2"
		]

		Start [shape=Mdiamond, label="Start"]
		Exit  [shape=Msquare, label="Exit"]

		Work [
			shape=box,
			llm_prompt="Implement the thing. Goal: $goal\nWrite status.json: outcome=success",
			timeout="300s"
		]

		Check [shape=diamond, label="Check"]

		Start -> Work
		Work -> Check
		Check -> Exit [condition="outcome=success"]
		Check -> Work [condition="outcome=fail", loop_restart=true]
	}`

	// 1. Parse
	graph, err := dot.Parse(dot)
	require.NoError(t, err)

	// 2. Validate
	diags := validate.Validate(graph)
	for _, d := range diags {
		require.NotEqual(t, validate.SeverityError, d.Severity, d.Message)
	}

	// 3. Verify prompt resolved from llm_prompt
	workNode := graph.Nodes["Work"]
	require.Equal(t, "Implement the thing. Goal: $goal\nWrite status.json: outcome=success", workNode.Prompt())

	// 4. Verify context_fidelity_default is accessible
	require.Equal(t, "truncate", graph.Attr("context_fidelity_default", ""))
}
```

**Step 2: Run test**

Run: `go test ./internal/attractor/engine/ -run TestEngine_ReferenceStyleDotfile -v`
Expected: PASS (after all previous tasks are done)

**Step 3: Commit**

```bash
git add internal/attractor/engine/reference_compat_test.go
git commit -m "test(attractor): add integration test for reference-style dotfiles

Validates that dotfiles using llm_prompt, context_fidelity_default,
context_thread_default, timeout on codergen nodes, and loop_restart
all parse, validate, and resolve correctly."
```

---

## Execution Order & Dependencies

```
Task 1 (branch)
  └── Task 2 (llm_prompt alias) — no dependencies
  └── Task 3 (fidelity aliases) — no dependencies
  └── Task 4 (loop_restart) — no dependencies
  └── Task 5 (codergen timeout) — no dependencies
      └── Task 6 (skill docs) — after 2-5 so docs reflect reality
      └── Task 7 (integration test) — after 2-5 so test covers all fixes
```

Tasks 2, 3, 4, 5 are independent and can be done in parallel or any order. Tasks 6 and 7 should come after all code changes.
