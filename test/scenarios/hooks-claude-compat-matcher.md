# hooks-claude-compat-matcher: Claude-style hooks.json drives a real serf tool call — exact matcher + exec-form

**What this covers**: the Phase-1 Claude-compatible hook behavior on
branch `lifecycle-hooks-claude-compat` (commits `a4685d3d` matcher,
`28bd828e` exec-form). Proves end-to-end, against a live model and a
real `shell` tool call, that:

1. **Exact matcher**: a `PreToolUse` hook with matcher `"Bash"` fires
   on serf's shell tool and does NOT substring-match a near name. The
   headline fix: previously `"Bash"` was compiled as a regex and
   substring-matched (so `"Bas"` would have matched `"Bash"`); now a
   matcher of only `[A-Za-z0-9_|]` is exact / pipe-list, not regex
   (`agent/internal/hooks/matcher.go`).
2. **Command exec-form**: a command hook with `"args":[...]` is spawned
   directly with no shell (`agent/internal/hooks/hooks.go`
   `executeCommandHook`: `len(hook.Args) > 0` → `exec.CommandContext`).
3. An exit-0 PreToolUse command hook does NOT block the tool call.

The deterministic Go tests (`agent/internal/hooks/*_test.go`) cover the
unit behavior; this card is the LIVE proof that a hooks.json on disk
reaches a real serf session and gates a real model-driven tool call.

### Loading mechanism (how a hooks.json reaches a live session)

`serf` (the CLI) takes a repeatable `--plugin-dir <dir>` flag
(`cmd/serf/main.go`, wired to `SessionConfig.PluginDirs`). Each `<dir>`
is a plugin ROOT: it must contain `.claude-plugin/plugin.json` (or
`.codex-plugin/plugin.json`), and hooks are read from
`<dir>/hooks/hooks.json` by default (`agent/plugin/plugin.go` `Load` →
`agent/plugin/hooks.go` `discoverPluginHooksDiag`, default path
`<pluginDir>/hooks/hooks.json`). The hooks.json uses the Claude wrapper
shape `{"hooks": {"PreToolUse": [ {"matcher": ..., "hooks": [...]} ]}}`.

### Tool name to match against — IMPORTANT

serf's shell tool is canonically named `shell`
(`agent/internal/tool/definitions.go` `DefShell`). Matchers, however,
are tested against the **Claude** tool name: at the PreToolUse site
(`agent/session_tools.go` `execTool`) serf sets the hook input's tool
name to `toolname.SerfToClaude(call.Name)`, and `shell` → `Bash`
(`agent/internal/toolname/toolname.go`). So the matcher that fires on
the shell tool is `"Bash"`, NOT `"shell"`. Matching the substring proof
to the real tool name: `"Bas"` is the substring of the real Claude name
`"Bash"` — exact mode rejects it, old regex mode would have accepted it.

## Pre-state

- `serf` built from this branch:
  ```bash
  cd /Users/jesse/prime-radiant/toil-suite/serf
  go build -o /tmp/serf ./cmd/serf
  set -a; . "$PWD/.env"; set +a   # zsh: bare `. .env` fails; use the explicit form
  ```
- A model whose instance is credentialed by `.env`. This card uses
  `--model openai/gpt-5.4-mini` (the `openai` instance, type `openai`,
  reading `OPENAI_API_KEY`).
- `/usr/bin/touch` present (a real binary, so exec-form is genuinely
  no-shell — `sh -c` would defeat the point of the test).

## Steps

