# Prompt Redesign: Soul + Skills — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Separate serf's monolithic base.md into a lean soul prompt + methodology skills loaded on demand.

**Architecture:** base.md becomes ~80 lines (identity, values, coordinator role). TDD, debugging, verification, and delegation methodology move to embedded skills. Agent types get a `skills` frontmatter field for auto-injection per the Claude Code subagent spec.

**Tech Stack:** Go (agent package), embedded markdown files, YAML frontmatter parsing.

**Date**: 2026-02-26
**Status**: Plan
**Branch**: feat/gpt-5.3-codex-phase-support

## Problem

The current system prompt (`base.md`, 514 lines) is a monolithic document that tries to be
everything: identity, values, TDD methodology, debugging methodology, subagent delegation
patterns, verification workflow, and more. This causes:

1. **Wrong methodology applied**: On build-cython-ext (a fix-and-build task), the agent
   slavishly followed the TDD mandate, spawning a test-writer subagent that consumed 85%
   of its 900s budget writing tests for a task that already had a test suite.
2. **Bloated context**: Every agent carries ~14K chars of methodology it may never use.
3. **Conflicting signals**: base.md says both "implement minimum code" and "delegate
   implementation to subagents." The agent can't be both doer and orchestrator.
4. **Accreted, not designed**: Prompt grew via incremental additions (see git log) rather
   than coherent design.

## Design

### Principle: Soul + Kung-Fu

The base prompt is the agent's **soul** — identity, values, and role. Specific methodologies
are **skills** that get loaded when relevant, like Neo downloading martial arts in The Matrix.

### 1. base.md — The Soul (~80 lines)

Covers four things and nothing else:

**Identity & Values**
- You are serf. You persist until done. You don't lie. You don't fabricate.
- Read errors carefully. Correctness over speed.
- You are efficient and productive with your resources. You don't waste time, but you
  also don't hurry or rush.

**Role: Coordinator**
- You understand the task, break it into pieces, and dispatch subagents to do the work.
- You are accountable for the outcome. If a subagent fails or produces bad work, that's
  your failure — you should have caught it.
- You verify subagent output against the original requirements. Don't trust self-assessment.
- You can do simple things directly (run a command, read a file), but sustained
  implementation work should be delegated so your context stays clean.
- You enforce your values on your subagents — if a subagent cuts corners, you send it back.

**Skills**
- You have access to skills — specialized methodologies for different kinds of work.
- Before starting, consider which skills apply to this task.
- Listed: core skill names with 1-line descriptions (see section 3 below).
- When dispatching subagents, tell them which skills to load if relevant.

**submit_result**
- Trimmed version of current rules: call when task is complete and verified.

### 2. subagent_base.md — Shared Subagent Instructions (~45 lines)

Keep current content (submit_result rules, non-interactive constraint, workflow basics)
plus two additions:

**Skills awareness** (~5 lines): Skills may be pre-loaded into your context. Follow
their methodology. If the coordinator told you to load a skill, do so.

**Value inheritance** (~5 lines): You share the coordinator's values: honesty, correctness,
thoroughness. Never fabricate results. A thorough partial result beats a sloppy complete one.

### 3. Core Skills (embedded, loaded on demand)

These ship with serf as embedded skill files. The coordinator lists them in its prompt with
1-line descriptions and decides which to load or tell subagents to load.

| Skill | Description | Current source |
|-------|-------------|----------------|
| `tdd` | Test-driven development: write tests first, implement against them | base.md lines 63-181 |
| `debugging` | Systematic debugging: root cause, hypothesis, approach log | base.md lines 344-408 |
| `verification` | Adversarial self-review before submitting | base.md lines 429-508 |
| `subagent-patterns` | When/how to delegate, blocking vs async, parallel | base.md lines 202-287 |
| `ops-task` | Fix/build/configure workflow: install deps, try, fix, verify | New (distilled from workflow section) |

### 4. Agent Types — Slim Identity + Tool Constraints + Skills

Agent type markdown files become minimal. They define:
- Tool access (the security boundary)
- A `skills` frontmatter field listing skills to auto-inject
- A brief identity statement with values

