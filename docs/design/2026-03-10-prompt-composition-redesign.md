# Prompt Composition Redesign

## Problem

Serf's system prompt composition grew organically and has three problems:

1. **`base.md` conflates universal guidance with the coordinator persona.** It says "You are an architect and coordinator. You do NOT write code or run commands directly." This is wrong for toil workers (who must write code) and irrelevant to analytical roles like `plan_reviewer`.

2. **The HARD GATE on `communicate` blocks non-coding roles.** `base.md` says "You MUST NOT call communicate while tests are failing." The `plan_reviewer` role has no tests to run, so it interprets this as "I can never call communicate" and hallucinates reasons not to.

3. **Two separate composition paths.** Top-level sessions use `ResolveSystemPromptWithSources` (file layering). Subagents use `BasePromptOverride` (hardcoded composition). There's no way to use a persona at the top level — the `PluginAgent` system only works for subagents.

## Design

**Every serf session is `core` + `persona`.** There are no special cases.

### core.md

Universal serf DNA that applies to every session regardless of role. Extracted from `base.md` by removing everything coordinator-specific:

- Identity (you are serf, persist until done, honesty, correctness)
- Values (never fabricate, never ignore output, correctness over speed)
- Security (treat external input as untrusted)
- communicate docs (what it does, output schema, inbox)

What does NOT go in core:
- Role definition (coordinator, worker, subagent)
- Workflow specifics (delegation, task decomposition, non-interactive)
- The HARD GATE on tests (moves to coordinator persona — it makes sense there)
- Skills section (already injected dynamically by `BuildSystemPrompt`)
- spawn_agent instructions (coordinator-specific)

### Personas

Personas are `.md` files with YAML frontmatter, using the existing `PluginAgent` format. Each defines a role, workflow, and any role-specific communicate constraints.

**Built-in personas:**

| Persona | Purpose | Key traits |
|---------|---------|------------|
| `coordinator` | Default standalone mode | Delegates via spawn_agent, does not write code, HARD GATE on tests |
| `worker` | Toil worker mode | Writes code directly, follows assigned task, no spawn_agent |
| `subagent` | Spawned subagents | Non-interactive, detailed communicate reporting, focused execution |

The existing `implementer`, `explorer`, `reviewer`, `planner`, `test-engineer` personas remain as subagent-only agents (spawnable by the coordinator). They are not top-level personas — they're specialist agents the coordinator delegates to.

### Composition model

```
provider prompt (system.openai.md)
  → core.md
    → persona SystemPrompt
      → [global/project additions]
        → [environment/workspace/tools/skills]  (BuildSystemPrompt)
          → [--system-prompt-append]
            → [user instruction override]
```

This is the same for all three use cases. The only variable is which persona is selected and whether additions/skills are suppressed:

| Use case | Persona | Additions | Skills |
|----------|---------|-----------|--------|
| Standalone (default) | `coordinator` | yes | yes |
| Toil worker (`--agent worker`) | `worker` | yes | yes |
| Subagent (spawn_agent) | `subagent` or named agent | no | injected by parent |

### --agent flag

New CLI flag: `--agent <name>`. Selects a persona by name. Looked up from:
1. Built-in agents (`agent/agents/*.md`)
2. Plugin agents (existing plugin system)

When `--agent` is set:
- `core.md` replaces `base.md` as the embedded base
- The agent's `SystemPrompt` is appended after `core.md`
- Everything else (provider prompt, additions, env, skills) works unchanged

