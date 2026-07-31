# ask-noninteractive-invisible: ask_user is absent (not merely disabled) outside interactive root sessions

**What this covers**: spec §8 row `ask-noninteractive-invisible.md` — §7 point 1's
registration-seam gate: `registerAskTool` never runs when `cfg.NonInteractive` is set, so the
tool is unregistered (and therefore unadvertised to the provider) rather than present-but-
blocked. Exercises both named surfaces: a hub-spawned `non_interactive:true` session (the
same `SessionConfig.NonInteractive` flag `serve --non-interactive` sets — confirmed by
`cmd/serf-hub/web_spawn.go:89` threading the spawn request's `non_interactive` straight into
the daemon's launch overrides) and the one-shot `serf <prompt>` CLI, which hardcodes
`NonInteractive: true` unconditionally (`cmd/serf/main.go`/`run.go` — no flag needed or
available to turn it off).

## Pre-state

- Hub + credentials as `ask-web-answer.md` (reuse if still running on `127.0.0.1:9280`); the
  one-shot half of this card also needs the plain `serf` binary and does not touch the hub.
- `openai/gpt-5.5` usable.

## Steps

1. **Hub-spawned non-interactive session.** Spawn with `"non_interactive": true` and a
   prompt that explicitly tries to invoke `ask_user` by name:
   ```bash
   tmpdir1=$(mktemp -d -t serf-e2e-ask-ni-hub-XXXXX)
   body=$(jq -n --arg wd "$tmpdir1" '{
     prompt: "Call the tool named ask_user right now, asking header \"Confirm\" question \"Should we proceed?\" with options Yes and No. If no such tool exists in your tool list, say so plainly and proceed on your own best judgment instead.",
     model: "openai/gpt-5.5", working_dir: $wd, harness: "serf", branch: "", access_mode: "full", agent: "default",
     non_interactive: true, launch_overrides: {}
   }')
   SID1=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$body" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID1" | jq -r '.state // ""')
     [ "$st" = "awaiting" ] && break
     sleep 1
   done
   echo "SID1=$SID1 state=$st"
   ```
2. **One-shot CLI.** No hub involved; a fresh, uniquely-named workdir doubles as the lookup
   key afterward, since a plain one-shot run never prints its session ID. Match on the
   workdir's unique basename rather than its full path — on macOS a mktemp path's parent
   (`/tmp` or `$TMPDIR`) is itself a symlink into `/private/...`, and whether a given code
   path records the raw or the resolved form of `working_dir` is inconsistent (confirmed
   live: the one-shot daemon records the raw, unresolved path; a hub-spawned daemon records
   the resolved `/private/...` form), so anchoring on the full path is fragile in either
   direction while the mktemp-generated basename is unaffected by symlink resolution either
   way:
   ```bash
   tmpdir2=$(mktemp -d -t serf-e2e-ask-ni-oneshot-XXXXX)
   /tmp/serf-ask --model openai/gpt-5.5 --dir "$tmpdir2" \
     "Call the tool named ask_user right now, asking header \"Confirm\" question \"Should we proceed?\" with options Yes and No. If no such tool exists in your tool list, say so plainly and proceed on your own best judgment instead."
   TFILE2=$(grep -l "\"working_dir\":\"[^\"]*$(basename "$tmpdir2")\"" ~/.local/state/serf/projects/*/sessions/*.transcript.jsonl)
   SID2=$(basename "$TFILE2" .transcript.jsonl)
   echo "SID2=$SID2 (via $TFILE2)"
   ```
3. For **both** sessions, check structural calls, then inspect the exact provider request
   explicitly. Run an audit session in the same project directory and instruct it to call
   `read_session_transcript` on the target SID with `source=api_log`, select the target's
   first `api_attempt`, and make a second call with that `attempt_id` plus `body=request`.
   It must parse the expanded JSON request structurally: list the names in `tools[]`, report
   whether `ask_user` is present, and confirm the non-interactive prompt section is present.
   ```bash
   go run ./cmd/serf-doctor transcript "$SID1" --count ask_user
   go run ./cmd/serf-doctor transcript "$SID2" --count ask_user
   /tmp/serf-ask --model openai/gpt-5.5 --dir "$tmpdir1" \
     "Audit session $SID1. Use an explicit API-log summary and request-body expansion as described in this scenario; report the structured tools array and non-interactive prompt evidence."
   /tmp/serf-ask --model openai/gpt-5.5 --dir "$tmpdir2" \
     "Audit session $SID2. Use an explicit API-log summary and request-body expansion as described in this scenario; report the structured tools array and non-interactive prompt evidence."
   ```

## Expected

- Step 3: `serf-doctor ... --count ask_user` prints `ask_user: 0 calls` for **both**
  sessions. An assistant-text mention is reported separately and is not an invocation.
- Each explicit request-body expansion has no tool definition whose structured name is
  `ask_user`, and does contain the non-interactive prompt section
  (`agent/prompts/sections/non-interactive.md.tmpl`). The summary itself contains no body
  data and states `credential_values_excluded: true`.
- Falsification: if `ask_user` appears in the tool list of a `--non-interactive` or one-shot
  session, gating is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID1/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir1" "$tmpdir2" /tmp/serf-ask /tmp/serf-hub-ask
```

## Sharp edges

- **Model behavior when asked to call a nonexistent tool is not fully deterministic.** Most
  providers will not let the model structurally invoke a tool absent from the request's
  tool list at all, so the far more likely outcome is prose ("I don't have an ask_user
  tool..."). If the model *does* somehow manage to emit a structural `ask_user` tool call
  (`serf-doctor`'s `calls` field nonzero), the daemon's exec-time guard
  (`agent/session_tools_ask.go`) should still have produced an error result containing
  `unknown tool: ask_user` for it — check the outline
  (`go run ./cmd/serf-doctor transcript "$SIDn" --format outline --range last:4`) if `calls`
  is unexpectedly nonzero. Either outcome is consistent with gating working; the
  deterministic, always-applicable check is the `--count` invariant above, not the model's
  prose.
- **One-shot mode never prints its session ID** on a fresh (non-`--resume`) run — the
  working-dir grep in step 2 is how an agent (or a human) locates the transcript afterward;
  don't assume `stdout` carries an ID to capture.
- `--count` inspects the whole semantic transcript, including turns; if you reuse a workdir/session
  across multiple prompts in one debugging pass, old mentions accumulate — use a fresh
  `mktemp -d` per attempt, as done above.