**explorer.md**
```yaml
---
name: explorer
description: "Read-only codebase exploration."
tools: [glob, grep, read_file, shell]
---
You are a read-only exploration agent. Search, read, and report.
Do not modify files. Use shell only for read-only commands.
```

**test-engineer.md** (renamed from test-writer)
```yaml
---
name: test-engineer
description: "Adversarial test engineer and quality gate."
tools: [glob, grep, read_file, write_file, apply_patch, shell]
skills: [test-engineering]
---
You write tests. A separate engineer implements. Your tests are the quality gate.
```

**implementer.md**
```yaml
---
name: implementer
description: "Code implementation agent."
tools: [glob, grep, read_file, write_file, apply_patch, shell]
---
You implement code. You read and understand existing code before touching it.

You value DRY — reduce duplication even when it takes extra effort.
You value YAGNI — build what's needed now, not what might be needed later.
You are careful and responsible. You match the style of surrounding code.
You keep changes minimal and focused. You don't refactor what you weren't asked to touch.
You name things by what they do in the domain, not how they're implemented.
```

**reviewer.md**
```yaml
---
name: reviewer
description: "Verify work against requirements."
tools: [glob, grep, read_file, shell]
skills: [verification]
---
You verify work against requirements. You are skeptical by default.
```

### 5. system.openai.md — Provider-Specific (kept, trimmed)

Keep apply_patch documentation and exploration/editing constraints.
Remove behavioral guidance that duplicates base.md (persistence, round awareness).
~60 lines down from ~103.

### 6. New Feature: `skills` Frontmatter on Agents

