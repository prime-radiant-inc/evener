# Transcript Structure View — Design

Date: 2026-04-05
Context: Serf eval dashboard (`tools/dashboard/`)

## Purpose

Give a reader the *shape* of an eval run at a glance — did it pass, what the
coordinator delegated, what task lists got handed around, what each agent said
at the end — without any of the turn-by-turn tool-call detail that the
existing Trajectory view already shows.

## Placement

A new sub-page of the task detail, reached via the route
`#/runs/{job}/tasks/{task}/structure` (with optional `?trial=HASH` for
multi-rep tasks). The existing task detail page gains a link at the top:
**"View structure →"**. Breadcrumbs on the structure page read: Dashboard /
Runs / `{job}` / `{task}` / Structure.

The structure page lives in `app.js` alongside the existing routes. No
separate HTML file.

## Page layout (top to bottom)

1. **Outcome badge.** `PASS` / `FAIL` / `PARTIAL` with the numeric reward.
   - reward == 1.0 → `PASS` (green)
   - reward == 0.0 → `FAIL` (red)
   - 0 < reward < 1 → `PARTIAL` (amber, shows the value)
   - no reward → `NO REWARD` (grey)
   Source: `task.reward` from the existing task-detail JSON.
2. **Task + job identifier line.** `{task_name} · {job_name}` plus trial hash
   if present.
3. **Root session card.** The coordinator. See "Session card" below.

## Session card (recursive)

Every session in the transcript tree becomes one card. The top-level card is
the coordinator; delegated sub-sessions render as nested cards inside their
parent's Delegations section. Each card has:

### Header

A single line: `agent_type · model · session_id[:8]`, plus a small
right-aligned link "full trajectory →" that jumps to the regular task detail
page scrolled to that session.

### Section 1: System prompt

Collapsed by default. Shown in monospace `<pre>` when expanded. Source:
- Root session: `task.system_prompt` (already on the response).
- Child sessions: needs to be added to the API response. Currently the
  backend only surfaces `system_prompt` for the root session; we will add
  it to each trajectory node.

### Section 2: Initial task list

The first `task_list` tool call with `action: "append"` in this session.
Rendered as a numbered list, one item per task:

```
1. inspect workspace
   Inventory the workspace contents and identify any tests or…
2. implement image generator
   …
```

Where item line 1 = `task.description`, line 2 = `task.prompt` (collapsed past
~120 chars). If the session has no `task_list` call, this section is omitted
entirely (no empty heading).

### Section 3: Delegations

One row per `spawn_agent` tool call in the session, in call order. Each row
shows:

- A one-line header: `→ {agent_type}` with small badges for `model`,
  `max_turns`, `reasoning_effort`.
- The `task` text, collapsed past ~200 chars with "show more".
- The `task_list` argument if present, rendered the same way as Section 2.
- A ▸ expander that, when opened, renders the **matching child session's
  card inline**. The match is `tool_call.id ==
  child_session.parent_tool_call_id`. If no child session matches (e.g.
  spawn failed), show a "(no matching child session)" placeholder.

`close_agent` calls are ignored — they don't add signal.

### Section 4: Final response

The text content of the LAST `ASSISTANT` round in this session. If the last
round has no text (tool-call-only), walk backwards to the most recent round
with text. Collapsed past ~500 chars with "show more". If no text is ever
found (unlikely but possible for a session that only emits tool calls),
render "(no text output)".

## Data flow

```
User navigates to #/runs/{job}/tasks/{task}/structure
  ↓
app.js fetches /api/runs/{job}/tasks/{task}?trial=...  (existing endpoint)
  ↓
Build session map: { session_id → session_node } from:
    - task.trajectory[] (root sessions)
    - task.trajectory[].children[] (sub-sessions)
  ↓
For each session_node, extract:
    system_prompt     : from node.system_prompt (needs backend change)
    initial_tasklist  : first task_list tool_call with action=="append"
    delegations       : all spawn_agent tool_calls, paired with children
                        by tool_call.id == child.parent_tool_call_id
    final_response    : last ASSISTANT round's text (walk back if empty)
  ↓
Render root session card → recursively render delegation children
```

No new endpoint is added. The logic for extracting structure lives entirely
in `app.js`. This keeps the API surface small and avoids duplicating
traversal logic across frontend and backend.

