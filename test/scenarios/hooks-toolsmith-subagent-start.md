# hooks-toolsmith-subagent-start: the real toolsmith plugin's `SubagentStart` hook runs in a live delegate

**What this covers**: the reported bug and the branch `claude-hooks-compat`
that fixes it. The `toolsmith` plugin on disk declares a `SubagentStart`
hook; before this branch every session that loaded it printed

```
plugin "toolsmith" declares a hook for the reserved event "SubagentStart", which evener does not yet fire; this hook will not run
```

and the hook never ran. Five lifecycle events moved from "recognized but
unsupported" (reserved) to fired in `d454bcfeaf` (`feat(hooks): register
five lifecycle events and widen the matcher exact set`), and evener gained
a `SubagentStart` dispatch site in `8a2c8128c9` (`feat(hooks): fire
SubagentStart for every delegate run launch`). This card is the LIVE proof
that the real plugin on disk, loaded through `--plugin-dir`, reaches a real
session: the hook runs, the old warning is gone, and the hook's Claude-shaped
`additionalContext` is in the child delegate's first model request.

The deterministic Go tests (`agent/subagent_start_hook_test.go`,
`agent/internal/hooks/*_test.go`, `agent/plugin/hooks_test.go`) pin the unit
behavior. This card proves the on-disk plugin path — manifest discovery,
hooks.json parsing, `${CLAUDE_PLUGIN_ROOT}` expansion, the shell-form
command, stdin, and the `hookSpecificOutput` return — end to end.

### Loading mechanism (how a hooks.json reaches a live session)

`evener` (the CLI) takes a repeatable `--plugin-dir <dir>` flag
(`cmd/evener/main.go:367`). Each `<dir>` is a plugin ROOT: it must contain
`.claude-plugin/plugin.json` (or `.codex-plugin/plugin.json`), and hooks are
read from `<dir>/hooks/hooks.json` by default (`agent/plugin/plugin.go`
`Load` → `agent/plugin/hooks.go` `discoverPluginHooksDiag`). The hooks.json
uses the Claude wrapper shape
`{"hooks": {"SubagentStart": [ {"matcher": ..., "hooks": [...]} ]}}`.

The real toolsmith plugin declares two hooks in its `hooks/hooks.json`:

- `SessionStart`, matcher `startup|clear|compact`
- `SubagentStart`, matcher `.*`

Both run `"${CLAUDE_PLUGIN_ROOT}/hooks/run-hook.cmd" inject <Event>` with
`"shell": "bash"`. `${CLAUDE_PLUGIN_ROOT}` expands to the plugin root
(`agent/plugin/hooks.go:324`), and a command with no `args` is shell-form
(`agent/internal/hooks/command_runtime.go:209`). `hooks/run-hook.cmd` execs
`hooks/inject` with the event name; `inject` reads the hook's JSON input
from stdin, skips the toolsmith agent itself, and prints

```json
{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"Toolsmith: don't write your own helper tools.\n…"}}
```

on stdout with exit 0. evener parses `additionalContext` as model context
(`agent/internal/hooks/hooks.go:437`), and the `SubagentStart` dispatch site
delivers it to the CHILD as a `hook-context` steering turn
(`agent/subagents.go:1783-1803` `runSubagentStartHook` → `deliverHookContext`,
`agent/session_queue.go:383`), wrapped in `<SYSTEM-REMINDER>…</SYSTEM-REMINDER>`.
So the child's first conversation request carries the text. The plugin's
`SessionStart` hook prints the same text, so with the unmodified plugin you
cannot tell which event delivered it; step 6 is the control that isolates
`SubagentStart`.

### Which session is which

The delegate runs as its own child session in the same `--state-dir`:

- `<stateDir>/sessions/<parentID>.meta.json` — `is_subagent` absent/false
- `<stateDir>/sessions/<childID>.meta.json` — `"is_subagent":true` and
  `"parent_session_id":"<parentID>"`

The parent's transcript records the hook completion
(`"hook":{"event":"SubagentStart","plugin_name":"toolsmith","exit_code":0}`,
turn kind `HOOK_COMPLETED`); the child's transcript has the delivered
`hook-context` steering turn.

## Pre-state

- `evener` built **from the checkout under test**. Build from the worktree
  you are testing; a hardcoded path to the main checkout silently proves the
  wrong branch. The binary and all per-run state land under one `mktemp -d`
  so two concurrent runs cannot overwrite each other's markers or session
  logs:
  ```bash
  cd "$(git rev-parse --show-toplevel)"   # must be the branch under test
  git log --oneline -1                     # expect 70cf034e92 or a descendant
  WORK=$(mktemp -d /tmp/evener-subagent-start.XXXXXX)
  go build -o "$WORK/evener" ./cmd/evener
  ```
- The real toolsmith plugin installed at
  `~/.config/evener/plugins/cache/toolsmith/toolsmith/398cd1c718ff33c3148e2640c950c74a4bb3b4ba/`.
  Confirm its manifest and hook declaration before relying on it:
  ```bash
  TS=~/.config/evener/plugins/cache/toolsmith/toolsmith/398cd1c718ff33c3148e2640c950c74a4bb3b4ba
  test -f "$TS/.claude-plugin/plugin.json" && echo manifest-ok
  jq -c '.hooks | keys' "$TS/hooks/hooks.json"   # expect ["SessionStart","SubagentStart"]
  ```
  A newer toolsmith commit under `.../toolsmith/toolsmith/<other-sha>/` is
  the same test as long as `SubagentStart` is declared; pin whichever one
  you verify against and update the path here.
- A credentialed model. This card was recorded on
  `lunaroute/deepseek-4.1-flash` (instance `lunaroute`, `openai-chat`
  protocol, key in `~/.config/evener/credentials.toml`). Any model that can
  call the `delegate` tool works; substitute `--model` if that one is
  unavailable. `OPENAI_API_KEY` on this machine is a placeholder (`1234`) and
  answers 401.
- `jq` on PATH (used to read the api log). `/usr/bin/jq` is present on this
  Mac.
- `--api-log on` writes provider request bodies to
  `<stateDir>/sessions/<id>.api.jsonl`; this card needs it for the final
  assertion. Bodies can contain private source — they stay under `$WORK`.

## Steps

1. Make a hermetic project that is a **git repo**. The `delegate` tool's
   default `isolation:"worktree"` refuses a non-repo ("not in a git
   repository") and the model then loops retrying; a real repo makes the
   spawn succeed:
   ```bash
   PROJ="$WORK/proj"; STATE="$WORK/state"
   mkdir -p "$PROJ" "$STATE"
   git -C "$PROJ" init -q .
   git -C "$PROJ" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
   ```

2. Run evener live with the real plugin dir and only toolsmith enabled, and
   force one delegate. `--verbose` emits NDJSON lifecycle events to stderr;
   `--api-log on` records request bodies:
   ```bash
   TS=~/.config/evener/plugins/cache/toolsmith/toolsmith/398cd1c718ff33c3148e2640c950c74a4bb3b4ba
   "$WORK/evener" --model lunaroute/deepseek-4.1-flash \
     --dir "$PROJ" --state-dir "$STATE" \
     --plugin-dir "$TS" --enabled-plugins toolsmith \
     --api-log on --verbose \
     "Call the delegate tool exactly once with intent=\"Say exactly the word pong and nothing else, then finish.\" and delegation_allowance=0. When it returns, report exactly what the delegate said. Do not call any other tool." \
     > "$WORK/run.log" 2>&1
   echo "exit=$?"     # expect 0
   ```
   `--enabled-plugins toolsmith` keeps every other installed plugin (each
   with its own SessionStart hook) out of the child's prompt, so the
   `Toolsmith:` text in step 5 can only have come from this plugin.

3. Assert the hook RAN and the old warning is ABSENT, on the parent's stream:
   ```bash
   # the SubagentStart hook fired for the real plugin: one HOOK_START + one HOOK_END
   grep -c '"event":"SubagentStart"' "$WORK/run.log"          # MUST be 2
   grep '"event":"SubagentStart"' "$WORK/run.log" | head -2   # matcher ".*", plugin_name "toolsmith"

   # the reported bug's warning is gone
   grep -c 'declares a hook for the reserved event' "$WORK/run.log"     # MUST be 0
   ```

4. Assert the warning is absent from persisted state too, and find the child
   session. Every session in `$STATE` writes `sessions/<id>.log.jsonl`
   (plugin-loaded and SessionStart advisories), so the log is the second
   place the warning could hide:
   ```bash
   grep -rc 'declares a hook for the reserved event' "$STATE" || true   # MUST be 0 everywhere

   CHILD_META=$(grep -l '"is_subagent":true' "$STATE"/sessions/*.meta.json | head -1)
   CHILD="${CHILD_META%.meta.json}"
   echo "child=$CHILD"
   jq -r '.parent_session_id' "$CHILD.meta.json"   # names the parent session
   ```

5. Assert the hook's `additionalContext` reached the CHILD's first model
   request. The first POST in the child's api log that carries a tool list is
   the child's first conversation request (earlier POSTs are the session
   namer; the `GET /models` has an empty body):
   ```bash
   # every POST in the child's log, with bytes and whether it carries the hook text
   jq -r 'select(.kind=="api_attempt" and .request.method=="POST")
          | [(.request.body.byte_count|tostring),
             ((.request.body.data // "") | contains("don\u0027t write your own helper tools") | tostring)]
          | @tsv' "$CHILD.api.jsonl"
   ```
   The first body-bearing POST that also has `"tools"` in it MUST be `true`.
   A one-line machine check of the same thing (first POST whose body exceeds
   10 kB is the conversation request, not the namer):
   ```bash
   jq -r 'select(.kind=="api_attempt" and .request.method=="POST"
                 and (.request.body.byte_count // 0) > 10000)
          | ((.request.body.data // "") | contains("don\u0027t write your own helper tools"))' \
     "$CHILD.api.jsonl" | head -1     # MUST print true
   ```
   And the child's transcript records where the context came from — two
   `hook-context` steering turns (one SessionStart, one SubagentStart), both
   `machinery_flagged`:
   ```bash
   jq -r 'select(.kind=="entry" and .turn.kind=="STEERING")
          | [.turn.steering_kind, (.turn.timestamp|tostring)] | @tsv' \
     "$CHILD.transcript.jsonl"
   ```
   The parent's transcript holds the `SubagentStart` completion record:
   ```bash
   PARENT=$(jq -r '.parent_session_id' "$CHILD.meta.json")
   jq -r 'select(.kind=="entry" and (.turn.hook.event // "") == "SubagentStart")
          | [.turn.hook.event, .turn.hook.plugin_name, .turn.hook.matcher,
             (.turn.hook.exit_code|tostring)] | @tsv' \
     "$STATE/sessions/$PARENT.transcript.jsonl"
   # MUST print: SubagentStart  toolsmith  .*  0
   ```

6. (Isolation control) Re-run with a copy of the plugin whose
   `hooks.json` declares ONLY `SubagentStart`. The context must still reach
   the child, proving it came from `SubagentStart`, not from the sibling
   `SessionStart` hook (both `inject` invocations print the same text):
   ```bash
   CTL="$WORK/toolsmith-subagent-only"
   mkdir -p "$CTL"
   cp -R "$TS/.claude-plugin" "$TS/hooks" "$TS/bin" "$CTL/"
   jq '{hooks: {SubagentStart: .hooks.SubagentStart}}' "$TS/hooks/hooks.json" \
     > "$CTL/hooks/hooks.json"

   PROJ2="$WORK/proj2"; STATE2="$WORK/state2"
   mkdir -p "$PROJ2" "$STATE2"
   git -C "$PROJ2" init -q .
   git -C "$PROJ2" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
   "$WORK/evener" --model lunaroute/deepseek-4.1-flash \
     --dir "$PROJ2" --state-dir "$STATE2" \
     --plugin-dir "$CTL" --enabled-plugins toolsmith \
     --api-log on --verbose \
     "Call the delegate tool exactly once with intent=\"Say exactly the word pong and nothing else, then finish.\" and delegation_allowance=0. When it returns, report exactly what the delegate said. Do not call any other tool." \
     > "$WORK/run-control.log" 2>&1
   grep -c '"event":"SubagentStart"' "$WORK/run-control.log"                 # MUST be 2
   grep -c '"event":"SessionStart"' "$WORK/run-control.log"                  # MUST be 0
   CHILD2=$(grep -l '"is_subagent":true' "$STATE2"/sessions/*.meta.json | head -1); CHILD2="${CHILD2%.meta.json}"
   jq -r 'select(.kind=="api_attempt" and .request.method=="POST"
                 and (.request.body.byte_count // 0) > 10000)
          | ((.request.body.data // "") | contains("don\u0027t write your own helper tools"))' \
     "$CHILD2.api.jsonl" | head -1                                          # MUST print true
   # exactly ONE hook-context turn: SubagentStart delivered it, and there is no
   # SessionStart hook in this copy to deliver a second
   jq -r 'select(.kind=="entry" and .turn.kind=="STEERING"
                 and .turn.steering_kind=="hook-context") | .turn.message.content[0].text
          | length' "$CHILD2.transcript.jsonl" | wc -l                       # MUST be 1
   ```

## Expected

- After step 2: evener exits **0**. The delegate spawns (log carries
  `DELEGATE_UPDATED` with `child_session_id`) and answers `pong`.
- After step 3: exactly two `SubagentStart` lines (`HOOK_START` and
  `HOOK_END`), the start naming matcher `.*` and plugin `toolsmith`, the end
  `"exit_code":0`. Zero copies of the reserved-event warning. Observed shape:
  ```
  {"kind":"HOOK_START",…,"data":{"event":"SubagentStart","hook_type":"command","matcher":".*","plugin_name":"toolsmith"}}
  {"kind":"HOOK_END",…,"data":{"event":"SubagentStart","hook_type":"command","matcher":".*","plugin_name":"toolsmith","exit_code":0,"duration_ms":46}}
  ```
- After step 4: `CHILD.log.jsonl` holds three advisories — `plugin_loaded`
  (naming the toolsmith manifest), `session_start_hooks` (`kind=startup
  delivered=1`), and `session_namer`. No file under `$STATE` contains
  `declares a hook for the reserved event`.
- After step 5: the child's first conversation request (POST, body > 10 kB)
  carries `don't write your own helper tools`, and the child transcript has two
  `hook-context` steering turns. The parent transcript has one
  `SubagentStart … toolsmith .* 0` `HOOK_COMPLETED` entry.
- After step 6: the SubagentStart-only plugin still puts the text in the
  child's first request, the child carries exactly ONE `hook-context`
  steering turn (versus two in step 5), and no `SessionStart` hook fires at
  all — the text can only have come from `SubagentStart`. Recorded:
  `state-live7` (full plugin) child had 2 such turns; the control child had 1.
- Falsification (the regression is back, in priority order):
  - `grep -c 'declares a hook for the reserved event'` returns ≥1 → the event
    regressed to "recognized but unsupported": `HookSubagentStart` left
    `validHookEvents` (`agent/plugin/hooks.go`) or the warning moved events.
    This is the exact reported bug. At `origin/main` (`0345bd5c50`) the same
    command prints
    `plugin "toolsmith" declares a hook for the reserved event "SubagentStart", which evener does not yet fire; this hook will not run`.
  - `grep -c '"event":"SubagentStart"'` is 0 → the warning is gone but nothing
    fires: the `runSubagentStartHook` call site
    (`agent/subagents.go:1739` `launchSubagentRun`) is gone, or the matcher
    stopped matching `subagent`/`default` (the unnamed delegate reports
    `subagent`; matcher `.*` covers it).
  - `HOOK_START` present with a non-zero `exit_code` or a spawn error → the
    shell-form command or `${CLAUDE_PLUGIN_ROOT}` expansion broke; `inject`
    itself always exits 0, so a non-zero exit is evener's spawn failing.
  - The child's first POST does NOT contain `don't write your own helper
    tools` → the hook ran but its `hookSpecificOutput.additionalContext`
    was dropped before delivery: check `agent/internal/hooks/hooks.go:437`
    (parse) and `agent/subagents.go:1794-1803` (deliver).
  - No child session at all (`grep -l '"is_subagent":true'` finds nothing) →
    the delegate never spawned; the run was not in a git repo (see Sharp
    edges), so the assertion is untested rather than failed.

## Cleanup

```bash
# leave the tmpdir in place if rm -rf is blocked in your environment;
# everything is self-contained under /tmp/evener-subagent-start.*
echo "tmpdir retained: $WORK"
```

One isolated `git worktree` may be left behind by the delegate's
`isolation:"worktree"` lane; it lives under `$STATE/worktrees/` and is
removed when the run settles. Session state under `$STATE` is throwaway
because `--state-dir` points into `$WORK`.

## Sharp edges

- **The project must be a git repo.** `delegate`'s default
  `isolation:"worktree"` produces `delegate isolation:"worktree": not in a
  git repository` for a plain directory, and the model then retries the same
  call until it gives up — no child session, no hook, and the "hook did not
  fire" result is a harness miss, not a regression. This was hit while
  recording the card.
- **Say nothing about `agent_type`.** A plugin agent is matched by its
  catalog key (`toolsmith:toolsmith`). This card uses an unnamed delegate,
  whose `SubagentStart` matcher target is `subagent`
  (`subagentHookAgentType`, `agent/subagents.go:731`), which the plugin's
  `.*` matcher covers. Naming `agent_type` in the prompt invites a model to
  send `"default"`, which is accepted and resolves to `subagent`; leaving it
  out is simpler and gives the same matcher target.
- **`--enabled-plugins toolsmith` matters for a clean assertion.** Without
  it every installed plugin loads, and each SessionStart hook that injects
  model context lands in the child's prompt too. The `Toolsmith:` text is
  distinctive enough to survive that, but the child's prompt gets noisy and
  the run is slower and billed more.
- **Expect `duplicate plugin name "toolsmith"; keeping the first of: …`.**
  The plugin is discovered twice — once as an installed marketplace plugin
  and once through `--plugin-dir` — so evener warns and keeps the first. This
  is not the bug under test, and it appears in run and control alike. In step
  6 it also tells you which copy won: the control copy is listed first, and
  `grep -c '"event":"SessionStart"'` returning 0 confirms the copy that
  actually ran was the one without a SessionStart hook. If that count is 2,
  the installed copy won and the control proved nothing.
- **The API key on this machine.** `OPENAI_API_KEY` here is the placeholder
  `1234`; an `openai/*` model answers `401 Incorrect API key provided: 1234`.
  `~/.config/evener/credentials.toml` has a working `lunaroute` key, which is
  why the card names `lunaroute/deepseek-4.1-flash`. `codex-fsck.com/*` is
  OAuth-based and rate-limited (429 with a reset date) — usable if the window
  is open.
- **The namer request comes first.** The child's api log's first POST is the
  session namer ("Use this initial user prompt to name the session…"), not
  the turn. Filter on body size (`> 10000` bytes) or on the presence of a
  `tools` key, or you will assert against the namer and see `false`.
- **Two identical hook-context turns in the child.** Both toolsmith hook
  commands (`inject SessionStart` and `inject SubagentStart`) print the same
  `additionalContext`, so the child carries two identical
  `<SYSTEM-REMINDER>` steering turns. That is expected, and step 6 isolates
  `SubagentStart` as the one that delivers on a delegate launch. The dedup in
  `runSubagentStartHook` (`hookContextAlreadyPresent`,
  `agent/subagents.go:1845`) compares against the child's history at launch
  time; both deliveries here happen on the launch path itself, so both land.
- **`--verbose` is required for the `HOOK_START`/`HOOK_END` evidence.** The
  hook events are not emitted without it. The warning, by contrast, also
  prints on plain stderr as `warning: plugin … declares a hook for the
  reserved event …`, so a plain run still falsifies the headline bug.
- **Hook bodies are secrets-adjacent.** `--api-log on` records full request
  bodies (system prompt, environment, tool results) under `$STATE`. Keep them
  in `$WORK` and delete them if that matters.
