# subagent-notification-wake: a child completing wakes an idle parent with a proactive `<subagent-notification>`

**What this covers**: the proactive subagent-completion wake — the
spawn-and-be-notified pattern. When a parent spawns a child NON-blocking
(`spawn_agent` `blocking:false`) and then ends its turn (goes idle WITHOUT
waiting), and the child later reaches a terminal run state, serf
PROACTIVELY wakes the parent: it drains the durable notification queue and
delivers a `<subagent-notification agent_id="..." status="completed"
reason="completed" turns_used="N" transcript_ref="local:...">` block as a
`STEERING` turn that DRIVES A REAL MODEL TURN. The woken parent's model
sees the reminder and can read the child's result with `wait` /
`subagent_output`.

The wake is wired only in **serve mode**: the hub's per-session bridge sets
`SetNotifyFunc(func() { srv.SubmitNotification() })` (`cmd/serf/serve.go`),
which feeds an `EntryNotification` kick into the serve loop; the loop's
`acceptNotificationInput` (`agent/session_lifecycle.go`) drains the queue
and appends the reminder as `schema.TurnSteering`
(`formatNotificationReminder`). One-shot `serf run` does NOT deliver the
wake — there is no later turn to wake, and no `notifyFunc` is wired — so
this scenario MUST be driven through the hub, not the one-shot CLI.

## Pre-state

- Built binaries: `go build -o /tmp/serf-hub ./cmd/serf-hub` and
  `go build -o /tmp/serf ./cmd/serf` (the hub spawns `/tmp/serf` per
  session).
- Creds exported into the hub/daemon env (zsh — `"$PWD/.env"`, a bare
  `. .env` fails):
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model. `openai/gpt-5.4-mini` (the `openai` instance, NOT
  `oai-work`) is enough; the recipe uses it. The `openai` instance is
  OAuth-backed, so its `auth/openai.json` must be present and signed in
  (`/tmp/serf openai status` → `state=signed-in`).
- Hub running on a free port; the daemon it spawns has the DEFAULT
  subagent depth of 1, which already allows one child — no
  `--max-subagent-depth` flag is needed (the hub does not pass one, and
  `MaxSubagentDepth <= 0` defaults to 1 in `agent/session_config.go`).
- Only ONE serf-hub may run per host (a `~/.serf/hub.lock` flock enforces
  it). If another hub already holds the lock, run this hub under a private
  `HOME` (see Sharp edges) so it gets its own lock and state root.

## Steps

1. Start the hub and confirm it is up (401 = answered):
   ```bash
   /tmp/serf-hub -addr 127.0.0.1:9188 -serf /tmp/serf &
   sleep 2
   curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:9188/   # → 401
   TOKEN=$(cat ~/.serf/auth-token); HUB=http://127.0.0.1:9188
   ```

