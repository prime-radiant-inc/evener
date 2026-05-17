# dev-fix-broken-script: agent reads, fixes, and re-runs a broken Python script

**What this covers**: end-to-end smoke test of the "fix this bug"
pattern — the most common real serf use case. Exercises `read_file`
(or equivalent grep_files / list_dir), `apply_patch` (or `write_file`
or `edit_file`), and `exec_command`. Confirms the agent loop can
handle a multi-turn task that requires inspecting existing state
before mutating it.

## Pre-state

- Hub running with `--serf` resolvable.
- OAuth signed in for whichever model you pick (`openai/gpt-5.5` per
  user instruction, or substitute).
- `python3` on PATH.

## Steps

1. Set up a hermetic tmpdir with a deliberately-broken script:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-fix-XXXXX)
   cat > "$tmpdir/buggy.py" <<'EOF'
   def add(a, b):
       return a + b

   def main():
       print(add(2, 3)
       print("done")

   if __name__ == "__main__":
       main()
   EOF
   ```
   Note the missing `)` on the `print(add(2, 3)` line. Running it
   raises `SyntaxError: '(' was never closed`.

2. Sanity-check that it's actually broken before involving the
   agent:
   ```bash
   python3 "$tmpdir/buggy.py" || echo "PASS — script is broken as expected"
   ```

3. Spawn a session pointed at the tmpdir with a focused fix
   prompt:
   ```bash
   TOKEN=$(cat ~/.serf/auth-token)
   curl -s -X POST -H "Content-Type: application/json" \
        -H "Authorization: Bearer $TOKEN" \
        -d "{\"prompt\":\"There is a Python script buggy.py in the current directory that fails with a syntax error when run. Fix the bug. Then run python3 buggy.py and confirm it prints 5 then done. Do not rewrite the whole script — only fix the syntax error.\",\"model\":\"openai/gpt-5.5\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
        http://localhost:9180/api/spawn
   ```
   Capture `session_id`.

4. Wait for the file to become valid Python (i.e. the fix landed).
   Cap with an outer deadline. Do NOT use "daemon gone" as the
   success signal — the agent's `edit_file` may complete just
   before the daemon idles out, and a "daemon gone" check can
   race with the atomic file write:
   ```bash
   deadline=$((SECONDS + 120))
   until python3 "$tmpdir/buggy.py" >/dev/null 2>&1 || [ $SECONDS -ge $deadline ]; do
     sleep 2
   done
   ```

5. Verify:
   ```bash
   python3 "$tmpdir/buggy.py"
   diff -u <(echo "5
   done") <(python3 "$tmpdir/buggy.py")
   ```

## Expected

- `$tmpdir/buggy.py` is now syntactically valid Python.
- Running it prints `5` then `done` on stdout, exit 0.
- The fix is MINIMAL — the `add` function and `main` structure are
  unchanged. Only the missing `)` was added. Falsification: the
  agent rewrote the script wholesale (e.g. inlined the `add`
  function, removed `if __name__ == "__main__":`, etc.). The
  system prompt says "Keep changes minimal and focused".
- Transcript shows:
  - A read step (`read_file` of `buggy.py` or `list_dir` + read).
  - An edit step (`apply_patch` with a small hunk, or `edit_file`
    with a minimal `old_string`/`new_string`, or `write_file` —
    last is acceptable but minimal-diff is preferred).
  - An exec step (`python3 buggy.py`) that succeeds.
  - A `communicate` summary.
- Falsification: the agent gives up after one failed exec without
  reading the file; or claims to have fixed without re-running.

## Cleanup

```bash
rm -rf "$tmpdir"
```

## Sharp edges

- The agent system prompt explicitly says "Never substitute a
  workaround for the real implementation" and "Keep changes minimal
  and focused". A model that rewrites the whole script when a
  one-character fix would do is technically passing the test but
  failing the spirit. The scenario's falsification predicate
  catches the worst cases (structural changes); finer-grained
  diff inspection is a separate concern.
- Idle daemons exit after the inactivity timeout. The wait loop
  catches both "the fix worked" (script runs) and "the daemon
  died trying" (panic, OOM, exec error).
- Multi-line python heredocs in bash need `'EOF'` quoting (not
  `EOF`) to preserve `(` and `$` literally. The setup snippet
  uses `'EOF'` deliberately.
- If the agent decides to use `apply_patch` and the patch context
  doesn't match the file exactly (e.g. trailing whitespace), the
  patch may fail and the agent will need to read again and retry.
  Multiple read/patch cycles in the transcript are fine.
- This scenario assumes `python3` is on PATH inside the daemon's
  exec environment. If serf's launch is sandboxing the daemon
  away from PATH, this test will appear to fail on the agent's
  side. Check `which python3` from the daemon's working_dir in
  that case.
