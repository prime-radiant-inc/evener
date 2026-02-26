# Prompt Redesign: Soul + Skills

**Date**: 2026-02-26
**Status**: Design
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

## Migration

1. Write new base.md (~80 lines)
2. Write 5 core skill files
3. Slim agent type prompts, add `skills` frontmatter
4. Implement `skills` frontmatter parsing + injection in Go
5. Trim system.openai.md
6. Update tests
7. Benchmark: run terminal-bench to compare pass rate

## Risks

- **Model doesn't load skills it needs**: Mitigated by listing skills in base prompt
  with usage hints. Also, agent types auto-inject relevant skills via frontmatter.
- **Regression on tasks that benefited from TDD**: The coordinator can still tell
  subagents to follow TDD. It just isn't mandatory for every task type.
- **Token cost of skill injection**: Skills injected via frontmatter add to the subagent's
  system prompt. Keep skills concise to avoid bloat.