Per [Claude Code subagent spec](https://code.claude.com/docs/en/sub-agents.md):

> `skills`: Skills to load into the subagent's context at startup. The full skill
> content is injected, not just made available for invocation.

**Implementation**:
- Add `Skills []string` to `PluginAgent` struct
- Parse `skills` from agent frontmatter in `parsePluginAgent`
- At dispatch time in `spawnAgent`, resolve skill names → content, append to composed prompt
- Skill resolution: check session's loaded skills map first (namespaced), then try
  embedded skill files

## What Gets Deleted

The following content from base.md is **moved** to skills, not deleted:
- TDD cycle (80 lines) → `tdd` skill
- Debugging methodology (50 lines) → `debugging` skill
- Verification/adversarial review (80 lines) → `verification` skill
- Subagent delegation patterns (80 lines) → `subagent-patterns` skill
- Workflow section (60 lines) → partially to `ops-task` skill, partially to base.md

The following content is **removed entirely**:
- ASCII flowchart diagrams (3 of them, ~75 lines total) — replaced with prose in skills
- Redundant statements that appear in multiple places
- "Round awareness" guidance (causes anxiety, not productive behavior)

---

## Implementation Tasks

### Task 1: Add `Skills` field to PluginAgent and parse from frontmatter

**Files:**
- Modify: `agent/plugin_agents.go:13-22` (PluginAgent struct)
- Modify: `agent/plugin_agents.go:27-88` (parsePluginAgent)
- Test: `agent/plugin_agents_test.go`

**Step 1: Write failing test for skills parsing**

Add to `agent/plugin_agents_test.go`:

```go
func TestParsePluginAgent_Skills(t *testing.T) {
	data := []byte("---\nname: test-eng\ndescription: test engineer\nskills: [test-engineering, debugging]\n---\nYou write tests.\n")
	agent, err := parsePluginAgent(data, "builtin")
	if err != nil {
		t.Fatalf("parsePluginAgent: %v", err)
	}
	if len(agent.Skills) != 2 {
		t.Fatalf("Skills = %v, want 2 items", agent.Skills)
	}
	if agent.Skills[0] != "test-engineering" || agent.Skills[1] != "debugging" {
		t.Errorf("Skills = %v, want [test-engineering debugging]", agent.Skills)
	}
}

func TestParsePluginAgent_NoSkills(t *testing.T) {
	data := []byte("---\nname: explorer\ndescription: explore\n---\nRead-only.\n")
	agent, err := parsePluginAgent(data, "builtin")
	if err != nil {
		t.Fatalf("parsePluginAgent: %v", err)
	}
	if len(agent.Skills) != 0 {
		t.Errorf("Skills = %v, want empty", agent.Skills)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginAgent_Skills -v`
Expected: FAIL — PluginAgent has no Skills field.

**Step 3: Implement Skills field and parsing**

In `agent/plugin_agents.go`, add `Skills []string` to the PluginAgent struct.
In `parsePluginAgent`, after the tools parsing block, add similar parsing for
the `skills` field (list of strings, no mapping needed — skill names are plain strings).

**Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run TestParsePluginAgent -v`
Expected: All TestParsePluginAgent* tests PASS.

**Step 5: Commit**

```
git add agent/plugin_agents.go agent/plugin_agents_test.go
git commit -m "feat: parse skills frontmatter field on PluginAgent"
```

---

### Task 2: Inject skills content into subagent system prompt at dispatch

**Files:**
- Modify: `agent/subagents.go:55-163` (spawnAgent function)
- Modify: `agent/skills.go` (add ResolveSkillContent helper)
- Test: `agent/plugin_agents_integration_test.go`

**Step 1: Write failing test for skill injection**

Add to `agent/plugin_agents_integration_test.go`:

```go
func TestSpawnAgent_PluginAgentType_InjectsSkillContent(t *testing.T) {
	// Create a session with a plugin agent that has skills: [test-skill]
	// Create a skill "test-skill" with known body content
	// Spawn the agent, capture its system prompt
	// Assert the skill body appears in the composed prompt
}
```

Use the existing test patterns from `TestSpawnAgent_PluginAgentType_SystemPrompt`
(line 89) as a template. Create a temp skill dir with a SKILL.md, register it in
the session's skills map, create a PluginAgent with `Skills: []string{"test-skill"}`,
then verify the subagent's BasePromptOverride contains the skill body text.

**Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSpawnAgent_PluginAgentType_InjectsSkillContent -v`
Expected: FAIL — spawnAgent doesn't resolve or inject skills.

**Step 3: Implement skill resolution in spawnAgent**

In `agent/skills.go`, add:

```go
// ResolveSkillContent looks up a skill by name in the session's skills map
// and returns its body content. Returns ("", nil) if not found.
func ResolveSkillContent(skills map[string]SkillMeta, name string) (string, error) {
	// Try exact match first
	if meta, ok := skills[name]; ok {
		return LoadSkillBody(meta)
	}
	// Try unnamespaced match (skill name without plugin prefix)
	for key, meta := range skills {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == name {
			return LoadSkillBody(meta)
		}
	}
	return "", nil
}
```

In `agent/subagents.go` `spawnAgent`, after composing `subBase + rolePrompt`, resolve
and append any skills from `agent.Skills`:

```go
if agent != nil && len(agent.Skills) > 0 {
	for _, skillName := range agent.Skills {
		body, err := ResolveSkillContent(s.skills, skillName)
		if err != nil {
			// log warning but continue
			continue
		}
		if body != "" {
			composed += "\n\n" + body
		}
	}
}
```

**Step 4: Run tests**

Run: `go test ./agent/ -run TestSpawnAgent -v`
Expected: All PASS, including the new skill injection test.

**Step 5: Commit**

```
git add agent/subagents.go agent/skills.go agent/plugin_agents_integration_test.go
git commit -m "feat: inject skills content into subagent system prompt at dispatch"
```

---

### Task 3: Rewrite base.md — The Soul

**Files:**
- Modify: `agent/prompts/base.md`
- Test: `agent/prompt_resolver_test.go`, `agent/profile_test.go`

**Step 1: Save current base.md to a backup for reference**

```bash
cp agent/prompts/base.md agent/prompts/base.md.bak
```

**Step 2: Write new base.md**

~80 lines covering four sections:
1. **Identity & Values** — You are serf. Honesty. Persistence. Correctness over speed.
   Efficient and productive — don't waste time, don't rush.
2. **Role: Coordinator** — Understand the task, break it down, dispatch subagents, verify
   their output. Accountable for outcomes. Enforce values on subagents.
3. **Skills** — You have skills for specialized methodologies. Consider which apply before
   starting. List core skills with 1-line descriptions. Tell subagents which skills to use.
4. **submit_result** — Trimmed: call when task is complete and verified. Must have evidence.

Reference `agent/prompts/base.md.bak` for specific wording to preserve from the soul-relevant
sections (principles, persistence, submit_result). Discard TDD cycle, debugging methodology,
verification flowcharts, subagent delegation patterns, workflow details.

**Step 3: Run existing tests and fix any that assert on removed content**

Run: `go test ./agent/ -run TestResolveSystemPrompt -v`
Run: `go test ./agent/ -run TestAllProfiles -v`
Run: `go test ./agent/ -run TestEmbeddedPrompts -v`

Tests that check for TDD keywords, debugging sections, or delegation patterns in base.md
will fail. Update them to check for the new soul content instead. Key tests to update:

- `TestEmbeddedPrompts_ContainCoreGuidance` (prompt_resolver_test.go:53) — update expected strings
- `TestAllProfiles_SystemPromptContainsTaskListGuidance` (profile_test.go:259)
- `TestAllProfiles_SystemPromptContainsSubmitResultGuidance` (profile_test.go:289)
- `TestAllProfiles_SystemPromptContainsSubagentGuidance` (profile_test.go:390)

**Step 4: Run full test suite**

Run: `go test ./agent/ -short -count=1`
Expected: All PASS.

**Step 5: Commit**

```
git add agent/prompts/base.md agent/prompt_resolver_test.go agent/profile_test.go
git commit -m "prompt: rewrite base.md as soul-only (identity, values, coordinator role)"
```

**Step 6: Remove backup**

```bash
rm agent/prompts/base.md.bak
```

---

### Task 4: Write the ops-task core skill

**Files:**
- Create: `agent/skills/ops-task/SKILL.md`
- Test: `agent/builtin_skills_test.go`

**Step 1: Write SKILL.md**

The ops-task skill covers fix/build/configure workflows — the kind of work that dominates
benchmark tasks. Distill from base.md's workflow section (lines 296-418):

- Install missing dependencies before retrying
- Read the complete error message before attempting fixes
- Try different approaches when stuck (3-strike rule)
- Keep an approach log to survive compaction
- Clean up temp files before finishing
- Verify output exists and services respond

Frontmatter:
```yaml
---
name: ops-task
description: "Fix, build, and configure tasks: install deps, try, read errors, fix, verify. Use for debugging broken builds, configuring services, and operational fixes."
---
```

**Step 2: Run builtin skills discovery test**

Run: `go test ./agent/ -run TestEmbeddedSkills -v`

The count assertion (`TestAllEmbeddedSkills_CanBeLoaded`, expects ≥15) should now
pass with one more skill. If it's a hard count, update it.

**Step 3: Commit**

```
git add agent/skills/ops-task/SKILL.md
git commit -m "skill: add ops-task for fix/build/configure workflows"
```

---

### Task 5: Slim agent type prompts and rename test-writer → test-engineer

**Files:**
- Modify: `agent/agents/explorer.md`
- Create: `agent/agents/test-engineer.md` (replacement for test-writer.md)
- Delete: `agent/agents/test-writer.md`
- Modify: `agent/agents/implementer.md`
- Modify: `agent/agents/reviewer.md`
- Test: `agent/builtin_agents_test.go`

**Step 1: Write slim agent prompts**

Each agent type becomes:
- Frontmatter with name, description, tools, color, and optionally skills
- 2-8 lines of identity and values (no methodology — that comes from skills)

explorer.md — read-only, no skills needed, brief identity.
test-engineer.md — skills: [test-driven-development], adversarial quality gate identity.
implementer.md — DRY, YAGNI, careful, responsible, match surrounding style.
reviewer.md — skills: [verification-before-completion], skeptical by default.

**Step 2: Update tests for rename**

In `agent/builtin_agents_test.go`:
- Change all references from "test-writer" to "test-engineer"
- Update `TestBuiltinAgents_LoadsAllRoles` expected agent count/names
- Verify skill field is populated on test-engineer and reviewer
- Update `TestBuiltinAgents_ExplorerTools` if tool lists changed

**Step 3: Run tests**

Run: `go test ./agent/ -run TestBuiltinAgents -v`
Expected: All PASS.

**Step 4: Run full suite to catch any other references to "test-writer"**

Run: `grep -r "test-writer" agent/` — fix any remaining references.
Run: `go test ./agent/ -short -count=1`
Expected: All PASS.

**Step 5: Commit**

```
git add agent/agents/ agent/builtin_agents_test.go
git commit -m "prompt: slim agent types, rename test-writer to test-engineer, add skills frontmatter"
```

---

### Task 6: Update subagent_base.md with skills awareness and value inheritance

**Files:**
- Modify: `agent/prompts/subagent_base.md`
- Test: `agent/builtin_agents_test.go`

**Step 1: Add two sections to subagent_base.md**

After the existing "Non-interactive" section, add:

**Skills** (~5 lines): If skills were loaded into your context, follow their methodology.
The coordinator chose them for a reason.

**Values** (~5 lines): You share the coordinator's values: honesty, correctness, thoroughness.
Never fabricate results. A thorough partial result is more useful than a sloppy complete one.

**Step 2: Run tests**

Run: `go test ./agent/ -run TestSubagentBasePrompt -v`
Expected: PASS. May need to update assertions if they check exact content.

**Step 3: Commit**

```
git add agent/prompts/subagent_base.md
git commit -m "prompt: add skills awareness and value inheritance to subagent base"
```

---

### Task 7: Trim system.openai.md

**Files:**
- Modify: `agent/prompts/system.openai.md`
- Test: `agent/profile_test.go`

**Step 1: Remove duplicated behavioral guidance**

Remove from system.openai.md:
- "Bias to action" / persistence directives (now in base.md soul)
- "If you are finishing in under 10 rounds" round-awareness (removed entirely)
- Any content that duplicates what's in base.md or skills

Keep:
- "You are serf" identity line
- apply_patch tool documentation and format
- Exploration/reading files batching guidance
- Editing constraints (ASCII default, no revert, no amend)
- Code implementation standards

Target: ~60 lines from current ~103.

**Step 2: Run tests**

Run: `go test ./agent/ -run TestOpenAI -v`
Run: `go test ./agent/ -run TestResolveSystemPrompt -v`
Expected: PASS (may need to update assertions checking for removed content).

**Step 3: Commit**

```
git add agent/prompts/system.openai.md agent/profile_test.go
git commit -m "prompt: trim system.openai.md, remove duplicated behavioral guidance"
```

---

### Task 8: Full test suite + build verification

**Files:** None (verification only)

**Step 1: Run full test suite**

```bash
go test ./... -short -count=1
```

Expected: All PASS. Fix any failures.

**Step 2: Build binary**

```bash
go build ./cmd/serf/
```

Expected: Clean build.

**Step 3: Smoke test**

Run serf on a simple task locally to verify the new prompts work:

```bash
./serf --provider openai --model gpt-5-mini -- "List the files in the current directory"
```

Verify it responds sensibly and submit_result works.

**Step 4: Commit any remaining fixes**

---

### Task 9: Deploy and benchmark

**Step 1: Cross-compile for Linux**

```bash
GOOS=linux GOARCH=amd64 go build -o serf-linux-amd64 ./cmd/serf/
```

**Step 2: Deploy to flower-garden**

```bash
scp serf-linux-amd64 192.168.118.101:~/git/terminal-bench/serf-linux-amd64
```

**Step 3: Run benchmark**

Run a full 89-task benchmark with the new prompts. Compare against serf-reviewer-full2
(41/89 = 46% with reviewer gate).

```bash
ssh 192.168.118.101 "export PATH=\"\$HOME/.local/bin:\$PATH\" && cd ~/git/terminal-bench && harbor run ... --job-name serf-soul-skills-1 ..."
```

**Step 4: Analyze results**

Use `tools/transcript-viewer.py` to generate failure transcripts. Compare failure
categories (timeout, wrong answer, no submit) against the baseline.

---

## Risks

- **Model doesn't load skills it needs**: Mitigated by listing skills in base prompt
  with usage hints. Also, agent types auto-inject relevant skills via frontmatter.
- **Regression on tasks that benefited from TDD**: The coordinator can still tell
  subagents to follow TDD. It just isn't mandatory for every task type.
- **Token cost of skill injection**: Skills injected via frontmatter add to the subagent's
  system prompt. Keep skills concise to avoid bloat.
- **Test churn**: Many existing tests assert on specific strings in system prompts.
  Expect 10-20 test updates. Use `grep -r` to find all references before modifying.