1. Build a hermetic project + plugin tree (markers land in the project
   dir; absolute paths in `args` so exec-form needs no cwd assumptions):
   ```bash
   WORK=$(mktemp -d /tmp/serf-hook-scenario.XXXXXX)
   PROJ="$WORK/proj"
   mkdir -p "$PROJ" "$WORK/plugin/.claude-plugin" "$WORK/plugin/hooks"

   cat > "$WORK/plugin/.claude-plugin/plugin.json" <<EOF
   { "name": "hook-matcher-probe", "version": "0.0.1",
     "description": "Probe: exact matcher + exec-form." }
   EOF

   cat > "$WORK/plugin/hooks/hooks.json" <<EOF
   { "hooks": { "PreToolUse": [
     { "matcher": "Bash",
       "hooks": [ { "type": "command", "command": "/usr/bin/touch",
                    "args": ["$PROJ/HOOK_FIRED_EXACT"] } ] },
     { "matcher": "Bas",
       "hooks": [ { "type": "command", "command": "/usr/bin/touch",
                    "args": ["$PROJ/HOOK_FIRED_SUBSTRING"] } ] }
   ] } }
   EOF
   ```
   Hook A (`"Bash"`, exact) should fire; hook B (`"Bas"`, a substring of
   the real tool name) should NOT.

2. Run serf live, forcing the shell tool. `--verbose` emits NDJSON
   lifecycle events to stderr:
   ```bash
   /tmp/serf --model openai/gpt-5.4-mini --dir "$PROJ" \
     --plugin-dir "$WORK/plugin" --verbose \
     "Use the shell tool to run exactly this command: echo serf-hook-probe-marker. Then tell me the output. Do not use any other tool." \
     > "$WORK/run.log" 2>&1
   echo "exit=$?"
   ```

3. Check the markers and the event stream:
   ```bash
   ls "$PROJ"/HOOK_FIRED_EXACT          # MUST exist
   ls "$PROJ"/HOOK_FIRED_SUBSTRING      # MUST be absent
   grep -E "PLUGIN_LOADED|HOOK_START|HOOK_END" "$WORK/run.log"
   grep '"tool_name":"shell"' "$WORK/run.log" | head -1
   ```

4. (Substring control — isolates the matcher) A second plugin with a
   SINGLE hook whose matcher is `"Bas"` must NOT fire even with no other
   hook present, proving `"Bas"` truly does not match `"Bash"` at
   runtime (not merely that group 2 was skipped):
   ```bash
   mkdir -p "$WORK/plugin-control/.claude-plugin" "$WORK/plugin-control/hooks"
   cat > "$WORK/plugin-control/.claude-plugin/plugin.json" <<EOF
   { "name": "hook-substring-control", "version": "0.0.1",
     "description": "Control: Bas must NOT match Bash." }
   EOF
   cat > "$WORK/plugin-control/hooks/hooks.json" <<EOF
   { "hooks": { "PreToolUse": [
     { "matcher": "Bas",
       "hooks": [ { "type": "command", "command": "/usr/bin/touch",
                    "args": ["$PROJ/CONTROL_BAS_FIRED"] } ] }
   ] } }
   EOF
   /tmp/serf --model openai/gpt-5.4-mini --dir "$PROJ" \
     --plugin-dir "$WORK/plugin-control" --verbose \
     "Use the shell tool to run exactly this command: echo serf-hook-control-marker. Then tell me the output. Do not use any other tool." \
     > "$WORK/run-control.log" 2>&1
   ls "$PROJ"/CONTROL_BAS_FIRED                          # MUST be absent
   grep -c '"tool_name":"shell"' "$WORK/run-control.log" # >0 (shell ran)
   grep -cE "HOOK_START|HOOK_END" "$WORK/run-control.log" # MUST be 0
   ```

## Expected

