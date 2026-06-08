# subagent-close-retains: close_agent destroys the session but RETAINS a closed record, hidden from the default list

**What this covers**: `close_agent` retention semantics
(`agent/internal/tool/definitions.go` `DefCloseAgent`,
`agent/subagents.go` `closeAgent`) and the `list_agents` default-hide /
include_closed surfacing (`agent/subagent_manager.go` `listAgents` +
`subagentMatchesFilter`). A parent spawns and `wait`s a child to
completion, then `close_agent`s it. `list_agents` with no args must HIDE
the closed child; `list_agents({include_closed:true})` must SURFACE it
with `status:"closed"`. This proves close retains an audit record (it does
not silently vanish) while keeping it out of the default working view.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env (zsh — `"$PWD/.env"`):
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model. `openai/gpt-5.4-mini` is enough; the recipe uses it.
- `--max-subagent-depth 1` so the root may spawn one child.
- State persistence on (default for the CLI).

## Steps

1. Hermetic project dir:
   ```bash
   proj=$(mktemp -d -t serf-e2e-close-XXXXX)
   ```

2. **One root run: spawn + wait + close, then list both ways.** Use a
   blocking spawn so the child is finished before the close:
   ```bash
   /tmp/serf --model openai/gpt-5.4-mini --dir "$proj" --max-subagent-depth 1 \
     "Do these steps strictly in order and report exactly what each tool returned.
      (1) Call spawn_agent with blocking=true and this task: 'Using the shell tool, run: echo close-test-child. Then call communicate with the message CHILD_DONE.' Capture the agent_id and confirm the child completed.
      (2) Call close_agent on that agent_id. Report the full JSON it returned (its status field especially).
      (3) Call list_agents with NO arguments. Report the full JSON. State whether the closed child's agent_id appears in this default list.
      (4) Call list_agents with include_closed set to true. Report the full JSON. State whether the closed child's agent_id appears now, and what its status is.
      Finally, answer two questions: (A) did the default list_agents (step 3) HIDE the closed child? (B) did include_closed=true (step 4) SHOW it with status closed?"
   ```
   Wait for exit 0.

## Expected

- After step 2, `close_agent` returns the final snapshot with
  `status:"closed"` (same wire shape as `wait`: `agent_id`, `status`,
  `reason`, `output`, `success`, `turns_used`, `transcript_ref`). Because
  the child completed before close, `reason` is the retained
  `completed` and `success` is `true` — close reports the LAST RUN's
  outcome, not a new one.
- After step 3, the default `list_agents` does NOT contain the child's
  `agent_id`. With only this one (now-closed) child, the result is
  `{"agents":[],"count":0}`.
- After step 4, `list_agents({include_closed:true})` DOES contain the
  child, with `status:"closed"` and `count:1`. Its record still carries
  the task, transcript_ref, and the retained `reason:"completed"`.
- The model's final A/B answers: (A) default list hid the closed child;
  (B) include_closed surfaced it with `status:"closed"`.
- Falsification:
  - The closed child still appears in the **default** `list_agents`
    (step 3) → the default-hide-closed contract regressed
    (`subagentMatchesFilter` no longer excludes closed without
    include_closed).
  - The child is ABSENT from `include_closed:true` (step 4) → close
    REMOVED the record instead of retaining it as closed. This is the
    core retention regression this card guards (close must retain, not
    delete).
  - `close_agent` returns `status` other than `closed` (e.g. still
    `completed`/`running`) → the close transition did not land.

## Cleanup

- Leave `$proj` and the temp dir on disk (no `rm -rf`). Fresh `mktemp -d`
  per run keeps reruns hermetic. The child's transcript persists on disk
  under `~/.local/state/serf/projects/<bucket>/sessions/` even after
  close — close destroys the live SESSION, not the transcript file.

## Sharp edges

- **`close` ≠ remove.** The closed record is retained (bounded by the
  per-parent terminal-retention cap, default 128) precisely so the audit
  trail survives; it is merely hidden from the default `list_agents`.
  `status="closed"` as a filter implies `include_closed=true`, so either
  `include_closed:true` or `status:"closed"` surfaces it — this card uses
  `include_closed:true`.
- Use `blocking=true` on the spawn so the child is already `completed`
  when close runs; the snapshot then shows `reason:"completed"`,
  `success:true` under `status:"closed"` (retained last-run outcome). A
  close on a still-running child would first stop the run — a different
  path, not what this card exercises.
- A closed child cannot be resumed (its session is destroyed);
  `subagent_output(view:"result")` on it still works and reports
  `status:"closed"`, but that peek is covered by
  `subagent-list-and-output.md`, not here.
- The model surfaces the shell tool as `[tool] shell`; the prompt says
  "the shell tool" to stay tool-name-agnostic.
- If a future change raises the default `list_agents` to include closed
  records, step 3 would show the child and falsify — that is the intended
  alarm, not a flaky test.
