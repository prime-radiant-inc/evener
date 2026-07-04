# ask-subagent-invisible: a delegate cannot see, call, or be granted ask_user

**What this covers**: spec §8 row `ask-subagent-invisible.md` — §7's root-only gate: a
delegate session (`isSubagentSession()` true) never registers `ask_user` regardless of
`agent_type` or any grant, because the registration seam gates on interactive-root-ness, not
on the `rootOnlySubagentTools()` allowance list that legitimately re-admits other
delegation-only tools for a coordinator.

## Pre-state

- Hub + credentials as `ask-web-answer.md` (reuse if still running on `127.0.0.1:9280`).
- `openai/gpt-5.5` usable.

## Steps

1. Spawn a **root** session and have it delegate a task whose whole job is to try to call
   `ask_user` and report what happened. `max_wait_ms` is set so the result comes back inline
   rather than requiring a notification-poll loop:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-ask-subagent-XXXXX)
   prompt="Call the delegate tool exactly once with max_wait_ms 45000 and this task: \"Immediately try calling a tool named ask_user, with header Confirm, question Should we proceed, and two options Yes and No. If that tool is unavailable, unknown, or absent from your tool list, call communicate with the exact text: ASK_USER_UNAVAILABLE. If it somehow succeeds, call communicate with the exact text: ASK_USER_SUCCEEDED.\" Then tell me the delegate_id, the job_id, and the final message from the delegate, and end your turn."
   body=$(jq -n --arg wd "$tmpdir" --arg prompt "$prompt" '{
     prompt: $prompt,
     model: "openai/gpt-5.5", working_dir: $wd, harness: "serf", branch: "", access_mode: "full", agent: "default", launch_overrides: {}
   }')
   SID=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$body" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 90); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$st" = "idle" ] && break
     sleep 1
   done
   echo "SID=$SID state=$st"
   ```
2. Find the delegate's own session ID from the parent's job tree:
   ```bash
   go run ./cmd/serf-doctor tree "$SID" --json | jq -r '.children[0].session_id'
   DELEGATE_SID=$(go run ./cmd/serf-doctor tree "$SID" --json | jq -r '.children[0].session_id')
   echo "DELEGATE_SID=$DELEGATE_SID"
   ```
3. Confirm the delegate's **own** transcript never advertised `ask_user` to the provider,
   and never structurally invoked it:
   ```bash
   go run ./cmd/serf-doctor transcript "$DELEGATE_SID" --count ask_user
   ```
4. Read the delegate's final message via the parent's own transcript (the parent already
   captured it through the blocking `delegate` call):
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:6
   ```
5. **`grant_tools` rejection — deterministic-only, not live-reachable today.** Do **not**
   attempt to drive this through a live `delegate` call: the shipped `delegate` tool schema
   (`agent/internal/tool/definitions.go` `DefDelegate`) sets `"additionalProperties": false`
   and its property list is exactly `task, agent_type, model, reasoning_effort, max_wait_ms,
   delegation_allowance, watch_parent, isolation, result_schema` — there is no `grant_tools`
   property, and `delegateTool`'s Go handler (`agent/session_tools_jobs.go`) never reads one
   from `args`. The one live caller of the `grantTools []string` parameter,
   `createDelegate` (`agent/job_delegate.go:227`), hard-codes `nil` for it on every call. The
   protected-grant rejection (`ask_user is root-only and cannot be granted to subagents`,
   `agent/subagents.go:498`) is real and load-bearing, but today it is exercised only by
   calling `Session.spawnAgent`/`prepareSubagentRun` directly at the Go level — which is
   exactly what `agent/subagents_test.go`'s `TestGrantToolsCannotRegrantAskUser` and
   `TestGrantToolsAskUserAliasNeverSilentlyGranted` do. Run those as the check for this part
   instead of inventing a live step that would silently no-op:
   ```bash
   go test ./agent/ -run 'TestGrantToolsCannotRegrantAskUser|TestGrantToolsAskUserAliasNeverSilentlyGranted' -v
   ```

## Expected

- Step 3: `ask_user: 0 calls` with no mention suffix — the delegate's own tool list never
  contained `ask_user`, no matter what its task instructed it to attempt (mirrors the
  invariant used in `ask-noninteractive-invisible.md`).
- Step 4: the parent's transcript shows the delegate's reported message is
  `ASK_USER_UNAVAILABLE` (the expected, overwhelmingly likely outcome — the delegate can't
  even construct a call the provider will accept for an undeclared tool). If it is instead
  `ASK_USER_SUCCEEDED`, that is an active regression: stop and file a kata immediately
  rather than continuing.
- Step 5: both Go tests pass, and `TestGrantToolsCannotRegrantAskUser` in particular asserts
  the exact string `ask_user is root-only and cannot be granted to subagents`.
- Falsification: if a delegate can call or see `ask_user`, root-only gating is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir" /tmp/serf-ask /tmp/serf-hub-ask
```

## Sharp edges

- **This card documents a real gap between the live tool surface and a spec-listed
  guarantee** (§8: "grant guard: `grant_tools: [\"ask_user\"]` on a delegate is rejected").
  That guarantee holds at the Go API level (verified by the tests in step 5) but has no live
  caller today — the `delegate` tool simply does not expose a way to request extra tool
  grants yet. This is the documented "over-specification trap" pattern from
  `docs/agentic-testing.md`: when production wiring doesn't reach a designed behavior,
  confirm the gate in source, point at the unit test that verifies it, and say so in the
  scenario rather than fabricating a step that looks executable but would actually exercise
  nothing (a `grant_tools` key on a live `delegate` call is silently discarded, not
  rejected — asserting a rejection error on that path would be testing a fiction). If a
  future task wires `grant_tools` into the live `delegate` schema, this step should be
  upgraded to drive it for real and the deterministic-only note removed.
- `go run ./cmd/serf-doctor tree` lists delegate children by default (`edge: "delegate"`);
  `--observers` is only needed for observer sidecars, not plain delegates, so it's omitted
  above.
- The delegate's task text is built as a plain bash variable, then threaded into the JSON
  body via `jq --arg prompt "$prompt"` rather than interpolated straight into the jq
  program text — `--arg` hands the string to jq out-of-band, so it needs no JSON-escaping
  by hand and tolerates apostrophes/quotes if you edit the wording. Avoid switching back to
  inlining the prompt directly inside the single-quoted jq program (`'{ prompt: "...", }'`
  ) — bash single quotes can't contain an embedded `'`, which the nested Go/JSON
  double-quote escaping in this task text made easy to get wrong.
