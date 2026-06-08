# subagent-list-and-output: list_agents enumerates a child and subagent_output peeks it (raw, non-consuming)

**What this covers**: the two root-only read tools — `list_agents`
(`agent/internal/tool/definitions.go` `DefListAgents`,
`agent/subagents.go` `listAgents`) and `subagent_output`
(`DefSubagentOutput`, `agent/subagent_output.go`). A parent spawns a
child that does a small checkable task AND emits a credential-looking line
in its report. The parent then (a) `list_agents` to confirm the child
appears with its status / closed / task / transcript_ref, (b)
`subagent_output(view:"result")` and `subagent_output(view:"outline")` to
peek the child. Two invariants are asserted: the credential
`sk-LIVETEST123456` is returned **verbatim** (there is no redaction
layer) by subagent_output, and subagent_output is **non-consuming** — a
peek does not spend the result, so the snapshot stays readable
afterward.

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
   proj=$(mktemp -d -t serf-e2e-list-XXXXX)
   ```

2. **One root run: spawn a checkable child, list it, peek it twice.** The
   child is told to do a tiny verifiable job and to put the literal
   credential string in its communicate message:
   ```bash
   /tmp/serf --model openai/gpt-5.4-mini --dir "$proj" --max-subagent-depth 1 \
     "Do these steps strictly in order and report exactly what each tool returned.
      (1) Call spawn_agent with blocking=true and this task: 'Using the shell tool, run: echo hello-from-child. Then call communicate with this exact message (include the credential token verbatim): RESULT=hello-from-child API_KEY=sk-LIVETEST123456'. Capture the agent_id.
      (2) Call list_agents (no arguments). Report the full JSON. Confirm the child you just spawned is in the list, and report its status, closed, task, and transcript_ref.
      (3) Call subagent_output with that agent_id and view=result. Report the full JSON it returned, including the content field VERBATIM.
      (4) Call subagent_output with that agent_id and view=outline. Report whether the content shows the child's turns.
      (5) Call subagent_output with that agent_id and view=result a SECOND time. Report whether it still returns content (a peek must not consume the result).
      Finally, answer two questions explicitly: (A) does the string sk-LIVETEST123456 appear verbatim anywhere in the subagent_output content? (B) did the second view=result peek still return the result?"
   ```
   Wait for exit 0.

## Expected

- After step 2, `list_agents` returns `{"agents":[{...}],"count":1}` (or
  more if prior children exist in the same run — here just the one). The
  child record carries: `agent_id`, `status:"completed"`,
  `closed:false`, `task` (the spawn task text), `agent_type`,
  `turns_used`, `result_available`, `result_consumed`, and
  `transcript_ref:"local:<id>"`.
- After step 3, `subagent_output(view:"result")` returns a wire object
  `{"agent_id":"<id>","view":"result","content":"...","truncated":false}`.
  Inside `content` (a JSON-stringified snapshot), the child's output that
  contained `sk-LIVETEST123456` appears **verbatim** — there is no
  redaction layer, so the literal `sk-LIVETEST123456` is present.
- After step 4, `view:"outline"` renders the child's per-turn outline in
  `content` (raw, still bounded by `max_bytes`).
- After step 5, the SECOND `view:"result"` peek STILL returns the
  populated snapshot — subagent_output never sets `result_consumed`, so
  peeking is idempotent. Both peeks return the same snapshot.
- The model's final A/B answers: (A) `sk-LIVETEST123456` appears
  verbatim; (B) the second peek still returned the result.
- Falsification:
  - `sk-LIVETEST123456` does NOT appear inside a `subagent_output` tool
    result's `content` (masked, blanked, or otherwise altered) → the
    raw-passthrough contract regressed (`subagent_output` must return the
    child's output unmodified). This is the primary regression guard.
  - The wire object carries a `redaction` field → the removed redaction
    layer was reintroduced.
  - `list_agents` returns `count:0` / an empty `agents` array although a
    child was just spawned in this run → the live-child enumeration
    regressed.
  - The second `view:"result"` peek returns empty/`note:"...not a tracked
    child..."` or an "already consumed" error → the peek consumed the
    result (it must not; only `wait` consumes).

## Cleanup

- Leave `$proj` and the temp dir on disk (no `rm -rf`). Fresh `mktemp -d`
  per run keeps reruns hermetic. Transcripts persist under
  `~/.local/state/serf/projects/<bucket>/sessions/`.

## Sharp edges

- **There is no redaction.** The credential is returned verbatim
  everywhere — in the `spawn_agent` (blocking) result's `output` field,
  in the `list_agents` record's `task` field (that field carries the
  spawn task TEXT, which the prompt deliberately wrote the credential
  into), and in `subagent_output`'s `content`. A plain `grep
  sk-LIVETEST123456` over stdout matching all of these is EXPECTED, not a
  failure.
- `subagent_output` `content` is the raw snapshot **JSON-stringified
  inside an outer JSON object** — so it arrives escaped (`"` → `\"`).
  Grep for the bare token `sk-LIVETEST123456`; escaping does not hide it,
  so a verbatim match is still a substring match.
- **Verify against the transcript, not stdout**, to read the actual
  subagent_output tool results (rather than the model's narration) and
  confirm THOSE show the token verbatim:
  ```bash
  SID=<parent_session_id from the list_agents record>
  TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
  python3 - "$TS" <<'PY'
  import json,sys
  for line in open(sys.argv[1]):
      e=json.loads(line)
      for c in e.get("turn",{}).get("message",{}).get("content",[]):
          tr=c.get("tool_result")
          if tr and tr.get("name")=="subagent_output":
              print(tr["content"])
  PY
  ```
  Every printed subagent_output content must contain `sk-LIVETEST123456`
  verbatim and must NOT contain any redaction marker.
- `list_agents` default hides only `closed` records; a freshly-completed
  child is `completed`, so it shows without `include_closed`. (Closed
  retention is a separate card: `subagent-close-retains.md`.)
- Use `blocking=true` on the spawn so the child has finished (status
  `completed`, result retained) by the time list/peek run — a still-
  running child would have `status:"running"` and no output yet.
- The model surfaces the shell tool as `[tool] shell`; the prompt says
  "the shell tool" to stay tool-name-agnostic.
