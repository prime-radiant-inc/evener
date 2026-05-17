# dev-hello-script: agent writes and runs a one-file Python script

**What this covers**: end-to-end smoke test that serf-as-coding-agent
can use an LLM to actually build software. Exercises `write_file` +
`exec_command` + `communicate` tools, the system prompt's "use code
for actions" guidance, and the agent loop's turn shape (USER_INPUT →
ASSISTANT(tool_call) → TOOL_RESULTS → ... → ASSISTANT(communicate)).
NOT trying to test specific model behavior — just confirms the
plumbing all the way through OAuth → adapter → loop → file system →
shell.

## Pre-state

- Hub running with `--serf` resolvable (sibling or PATH).
- OpenAI OAuth signed in (`./serf openai status` shows `source=oauth`),
  with quota available.
- `python3` on PATH so the agent can run the script.

## Steps

1. Create a run-specific temp directory so the test is hermetic and
   parallel-safe:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-dev-XXXXX)
   ```
2. Spawn a session via `/api/spawn` with `model=openai/gpt-5.5`,
   `working_dir=$tmpdir`, a one-paragraph prompt that asks for a
   single small artifact:
   ```bash
   TOKEN=$(cat ~/.serf/auth-token)
   curl -s -X POST -H "Content-Type: application/json" \
        -H "Authorization: Bearer $TOKEN" \
        -d "{\"prompt\":\"Create a file named hello.py in the current directory that prints exactly 'hello world' when run. Then run it with python3 to confirm the output. Report what you did.\",\"model\":\"openai/gpt-5.5\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
        http://localhost:9180/api/spawn
   ```
3. Capture `session_id` from the response.
4. Wait until either `$tmpdir/hello.py` exists OR the daemon has
   exited (idle timeout):
   ```bash
   until [ -f "$tmpdir/hello.py" ] || ! ps aux | grep -q "[s]erf serve.*$session_id"; do
     sleep 2
   done
   ```
   Cap with an outer timeout (~90s) so a stuck test doesn't hang.
5. Verify:
   ```bash
   cat "$tmpdir/hello.py"
   python3 "$tmpdir/hello.py"
   ```

## Expected

- `$tmpdir/hello.py` exists and contains a `print` statement that
  outputs exactly `hello world`.
- Running `python3 $tmpdir/hello.py` prints `hello world` on
  stdout, exit 0.
- Transcript (`find ~/.local/state/serf/projects -name "<id>.transcript.jsonl"`)
  has the expected turn shape:
  - 1 USER_INPUT (the prompt)
  - 1+ ASSISTANT turns containing `write_file` (with
    `file_path: hello.py`) and `exec_command` (with
    `command: python3 hello.py` or equivalent) tool calls
  - matching TOOL_RESULTS
  - a final `communicate` tool call summarizing what was done
- The daemon eventually reaches idle (or exits after the idle
  timeout) without panic / stuck-processing.
- Falsification: no file created, or file contents wrong, or the
  agent reports completion without actually running the script.

## Cleanup

```bash
rm -rf "$tmpdir"
```

Session metadata under `~/.local/state/serf/projects/...` lingers
but is harmless. Optional: `find ~/.local/state/serf/projects
-name "<session_id>*" -delete`.

## Sharp edges

- gpt-5.5 is the flagship OpenAI model in the harness list; the
  scenario uses it because the user requested it. Substitute any
  enumerated model if you want to test a smaller / cheaper run
  (e.g. `openai/gpt-5.2`, `anthropic/claude-haiku-4-5-20251001`).
- The agent system prompt instructs "use code for actions" and
  forbids inventing tool capabilities. A model that hallucinates a
  `create_file` tool that doesn't exist would either get a tool
  error or fall back to the right tool — both should be visible in
  the transcript. If you see hallucinated tools succeeding, the
  tool-name validation regressed.
- Idle timeout: an idle daemon eventually exits. Don't assume the
  daemon process is still running after the file is written —
  that's why the wait loop checks BOTH "file exists" and "daemon
  alive".
- This scenario doesn't exercise multi-turn correction, subagent
  delegation, or the task list — those are heavier surfaces with
  their own scenarios worth writing.
- If the agent writes the file via `apply_patch` (creating a new
  file via `*** Add File: hello.py`) instead of `write_file`,
  that's also a valid path — the transcript will show a different
  tool name. Update the falsification predicate if you want to be
  strict.