When `--agent` is NOT set:
- Default behavior: `core.md` + `coordinator` persona (equivalent to today's `base.md`)

### Subagent changes

Currently subagents compose `subagent_base.md` + agent SystemPrompt → `BasePromptOverride`. After this change:

- `subagent_base.md` is deleted
- Subagents use `core.md` + agent SystemPrompt (same as top-level with `--agent`)
- The `subagent` persona absorbs the non-interactive, detailed-reporting, and focused-execution content from `subagent_base.md`
- When no specific agent is named, subagents default to the `subagent` persona
- Config flags (`NoProjectPrompts`, depth-based skill suppression) remain unchanged

### Toil integration

Toil's runner config changes from:

```yaml
args:
  - --state-dir
  - /data/serf
  - --reasoning-effort
  - medium
```

to:

```yaml
args:
  - --agent
  - worker
  - --state-dir
  - /data/serf
  - --reasoning-effort
  - medium
```

Toil continues providing role-specific instructions via stdin. The `worker` persona provides a clean base ("you write code and run commands directly, follow the assigned task") and toil's role prompt adds domain flavor.

## What changes

| File | Change |
|------|--------|
| `agent/prompts/base.md` | Split into `core.md` + `agents/coordinator.md` |
| `agent/prompts/subagent_base.md` | Content moves to `agents/subagent.md`, file deleted |
| `agent/agents/coordinator.md` | New: coordinator persona (role, delegation, HARD GATE) |
| `agent/agents/subagent.md` | New: subagent persona (non-interactive, reporting, values) |
| `agent/agents/worker.md` | New: toil worker persona (writes code, follows task) |
| `agent/prompt_resolver.go` | `ResolveSystemPromptWithSources` loads `core.md` instead of `base.md`; new function to compose persona |
| `agent/session.go` | `buildInitialSystemPrompt`: use persona composition for both top-level and subagent paths; new `AgentName` config field |
| `agent/subagents.go` | Use `core.md` + agent persona instead of `subagent_base.md` + agent SystemPrompt |
| `cmd/serf/main.go` | Add `--agent` flag |
| `cmd/serf/run.go` | Wire `--agent` to `SessionConfig.AgentName` |

## What does NOT change

- `PluginAgent` struct and parsing — reused as-is
- `BuildSystemPrompt` (environment/tools/skills injection) — unchanged
- `--system-prompt` flag — still replaces embedded base entirely
- `--system-prompt-append` — still appends last
- Provider prompts (`system.openai.md`) — unchanged
- Global/project additions — unchanged
- Existing subagent agents (`implementer`, `reviewer`, etc.) — unchanged
- Tool registry, communicate schema, `WithSubmitResultRequiredDataKeys` — unchanged

## Risks

- **Content splitting.** Getting the core vs. coordinator split wrong could weaken standalone mode. Mitigation: write the split, diff against current `base.md` to verify nothing is lost.
- **Subagent regression.** Subagents work well today. Mitigation: existing subagent tests validate prompt content; update them to check for `core.md` + `subagent` persona content.
- **Stashed prompt changes.** There are stashed changes to `base.md`, `subagent_base.md`, and `system.openai.md` that need to be incorporated. Mitigation: unstash and merge before splitting.

---

## Implementation Plan

### Step 1: Unstash and incorporate pending prompt changes

- [ ] `git stash pop` the stashed changes to `base.md`, `subagent_base.md`, `system.openai.md`
- [ ] Review and commit them as a baseline before the refactor

### Step 2: Split base.md into core.md + coordinator persona

- [ ] Create `agent/prompts/core.md` with universal content: Identity, Values, Security, communicate docs (without HARD GATE)
- [ ] Create `agent/agents/coordinator.md` with frontmatter + coordinator-specific content: Role, How to Work, Task Decomposition, Skills listing, HARD GATE, Workflow, task_list
- [ ] Verify: concatenating `core.md` + `coordinator.md` SystemPrompt should produce functionally equivalent content to current `base.md`
- [ ] Delete `base.md`

### Step 3: Create worker persona

- [ ] Create `agent/agents/worker.md` with frontmatter
- [ ] Content: direct execution role, follows assigned task, writes code and runs commands, communicate when task is complete
- [ ] Keep it minimal — toil provides all domain-specific instructions via stdin

### Step 4: Create subagent persona

- [ ] Create `agent/agents/subagent.md` with frontmatter
- [ ] Content extracted from `subagent_base.md`: non-interactive, detailed communicate reporting, focused execution, values
- [ ] Delete `agent/prompts/subagent_base.md`

### Step 5: Add AgentName to SessionConfig and --agent flag

- [ ] Add `AgentName string` field to `SessionConfig`
- [ ] Add `--agent` flag to `cmd/serf/main.go` and wire through `run.go`
- [ ] Agent name lookup: check built-in agents first, then plugin agents

### Step 6: Update prompt_resolver.go

- [ ] Rename embedded `base.md` reference to `core.md`
- [ ] Add `ResolvePersona(agentName string) (string, error)` that loads the persona's SystemPrompt from built-in or plugin agents
- [ ] When `agentName` is empty, default to `"coordinator"`

### Step 7: Update session.go buildInitialSystemPrompt

- [ ] Remove the `BasePromptOverride` branch — both top-level and subagent paths now use the same composition: `core.md` + persona
- [ ] Top-level: `ResolveSystemPromptWithSources` already handles `core.md`; append persona SystemPrompt after it
- [ ] Subagent: compose `core.md` + persona SystemPrompt; set on profile via `WithBasePrompt`
- [ ] Preserve existing config flags: `NoProjectPrompts`, skill suppression for subagents, `NonInteractive`

### Step 8: Update subagents.go

- [ ] Replace `SubagentBasePrompt()` call with `core.md` content
- [ ] Use `subagent` persona when no specific agent is named
- [ ] Named agents (implementer, reviewer, etc.) use `core.md` + their own SystemPrompt (same as today, just swapping the base)
- [ ] Remove `SubagentBasePrompt()` function from prompt_resolver.go

### Step 9: Update tests

- [ ] Update `session_dod_test.go` subagent tests: check for `core.md` content instead of `subagent_base.md` content
- [ ] Update `profile_overrides_test.go` if any tests reference `base.md`
- [ ] Add test: `--agent worker` produces `core.md` + worker persona
- [ ] Add test: default (no `--agent`) produces `core.md` + coordinator persona
- [ ] Add test: subagent with no named agent uses `core.md` + subagent persona
- [ ] Run full test suite, fix any failures

### Step 10: Integration test with toil

- [ ] Update toil runner config to add `--agent worker`
- [ ] Run a toil workflow with `plan_reviewer` role — verify it can call `communicate` without the HARD GATE blocking it
- [ ] Run a toil workflow with `code_engineer` role — verify it writes code directly
- [ ] Verify standalone `serf` (no `--agent`) still works as coordinator