## One required backend change

`server.py` `get_task()` currently pulls `system_prompt` only from the
root (`depth == 0`) session and attaches it as `task.system_prompt`. For
the structure view, each nested session card needs its own system prompt.

The fix: when building the `trajectories` list in `server.py`, include
`system_prompt` on each trajectory node (root and child). Two places:

```python
# around line 90 — in the root loop
trajectories.append({
    "session_id": root_session["session_id"],
    "model": root_session["model"],
    "depth": root_session["depth"],
    "system_prompt": root_session.get("system_prompt", ""),  # NEW
    "trajectory": build_trajectory(root_session),
    "children": [
        {
            ...
            "system_prompt": child.get("system_prompt", ""),  # NEW
            ...
        }
        ...
    ],
})
```

Keep `task.system_prompt` as well (for the existing detail page that reads
it). This is strictly additive.

## Styling

Reuse existing dashboard primitives. No new CSS variables.

- Session cards: existing `.card` / `.card-body-flush` classes from
  `style.css`.
- Collapsible sub-sections: existing `twoStateSection` helper in `app.js`
  (currently used for the System Prompt section on the task detail page).
- Nesting: nested delegation cards get a left-border accent
  (`border-left: 2px solid var(--border-subtle)`, or equivalent existing
  token) to visually mark depth.
- Pass/fail badge: reuse existing status-badge color tokens already in
  `style.css` (the task grid uses them).

## Default expansion states

Inside each session card, top to bottom: **System prompt → Initial task list
→ Delegations → Final response**.

Rationale: the system prompt anchors the reader to what the agent was told,
then the task list shows the plan, then delegations show the work, then the
final response shows the answer.

Defaults on first load:

- **System prompt** — collapsed (long, rarely the signal).
- **Initial task list** — expanded (short, high signal).
- **Delegations section** — expanded (this is why the reader came).
- **Each delegation's child-session expander ▸** — collapsed (reader clicks
  to dive into the subagent). Keeps the root card compact and forces a
  deliberate choice to go deeper.
- **Final response** — expanded (short, and it's the answer).

## Edge cases

- **No transcripts at all** (queued / not started): render the outcome
  badge and a single line "No sessions recorded yet."
- **Session with zero rounds** (header only): card shows system prompt and
  "(no rounds executed)" for the other sections.
- **Delegation whose child session doesn't exist in the transcript tree**
  (sub-session failed to start, was stripped, etc.): show the delegation
  row with its task text, mark expander as `(no matching child session)`.
- **Multiple root sessions** (possible for some agent configurations):
  render each as a top-level session card. Existing backend already
  returns a list of root trajectories.
- **Truncation markers from backend** (the 8000-char tool-result
  truncation applied in `eval_dashboard.py`): does not affect this view
  because we don't render tool results at all. `task` and `task_list`
  arguments can be long but are not currently truncated by the backend.

## Out of scope

- Exporting the structure view to markdown.
- Comparing two runs' structure side by side.
- Showing tool-call counts, round counts, or any execution metrics beyond
  the pass/fail outcome badge.
- Editing / filtering. The view is read-only and shows everything it
  extracts.
- Tests for `app.js`. The dashboard does not currently have frontend
  tests; adding a test harness is a separate project.

## Testing

Backend:
- Extend `tools/dashboard/test_server.py` to assert that
  `/api/runs/{job}/tasks/{task}` returns `system_prompt` on both root and
  child trajectory nodes.
- Extend `tools/dashboard/test_data.py` or `test_trajectory.py` as needed
  if the system_prompt extraction moves into `data.py` / `trajectory.py`.

Frontend:
- Manual verification against a real transcript tree with at least one
  delegation (the existing test fixtures under `test_data.py` should
  provide one).

## Implementation order

1. Backend: add `system_prompt` to each trajectory node in `server.py`,
   with a test.
2. Frontend: add the `#/runs/.../structure` route, the outcome badge, and
   the root session card rendering (no delegation expansion yet).
3. Frontend: wire up delegation expansion (recursive child rendering) and
   session-map lookup by `parent_tool_call_id`.
4. Frontend: add the "View structure →" link on the existing task detail
   page.
5. Manual verification against two or three real tasks (pass and fail
   outcomes, with and without delegations).
