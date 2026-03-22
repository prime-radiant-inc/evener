# Plan: Lace Gate Prompt Experiment — 2026-03-09

## Goal
Improve lace benchmark pass rate by rewriting the benchmark persona (benchmark-h22.md)
to use formal verification gates in the workflow, replacing the advisory "Before You Stop"
checklist that the model ignores.

## Background
- Current: 56/89 raw (62.9%), 60/89 corrected (67.4%)
- Key failure pattern: agent writes code and stops without testing (torch-tensor-parallelism
  did 10 steps, sam-cell-seg did 12 steps, polyglots left artifacts)
- "Before You Stop" checklist added in prompt-v2 had zero effect on 3/7 tasks
- Hypothesis: verification must be a GATE in the workflow (you cannot proceed past it)
  not a checklist at the end

## Approach
1. Rewrite benchmark-h22.md workflow with explicit gates:
   - GATE 1 (after explore): "Have I read the test scripts? Do I know what tools are installed?"
   - GATE 2 (after each sub-task): "Does this sub-task's output actually work? Run it."
   - GATE 3 (before done): "Run the full test suite. Check output files exist. Clean workspace."

2. The key insight: gates are NOT "things to check" — they're "you cannot pass this point
   without doing this." The workflow digraph should make the gate a required node, not
   an optional branch.

3. Test iteratively against polyglot tasks using lace running locally.

## Test Tasks
- polyglot-rust-c: agent must write code, compile in both languages, verify output, clean artifacts
- polyglot-c-py: same pattern but Python + C
- These are ideal because: fast verification, clear pass/fail, the failure mode is exactly
  "didn't verify before stopping"

## Local Test Setup
1. Create /tmp/app/polyglot/ as the work directory
2. Run lace headless locally with --workdir /tmp/app
3. Use gpt-5.2-codex via OpenAI API
4. After each run: check /tmp/app/polyglot/ for correct files, run the test script
5. If it fails: interrogate the agent about what went wrong (or inspect transcript)

### Prerequisites
- rustc: installed (1.93, compatible with 1.75 task requirement)
- g++: Apple clang (close enough for polyglot testing)
- python3: installed
- lace binary: already built locally at /Users/jesse/git/lace/build/ (macOS arm64)
  Need to build macOS version, not linux — the linux binary was for magic-kingdom

### Build macOS lace binary
```bash
cd /Users/jesse/git/lace
bun build --compile --outfile build/lace-agent-macos packages/agent/src/main.ts
```

### Run locally
```bash
mkdir -p /tmp/app/polyglot
cd /Users/jesse/git/lace
export OPENAI_API_KEY=<from .env>
./build/lace-agent-macos --headless \
  --workdir /tmp/app \
  --connection openai-default \
  --model gpt-5.2-codex \
  --persona benchmark-h22 \
  -- "$(cat /Users/jesse/prime-radiant/serf/prompt-lab/tasks/polyglot-rust-c/instruction.txt)"
```

### Verify
```bash
ls /tmp/app/polyglot/  # should contain ONLY main.rs
rustc /tmp/app/polyglot/main.rs -o /tmp/app/polyglot/main && /tmp/app/polyglot/main 10
g++ -x c++ /tmp/app/polyglot/main.rs -o /tmp/app/polyglot/cmain && /tmp/app/polyglot/cmain 10
# Both should print 89
```

## Iteration Loop
1. Edit benchmark-h22.md with gate-based prompt
2. Rebuild macOS binary (fast — <2s with bun)
3. Clear /tmp/app/polyglot/
4. Run lace headless with polyglot-rust-c task
5. Check results — did agent verify? Did it clean up?
6. If no: read transcript, understand why gate was skipped, tighten prompt
7. Repeat until both polyglot tasks pass consistently
8. Then test on torch-tensor-parallelism and sam-cell-seg (need Docker for those)
9. If gates work locally: rebuild linux binary, deploy, run broader eval

## Success Criteria
- Both polyglot tasks pass on 3/3 local runs
- Agent transcript shows explicit verification steps (compile, run, check output)
- Agent cleans up build artifacts before stopping