- After step 3:
  - `HOOK_FIRED_EXACT` exists — the `"Bash"` hook fired via exec-form
    `/usr/bin/touch` (no shell), and exit 0 did not block the tool call
    (serf exits 0; the model returns `serf-hook-probe-marker`).
  - `HOOK_FIRED_SUBSTRING` is absent — `"Bas"` did not substring-match.
  - The NDJSON shows, in order: `PLUGIN_LOADED` (`hook-matcher-probe`);
    one `HOOK_START` with `"event":"PreToolUse","matcher":"Bash"`; the
    `shell` `TOOL_CALL_START`; one `HOOK_END` with `"matcher":"Bash",
    "exit_code":0`. Exactly ONE Start/End pair — the `"Bas"` group
    never ran. Observed shape:
    ```
    {"kind":"HOOK_START",...,"data":{"event":"PreToolUse","hook_type":"command","matcher":"Bash","plugin_name":"hook-matcher-probe"}}
    {"kind":"TOOL_CALL_START",...,"data":{"tool_name":"shell",...}}
    {"kind":"HOOK_END",...,"data":{"event":"PreToolUse",...,"matcher":"Bash","exit_code":0,"duration_ms":8}}
    ```
- After step 4: `CONTROL_BAS_FIRED` absent and ZERO hook events, while
  the shell tool was still called — `"Bas"` does not match `"Bash"`
  even as the sole hook.
- Falsification (the regression is back, in priority order):
  - `HOOK_FIRED_EXACT` is absent after a shell call → the exact matcher
    fails to fire on the real tool name (matcher broken, or
    serf→Claude name mapping changed and the matcher no longer tracks
    the real tool name).
  - `HOOK_FIRED_SUBSTRING` or `CONTROL_BAS_FIRED` appears → `"Bas"`
    substring-matched `"Bash"`: the regex-substring behavior regressed.
    (Counterfactual confirming this is the old behavior:
    `regexp.MustCompile("Bas").MatchString("Bash") == true`, whereas
    `"Bas" == "Bash"` is false.)
  - The hook never runs / serf errors spawning it → exec-form (`args`
    direct spawn) is broken.
  - serf exits non-zero or the tool is denied on an exit-0 hook →
    exit-0 wrongly blocks.

## Cleanup

```bash
# leave the tmpdir in place (rm -rf is blocked in this environment);
# the dir is self-contained under /tmp/serf-hook-scenario.*
echo "tmpdir retained: $WORK"
```

Session metadata under the XDG state dir lingers but is harmless.

## Sharp edges

- **Match against the Claude name, not `shell`.** The matcher is tested
  against `toolname.SerfToClaude(call.Name)`; for serf's `shell` tool
  that is `"Bash"`. A matcher of `"shell"` would NOT fire. If serf adds
  a shell tool with no Claude alias (passing through unchanged), match
  on that raw name instead.
- **Why `"Bas"`, not `"BashOutput"`.** The original regression note is
  `"Bash"` substring-matching `BashOutput`, but serf exposes no
  `BashOutput` tool, so that exact demo can't run live. `"Bas"` is a
  substring of the real tool name `"Bash"`, so it exercises the same
  substring-vs-exact distinction on a tool that actually exists. Both
  `"Bash"` and `"Bas"` are `[A-Za-z]`-only, so both take the exact /
  pipe-list path (not regex); the proof is exact-equality, not regex.
- **exec-form requires a real binary.** `/usr/bin/touch` is used so the
  no-shell path is genuinely exercised. Do NOT switch to `sh -c ...` —
  that reintroduces a shell and defeats the exec-form assertion. To run
  a script body in exec-form, invoke an interpreter with the script
  path as an `args` element (e.g. `{"command":"/bin/sh","args":["-c",...]}`
  only proves shell-form-via-args, which is not what this card tests).
- **The model must actually call the shell tool.** The prompt pins the
  exact command and forbids other tools; if a future model paraphrases
  or refuses, no tool call fires and the hook (correctly) never runs —
  that's a test-harness miss, not a matcher regression. Re-run or
  tighten the prompt; confirm a `"tool_name":"shell"` `TOOL_CALL_START`
  is present before trusting a "did not fire" result.
- **Absolute marker paths.** `args` paths are absolute so the exec-form
  child needs no working-directory assumptions; serf does not chdir the
  hook process to `--dir`.
- Use `--verbose` for the NDJSON lifecycle stream; without it the
  `HOOK_START`/`HOOK_END` evidence is not emitted and you must rely on
  the marker files alone.
