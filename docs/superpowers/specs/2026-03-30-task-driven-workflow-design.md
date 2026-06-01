# Task-Driven Agent Workflow

## Problem

The coordinator's behavior varies wildly across runs. The v55 experiment
showed that xhigh reasoning during planning and verification improves pass
rates (+0.333 to +1.0 on 7/9 tasks), but running xhigh on every round
wastes tokens on mechanical steps like listing files or calling spawn_agent.

The workflow (plan, inventory, delegate, verify, fix, submit) is prose in
the system prompt. The model sometimes follows it, sometimes wings it.
Stochastic compliance with existing instructions is the dominant failure
mode across 25 investigated tasks.

## Solution

Move the workflow from prose instructions into a structured task list that
the framework manages. Each task has a reasoning level, a prompt, and a
title. The framework auto-advances through tasks, auto-injects prompts as
system messages, and auto-sets reasoning effort based on the current task.

Agents can modify their task list at any time. The coordinator can pass
structured task lists to subagents when spawning them.

## Design

### Task struct

Add `ReasoningEffort` and `Insert` fields to the existing Task in
`agent/task_store.go`:

```go
type Task struct {
    ID              int        `json:"id"`
    Type            TaskType   `json:"type"`
    Description     string     `json:"description"`
    Prompt          string     `json:"prompt,omitempty"`
    Status          TaskStatus `json:"status"`
    DependsOn       []int      `json:"depends_on,omitempty"`
    Notes           []string   `json:"notes,omitempty"`
    ReasoningEffort string     `json:"reasoning_effort,omitempty"`
    Insert          string     `json:"insert,omitempty"`
}
```

- `ReasoningEffort`: when this task is `in_progress`, the framework sets the
  session's reasoning effort to this value. When empty, session default applies.
- `Insert`: only used in agent definition templates. `insert: parent_tasks`
  marks the placeholder where parent-provided tasks get spliced in.

### Agent definition format

YAML frontmatter in agent `.md` files gets a `tasks` array:

```yaml
---
name: implementer
description: "Code implementation agent."
tasks:
  - title: Understand requirements
    prompt: "Read the spec. Read tests. Explore the codebase."
    reasoning_effort: low
  - title: Do the work
    insert: parent_tasks
    prompt: "Implement the solution. Keep changes minimal and focused."
    reasoning_effort: low
  - title: Verify
    prompt: "Verify output works. Check all constraints. Run tests."
    reasoning_effort: low
  - title: Clean up
    prompt: "Remove scratch files. Never delete pre-existing workspace files."
    reasoning_effort: low
---

[prompt body: universal rules, identity, standards — no workflow steps]
```

Parsed by extending `parsePluginAgent()` in `plugin_agents.go`:

```go
type TaskTemplate struct {
    Title           string `yaml:"title"`
    Prompt          string `yaml:"prompt"`
    ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
    Type            string `yaml:"type,omitempty"`
    Insert          string `yaml:"insert,omitempty"`
}
```

Every agent type gets default tasks. The prompt body shrinks to universal
rules and identity — workflow steps live in the task list.

### Session startup

When a session starts for a named agent with `tasks` in its definition:

1. Create the TaskStore
2. Populate it with the agent's default tasks
3. Auto-start task #1 (set to `in_progress`)
4. Inject task #1's prompt as a system message
5. Set reasoning_effort from task #1

### Auto-advance

When the agent marks a task `done` via the task_list tool:

1. Find the next eligible task (lowest ID, all deps satisfied)
2. Set it to `in_progress`
3. Set reasoning_effort from the new task (or session default if unset)
4. Inject system message:
   ```
   [Task #N: Title | reasoning: level]
   Full task prompt text here...
   ```

Sequential advance by default. If the agent explicitly starts a different
task in the same tool call, that takes precedence.

### All-tasks-complete nudge

When all tasks are `done` or `cancelled` and the agent hasn't called
communicate:

```
All tasks on your list are complete. If you have remaining work, add
it to your task list. Otherwise, use communicate to indicate you're done.
```

### spawn_agent task_list

The spawn_agent tool gets an optional `task_list` parameter:

```json
{
  "task": "Implement the eigenvalue solver...",
  "agent_type": "implementer",
  "task_list": [
    {"title": "Read eval.py", "prompt": "Understand the benchmark interface"},
    {"title": "Implement with scipy LAPACK", "prompt": "Use dgeev wrapper"},
    {"title": "Benchmark sizes 2-10", "prompt": "Must beat numpy reference"}
  ]
}
```

When the subagent starts:

1. Load agent's default tasks
2. Find the task with `insert: parent_tasks`
3. If parent provided `task_list`: splice items at that position, remove
   placeholder
4. If no `task_list`: keep placeholder as a normal task with its own
   title/prompt
5. Inject all tasks into TaskStore, auto-start #1

The `task` string parameter remains as free-text context. The `task_list`
is the structured work plan. They complement each other.

### resume_agent task_list

Same parameter added to resume_agent (send_input). Tasks always append
to the existing list (not placeholder insertion — the agent is mid-work).

### Dynamic reasoning effort

Before each LLM call in the session loop:

