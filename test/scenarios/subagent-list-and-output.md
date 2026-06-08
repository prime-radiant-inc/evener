# subagent-list-and-output: list_agents enumerates a child and subagent_output peeks it (redacted, non-consuming)

**What this covers**: the two root-only read tools — `list_agents`
(`agent/internal/tool/definitions.go` `DefListAgents`,
`agent/subagents.go` `listAgents`) and `subagent_output`
(`DefSubagentOutput`, `agent/subagent_output.go`). A parent spawns a
child that does a small checkable task AND emits a credential-looking line
in its report. The parent then (a) `list_agents` to confirm the child
appears with its status / reason / task / transcript_ref, (b)
`subagent_output(view:"result")` and `subagent_output(view:"outline")` to
peek the child. Two invariants are asserted: the credential
`sk-LIVETEST123456` is **redacted** (masked, never verbatim) in
subagent_output, and subagent_output is **non-consuming** — a peek does
not spend the result, so the snapshot stays readable afterward.

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
      (2) Call list_agents (no arguments). Report the full JSON. Confirm the child you just spawned is in the list, and report its status, reason, task, and transcript_ref.
      (3) Call subagent_output with that agent_id and view=result. Report the full JSON it returned, including the content field VERBATIM.
      (4) Call subagent_output with that agent_id and view=outline. Report whether the content shows the child's turns.
      (5) Call subagent_output with that agent_id and view=result a SECOND time. Report whether it still returns content (a peek must not consume the result).
      Finally, answer two questions explicitly: (A) does the string sk-LIVETEST123456 appear verbatim anywhere in the subagent_output content, or is it masked? (B) did the second view=result peek still return the result?"
   ```
   Wait for exit 0.

## Expected

- After step 2, `list_agents` returns `{"agents":[{...}],"count":1}` (or
  more if prior children exist in the same run — here just the one). The
  child record carries: `agent_id`, `status:"completed"`,
  `reason:"completed"`, `task` (the spawn task text), `agent_type`,
  `turns_used`, `result_available`, `result_consumed`, and
  `transcript_ref:"local:<id>"`.
- After step 3, `subagent_output(view:"result")` returns a wire object
  `{"agent_id":"<id>","view":"result","redaction":"standard","content":"...","truncated":false}`.
  Inside `content` (a JSON-stringified snapshot), the child's output that
  contained `sk-LIVETEST123456` is **masked to `sk-«redacted»`** — the
  `sk-` prefix is kept, the body replaced by the marker `«redacted»`. The
  literal `sk-LIVETEST123456` does NOT appear.
- After step 4, `view:"outline"` renders the child's per-turn outline in
  `content` (still redacted, still bounded).
- After step 5, the SECOND `view:"result"` peek STILL returns the
  populated snapshot — subagent_output never sets `result_consumed`, so
  peeking is idempotent. Both peeks return the same `«redacted»` snapshot.
- The model's final A/B answers: (A) `sk-LIVETEST123456` is masked, not
  verbatim; (B) the second peek still returned the result.
- **Scope note (do NOT mis-read as a failure):** redaction is applied by
  `subagent_output` ONLY. The verbatim token legitimately appears
  elsewhere in the run output — in the `spawn_agent` (blocking) result's
  `output` field, and in the `list_agents` record's `task` field (that
  field carries the spawn task TEXT, which the prompt deliberately wrote
  the credential into) — because neither `spawn_agent` nor `list_agents`
  redacts. The assertion is specifically about subagent_output's
  `content`. To verify cleanly, read the actual `subagent_output` tool
  results off the transcript (see Sharp edges), not the model's narration.
- Falsification:
  - `sk-LIVETEST123456` appears **verbatim inside a `subagent_output`
    tool result's `content`** → redaction failed (the `sk-` standard rule
    in `agent/redact.go` regressed). This is the primary regression guard.
    (Its presence in the spawn result / list_agents `task` is expected,
    per the scope note — do not falsify on those.)
  - `list_agents` returns `count:0` / an empty `agents` array although a
    child was just spawned in this run → the live-child enumeration
    regressed.
  - The second `view:"result"` peek returns empty/`note:"...not a tracked
    child..."` or an "already consumed" error → the peek consumed the
    result (it must not; only `wait` consumes).
  - `redaction` field reports `"none"` when no redaction arg was passed →
    the default-standard contract regressed (default must be `standard`).

## Cleanup

- Leave `$proj` and the temp dir on disk (no `rm -rf`). Fresh `mktemp -d`
  per run keeps reruns hermetic. Transcripts persist under
  `~/.local/state/serf/projects/<bucket>/sessions/`.

## Sharp edges

- **The redaction marker is `«redacted»` (guillemets), and the `sk-`
  prefix is KEPT.** So the masked form is `sk-«redacted»`, not a blanked
  string. Assert on the absence of `sk-LIVETEST123456`, not on a specific
  redacted spelling — and note the masking keeps the `sk-` prefix
  legible, which is intended (the value, not the class, is the secret).
- The `sk-` rule fires on `\b(sk-)[A-Za-z0-9_-]{8,}` — the test token has
  13 chars after `sk-`, comfortably over the 8-char floor. Do NOT shorten
  the token below 8 trailing chars or the rule won't match and the test
  would falsely "fail" redaction.
- `subagent_output` `content` is the redacted snapshot **JSON-stringified
  inside an outer JSON object** — so it arrives escaped (`"` → `\"`).
  Grep for the bare token `sk-LIVETEST123456`; escaping does not hide it,
  so a verbatim leak is still a substring match.
- **Verify against the transcript, not stdout.** Because the token
  legitimately appears in the spawn result and the list_agents `task`
  field (scope note above), a plain `grep sk-LIVETEST123456` over stdout
  will match those and look alarming. Read the actual subagent_output
  tool results off the parent's transcript and confirm THOSE show
  `«redacted»`:
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
  Every printed subagent_output content must contain `«redacted»` and
  must NOT contain `sk-LIVETEST123456`.
- `list_agents` default hides only `closed` records; a freshly-completed
  child is `completed`, so it shows without `include_closed`. (Closed
  retention is a separate card: `subagent-close-retains.md`.)
- Use `blocking=true` on the spawn so the child has finished (status
  `completed`, result retained) by the time list/peek run — a still-
  running child would have `reason:null` and no output to redact yet.
- The model surfaces the shell tool as `[tool] shell`; the prompt says
  "the shell tool" to stay tool-name-agnostic.