2. Hermetic working dir for the parent session:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-notify-XXXXX)
   ```

3. **Spawn the parent with a prompt that forces spawn-non-blocking-then-
   idle.** The hard part of this scenario is getting the root to spawn the
   child and then STOP its turn WITHOUT waiting — so the later child
   completion is what wakes it. Forbid `wait` and `list_agents` explicitly
   and tell it to stop:
   ```bash
   /api/spawn  (POST, Bearer $TOKEN) with body:
     prompt: "Use spawn_agent with blocking=false to delegate this task to a subagent: 'Run `sleep 15` via exec_command, then communicate the exact text CHILD_DONE_42.' After you receive the agent_id, do NOT call wait and do NOT call list_agents. Simply tell me you've started the child and that you'll report its result when notified, then STOP and end your turn."
     model: "openai/gpt-5.4-mini"
     working_dir: "$tmpdir"
     harness: "serf"
     access_mode: "full"
     agent: "default"
     launch_overrides: {}
   ```
   Capture `session_id` (the parent SID; appwire ref `local:$SID`).

4. **Poll the parent's state through the idle→woken transition**
   (`/api/sessions/local:$SID`, read `.state`). The parent walks: `active`
   (first turn: spawns the child, then ends the turn) → `idle` (sitting,
   not waiting) → ~15s later the child finishes → `active` again (the
   notification wakes a fresh turn) → `idle` (after it reads/reports). Log
   on every state change so the idle period is visible.

5. **Read the PARENT transcript off disk** — the authoritative proof:
   ```bash
   SID=<parent session_id>
   TS=$(find <hub state root>/projects -name "$SID.transcript.jsonl")
   grep '"kind":"STEERING"' "$TS"
   ```
   (When the hub runs under a private `HOME`, its state root is
   `$HOME/.local/state/serf`; otherwise `~/.local/state/serf`. `find`
   resolves it either way.)

## Expected

- After step 3, the parent's first turn: a `spawn_agent` tool call with
  `blocking:false` returning `{"agent_id":"<child>","status":"running"}`,
  followed by a brief `communicate` ("started the child, will report when
  notified") — and then the turn ENDS. The parent does NOT call `wait` or
  `list_agents`. The session reaches `idle`.
- During the idle period (≈15s, while the child runs `sleep 15`), the
  parent makes NO model requests — it is genuinely parked.
- When the child finishes, the parent transcript gains a `STEERING` turn
  whose text is the notification block. THIS IS THE WAKE — the
  load-bearing proof. The block looks exactly like (observed live):
  ```
  <subagent-notification agent_id="01KTKBTTHMG117PR5N0KZZ7XWP" status="completed" reason="completed" turns_used="1" transcript_ref="local:01KTKBTTHMG117PR5N0KZZ7XWP">
  Subagent 01KTKBTTHMG117PR5N0KZZ7XWP finished (completed). Read its result with wait("01KTKBTTHMG117PR5N0KZZ7XWP") or subagent_output("01KTKBTTHMG117PR5N0KZZ7XWP", view=result).
  </subagent-notification>
  ```
  It is appended as `schema.TurnSteering` (`"kind":"STEERING"`), a
  user-role message that `expandHistory` passes through to the model
  WITHOUT rendering a user bubble — so the reminder reaches the model on
  this fresh turn. The `STEERING` entry's timestamp is strictly AFTER the
  parent's first-turn-ending `communicate`, with an idle gap (≈15–16s
  observed) in between. The wake is proven by the existence of that
  post-idle `STEERING` entry carrying the `<subagent-notification ...
  status="completed" ...>` block.
- BONUS (the model acting on the wake, observed live): the woken turn then
  calls `subagent_output(<child>, view=result)`, which returns
  `"output":"CHILD_DONE_42","success":true`, and the parent `communicate`s
  the result back ("Child agent ... completed with output: CHILD_DONE_42").
  So `CHILD_DONE_42` surfaces in the parent transcript, read on the woken
  turn — not on the original spawn turn.
- The session returns to `active` when the notification wakes it, then back
  to `idle` after it reports. Polling `.state` shows the
  `active → idle → active → idle` walk.
- The child transcript confirms the child actually did the work:
  `exec_command` `sleep 15` (`exit_code:0`, `duration_ms≈15000`) then
  `communicate("CHILD_DONE_42")`.
- Falsification:
  - **After the child finishes and the parent was idle, NO
    `<subagent-notification>` `STEERING` entry appears in the parent
    transcript** → the wake did not fire. This is the core regression this
    card guards: a terminal child silently failing to wake an idle parent
    (a broken `notifyFunc` → `SubmitNotification` → `acceptNotificationInput`
    chain, or notifications filtered out at drain). The parent would sit
    `idle` forever, never learning the child finished.
  - The notification block is appended as `USER_INPUT` (a user bubble)
    instead of `STEERING` → it was framed as a user turn, polluting history
    and miscounting `s.turns` (a notification is not a user turn).
  - The wake fires but BEFORE the parent's first turn ends (no idle gap) →
    the parent was still mid-turn; this scenario specifically proves the
    PROACTIVE wake of an ALREADY-IDLE parent. Lengthen the child sleep so
    the parent reliably idles first (see Sharp edges).

## Cleanup

- Shut down the parent (and, defensively, the child) session you spawned:
  ```bash
  curl -s -X POST -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
  ```
- Kill ONLY the hub you started (`pkill -f serf-hub` is too broad if
  another hub is running — match your port / private HOME, or kill the PID
  you captured).
- Leave `$tmpdir` and all transcripts on disk (do NOT `rm -rf`; it is
  blocked in this environment). A fresh `mktemp -d` per run keeps reruns
  hermetic.

## Sharp edges

- **Serve-mode only.** The wake is wired exclusively in `serf serve` (the
  hub path). `serf run` (one-shot) does NOT deliver it — no later turn
  exists to wake, and no `notifyFunc` is set. Do not try to reproduce this
  through the one-shot CLI; it will look like a "missing wake" but is
  simply out of scope for that mode.
- **Getting the parent to idle BEFORE the child finishes is the whole
  game.** The proof is a notification arriving AFTER the parent parked. The
  prompt must forbid `wait` and `list_agents` and order an explicit STOP,
  or the model will block-disguised-as-async (`blocking=false` then
  immediately `wait`) and never idle. With `openai/gpt-5.4-mini` the
  recipe prompt worked on the FIRST attempt: the parent spawned, sent one
  `communicate`, and ended its turn cleanly. If a model still races (waits
  instead of stopping), lengthen the child `sleep` (e.g. 30s) so the parent
  reliably idles first, or switch to `openai/gpt-5.5` (steadier
  sequencing).
- **Default subagent depth is 1 — no flag needed.** The hub does not pass
  `--max-subagent-depth`, and the daemon defaults `MaxSubagentDepth <= 0`
  to 1, which permits exactly one child. Do not add a flag expecting it to
  matter for the spawn payload; depth is a daemon-launch concern, not a
  per-spawn field.
- **One hub per host (flock).** `serf-hub` takes an exclusive flock on
  `~/.serf/hub.lock`; a second hub exits with `flock: resource temporarily
  unavailable`. If another hub already holds it and you must not disturb
  it, start your hub under a private `HOME` so it computes its own lock and
  state root:
  ```bash
  HHOME=/tmp/serf-hub-home
  mkdir -p "$HHOME/.serf" "$HHOME/.local/state/serf/auth"
  cp ~/.serf/providers.toml ~/.serf/credentials.toml ~/.serf/auth-token "$HHOME/.serf/"
  cp ~/.local/state/serf/auth/openai.json "$HHOME/.local/state/serf/auth/openai.json"  # OAuth state for the openai instance
  env HOME="$HHOME" XDG_STATE_HOME= /tmp/serf-hub -addr 127.0.0.1:9188 -serf /tmp/serf &
  ```
  Reusing the real `auth-token` keeps `TOKEN=$(cat ~/.serf/auth-token)`
  valid. The OAuth `openai.json` MUST be copied or the `openai` instance
  spawn fails credential validation. Transcripts then land under
  `$HHOME/.local/state/serf/projects/...`, not the default location.
- **Transcript content schema for extraction.** Each content part keys its
  type under `kind` (not `type`): `kind:"text"` for the notification block,
  `kind:"tool_call"` (with a nested `tool_call.{name,arguments}`) for the
  parent's `spawn_agent` / `subagent_output` / `communicate` calls, and
  `kind:"tool_result"` (nested `tool_result.{name,content}`) for results. A
  naive extractor keyed on `type` reads every part as empty — grep the raw
  JSONL for `<subagent-notification` and `CHILD_DONE_42` if in doubt.
- **The notification can interleave with an in-flight turn too.** This card
  exercises the idle-parent case (the headline). The same
  `acceptNotificationInput` path also drains the queue at the start of an
  input turn when the parent is busy; that interleaving is covered by the
  unit tests in `agent/notification_test.go`, not here.
