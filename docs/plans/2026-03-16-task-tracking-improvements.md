# Task Tracking Improvements

## Problem

The task_list tool exists but is underused because:

1. No system prompt guidance tells the agent how or when to use it.
2. No dynamic reminders keep the agent oriented on its task list.
3. Tasks are a flat list with no dependency relationships or ordering.
4. There is no "current task" or "what's next" concept — the agent must
   figure this out from the raw list every time.
5. The `undone` status is unnatural; `open` is standard.

## Design

### Status rename

`undone` becomes `open`. Persisted task files are ephemeral per-session,
so no migration needed. All four statuses: `open`, `in_progress`, `done`,
`cancelled`.

Update locations: Go constants, tool schema enum, tool description
string, and validation error messages.

### Task struct changes

Add to `Task`:

- `DependsOn []int` — IDs of tasks this one depends on. Empty means no
  dependencies. Informational, not enforced — the agent can work on any
  task regardless of dependency state.

No separate Priority field. Task IDs are monotonically increasing and
serve as the tiebreaker when multiple tasks are eligible.

### Tool schema changes

**`append` action** gains optional `depends_on` per task:

```json
{
  "action": "append",
  "tasks": [
    {"description": "Build auth", "prompt": "...", "depends_on": [1, 2]}
  ]
}
```

**`update` action** gains optional `depends_on`:

```json
{
  "action": "update",
  "updates": [
    {"id": 3, "status": "in_progress", "depends_on": [1, 2]}
  ]
}
```

Setting `depends_on` to `[]` clears dependencies. Omitting it leaves
them unchanged.

No new actions. Reordering is achieved by modifying `depends_on`.

### Validation

- `depends_on` IDs must reference existing tasks. Reject with an error
  if any referenced ID does not exist.
- Circular dependencies are rejected. On append or update, validate that
  the new dependency graph is acyclic (simple DFS cycle detection).

### Dependency resolution and "next task"

When determining eligible next tasks:

1. Collect all tasks with status `open`.
2. Filter to those whose `depends_on` are all satisfied. A dependency is
   satisfied if its status is `done` or `cancelled`.
3. Sort by ID (insertion order).

This runs whenever a task is marked `done` or `cancelled`. The tool exec
handler (not TaskStore) formats the response:

- Confirmation of the status change.
- If exactly one eligible task: "Next task: #N — description. Mark it
  in_progress to begin."
- If multiple eligible tasks: "Ready tasks: #N — desc, #M — desc, ...
  Pick one and mark it in_progress."
- If no tasks are ready (all blocked or all done): state this.

TaskStore remains a clean data layer. The next-task enrichment lives in
the tool execution handler in session.go.

### System prompt guidance (static)

Add a task_list guidance section to core.md (shared across all providers
and subagents). Content:

- Use `task_list` to plan and track multi-step work.
- Create tasks at the start of complex work.
- Mark tasks `in_progress` before starting, `done` when complete.
- Use `depends_on` to express ordering relationships between tasks.
- The tool will suggest what to work on next when you complete a task.

### Dynamic task reminders

Three triggers inject system reminders into the conversation. Any
task_list tool call (including `view`) resets the round counter.

**1. After context compaction**

Always inject if tasks exist. Content: the full task list with statuses,
dependencies, and notes. The agent just lost detailed history and needs
complete reorientation.

**2. Tasks exist, tool not used in 5 rounds**

Content: current `in_progress` task(s), next 3 eligible tasks (by
dependency resolution), and overall progress summary
("Progress: 3/7 tasks complete").

**3. No tasks exist, tool never used, 10 rounds in**

Content: suggestion to consider using `task_list` to organize work.
Fires once, not repeatedly.

### Injection mechanism

The session tracks:

- `taskToolLastRound int` — round number of last task_list tool call.
- `taskToolEverUsed bool` — whether task_list has ever been called.

Before building each LLM request, check the trigger conditions and
inject a system reminder. The reminder uses the same mechanism as the
existing post-compaction task snapshot injection (via OnCompactionTurn
callback), extended to cover the periodic triggers.

The reminder is injected as a system-level message appropriate to each
provider: `developer` role for OpenAI, `system` turn for Anthropic,
`systemInstruction` addition for Gemini. Implementation should follow
the existing pattern used for compaction summaries.

### What doesn't change

- Task file per session (subagent isolation preserved).
- Subagents get their own task lists with full capabilities.
- Atomic file persistence (temp + rename).
- Notes accumulation on updates.
- Task snapshots in context compaction metadata.