1. Check TaskStore for current `in_progress` task
2. If it has `ReasoningEffort` set: `s.SetReasoningEffort(task.ReasoningEffort)`
3. If no in_progress task or no effort specified: use session default

The session default (`serf_agent.py`) reverts to `"low"`. The coordinator's
Plan and Verify tasks declare `xhigh` where needed.

### Prompt slimming

**What moves to task list:** numbered workflow steps, step-specific
instructions, process flow.

**What stays in prompt body:** universal identity/role, hard rules (must
spawn implementer, never write code, one implementer gets the whole
problem, submitting hard gate), implementation standards, spec authority,
when you get stuck, output integrity.

**Rule of thumb:** if it applies regardless of which step the agent is on,
it stays in the prompt. If it's guidance for a specific step, it goes in
that task's prompt.

### Coordinator default tasks

```yaml
tasks:
  - title: Inventory
    prompt: >
      List files and note any tests or verification scripts.
      Listing, not reading. Do not read source files, data files,
      or skill files — the implementer will do that.
    reasoning_effort: low
  - title: Plan
    prompt: >
      Analyze the task requirements and workspace inventory.
      What does this task require? What are the acceptance criteria?
      What approach should the implementer take? What are the risks?
      Write out the COMPLETE delegation prompt — every detail, every
      constraint, every file path. Do not paraphrase the spec —
      include it verbatim plus your analysis. Plan how you will
      verify the result. Create the task list for the implementer.
    reasoning_effort: xhigh
  - title: Delegate
    prompt: >
      Spawn ONE implementer with your delegation prompt and task list.
      Use max_turns=100, reasoning_effort=low. Always delegate into
      the project root.
    reasoning_effort: low
  - title: Verify
    prompt: >
      Independently verify the deliverable. Do not trust the completion
      report. Run test suites if they exist. Check file existence and
      structure. When no tests exist, check acceptance criteria directly.
      Running the deliverable for acceptance testing IS allowed.
    reasoning_effort: xhigh
  - title: Fix (if needed)
    prompt: >
      If verification found issues, spawn a fix agent with the specific
      failure output. Then re-verify. If verification passed, skip this
      task.
    reasoning_effort: xhigh
  - title: Submit
    prompt: >
      List the workspace directory. Remove files YOU created during
      verification. Never remove files the implementer created. Then
      call communicate.
    reasoning_effort: low
```

### Implementer default tasks

```yaml
tasks:
  - title: Understand requirements
    prompt: >
      Read the spec requirements carefully. Read and understand ALL
      pre-written tests if provided. Explore the codebase for patterns,
      conventions, and existing code you can build on.
    reasoning_effort: low
  - title: Do the work
    insert: parent_tasks
    prompt: >
      Implement the solution. Keep changes minimal and focused.
    reasoning_effort: low
  - title: Verify
    prompt: >
      Verify your output WORKS, not just that it exists. Test with the
      exact inputs and interface the task spec describes. Check ALL
      stated constraints. Run the project's tests.
    reasoning_effort: low
  - title: Clean up
    prompt: >
      Remove files your process created that aren't deliverables.
      Never delete files that were in the workspace when you started.
      Never delete build outputs the spec asks you to produce.
    reasoning_effort: low
```

## Files to modify

| File | Change |
|------|--------|
| `agent/task_store.go` | `ReasoningEffort` + `Insert` fields, `CurrentInProgress()` method, auto-advance logic |
| `agent/plugin_agents.go` | Parse `tasks` from YAML, `TaskTemplate` struct |
| `agent/session.go` | Dynamic reasoning from current task, startup task injection |
| `agent/profile.go` | `task_list` param on spawn_agent and resume_agent |
| `agent/agents/coordinator.md` | YAML tasks, slim prose to role + hard rules |
| `agent/agents/implementer.md` | YAML tasks, slim prose to standards + identity |
| `agent/agents/reviewer.md` | YAML tasks |
| `agent/agents/explorer.md` | YAML tasks |
| `agent/agents/worker.md` | YAML tasks |
| `agent/task_reminders.go` | Show reasoning_effort in task displays |
| `tools/serf_agent.py` | Revert reasoning_effort default to "low" |

## Migration path

Each step independently deployable:

1. Task struct: add `ReasoningEffort` + `Insert` fields (backward compatible)
2. Agent definition: parse `tasks` from YAML (no-op if no tasks defined)
3. Startup injection: populate TaskStore from agent defaults
4. Auto-advance: on task completion, start next eligible + inject prompt
5. Dynamic reasoning: set effort from current task before each LLM call
6. spawn_agent task_list: optional parameter, old calls unchanged
7. resume_agent task_list: same
8. Coordinator prompt: add YAML tasks, slim body
9. Implementer prompt: add YAML tasks, slim body
10. Other agents: add YAML tasks
11. Revert serf_agent.py to reasoning_effort="low"

## Verification

1. `go test ./agent/...` — all existing tests pass at each step
2. Local test: run coordinator against a simple task, verify task
   auto-population, reasoning switching, prompt injection, spawn with
   task_list
3. Eval: 3 reps on regression set + target tasks, compare against v55
4. Confirm system prompt size decreased (fewer tokens)
