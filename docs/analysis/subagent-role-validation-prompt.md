You are validating the revised `worker` and `subagent` agent profiles.

You are the top-level test runner for this task. Execute the test plan yourself.
Do not delegate the evaluation task to another subagent that would then need to
spawn more subagents. The agent running this prompt must be the one that calls
`spawn_agent`, `wait`, and `resume_agent` for the test runs.

Goal:
Compare their actual behavior on small delegated tasks and verify that:
- `subagent` behaves like a generic scoped delegate
- `worker` behaves like an implementation-heavy executor with stronger verification bias

Rules:
- Do not modify the prompt files.
- Prefer small, cheap tests.
- Save exact outputs, exit codes, and transcript paths.
- Report observed behavior, not intentions.
- Do not hand this plan to a lower-level agent and ask it to conduct the tests.
- If you need parallelism, use your own parallel tool calls at the top level.

Run these tests:

1. Minimal command test
- Spawn a `subagent` to run exactly one simple shell command like `printf 'SUBAGENT_OK\n'`.
- Check whether it does only that task or adds extra verification/workspace inspection.

2. Minimal command test for worker
- Spawn a `worker` to run exactly one simple shell command like `printf 'WORKER_OK\n'`.
- Check whether it adds extra verification steps beyond the literal request.

3. Read-only inspection test
- Ask `subagent` to read one named file and report one specific fact from it.
- Verify it does not assume code changes are needed.

4. Implementation-leaning test
- Ask `worker` for a tiny code/config change with a relevant targeted verification step.
- Verify it actually behaves like an implementation agent and reports evidence.

5. Strict scope comparison
- Give both agents the same narrowly-scoped task.
- Compare how much initiative each takes beyond the request.

For each run, record:
- agent type
- exact delegated prompt
- commands/tools used
- whether files were modified
- whether extra verification happened
- whether behavior matched the intended role
- transcript artifact path

Final output format:
- Short verdict for `subagent`
- Short verdict for `worker`
- Key behavioral differences
- Any remaining prompt mismatches
- Recommended follow-up edits, if any

Execution note:
- This prompt is intended for a top-level agent with delegation tools.
- Good choices: the current session agent, a coordinator, or another agent profile
  that already has `spawn_agent` available.
- Do not run this prompt inside a worker or subagent that lacks delegation tools.
