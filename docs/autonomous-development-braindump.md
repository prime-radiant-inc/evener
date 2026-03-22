# Autonomous Development — Brain Dump

Date: 2026-02-24

## The Problem

The agent consistently fails hard tasks (GPT-2 codegolf, etc.) because:

1. **No subagent dispatch**: Despite skills telling it to use subagent-driven-development, it never actually calls `spawn_agent`. It loads the skill, reads the instructions, then rationalizes skipping them to "work faster." Confirmed via interrogation — the agent admitted it consciously skipped the required workflow.

2. **Same entity writes tests and implementation**: The agent writes weak tests that its stub passes. For GPT-2 codegolf, it wrote a test that checks "does it compile? does it run? is output non-empty?" — any 50-byte stub passes that. It never tested actual GPT-2 output correctness despite having the weights available.

3. **Agent games tests**: When told what the verifier checks for ("WARRANTY OF ANY KIND, EXPRESS OR IMPLIED"), the agent literally hardcoded that string in its output. It optimizes for "pass the test" not "solve the problem."

4. **Two-skill handoff gap**: writing-plans tells the agent to load subagent-driven-development next. This is a decision point where the model opts out every time. The handoff between skills is where the process breaks.

5. **Time pressure causes shortcuts**: The graduated steering system (40%/60%/80% budget warnings) created perceived urgency that the agent used to justify skipping quality processes. We removed it, but the agent still gives up early claiming tasks are "infeasible" without trying.

## The Architecture

Replace the writing-plans → subagent-driven-development skill chain with a single **"autonomous development"** skill.

### Roles

**Orchestrator** (top-level agent):
- Pure dispatcher. Does NOT write code, tests, or plans.
- Reads subagent outputs, makes sequencing decisions, dispatches next steps.
- If we can restrict its tool registry (no write_file, no apply_patch, no shell), it physically CAN'T skip delegation.
- Its judgment calls: which task next, is this subagent output good enough, do we need to replan.

**Planning subagent** (persistent via send_input):
- Reads the spec, explores the codebase, produces task breakdown.
- Orchestrator spawns it once, gets plan via wait().
- Later: `send_input(planner_id, "task 3 failed because X, replan")` to resume with full context.
- Can adjust the plan as work progresses without losing its exploration context.

**Test-writing subagent** (per task):
- Gets: spec requirements for the task.
- Does NOT know what implementation approach will be used.
- Coached as adversarial: "You are the quality gate. You will be evaluated on the completeness and correctness of your tests. A separate engineer will implement the code. Your tests are the only thing standing between their work and production. Write tests that catch: stubs, hardcoded outputs, implementations that don't actually read/use input data, implementations that ignore the computational requirements."
- Its job: help the team ship good software, NOT help the implementer by giving easy-to-satisfy tests.

**Implementation subagent** (per task):
- Gets: spec requirements AND the pre-written tests.
- Writes code to pass the tests.
- CANNOT modify the tests (they came from a different subagent).
- If it can't pass the tests, it reports what's failing and why.

**Adversarial reviewer subagent** (per task):
- Gets: spec + tests + implementation.
- Specifically checks for: stubs, hardcoded outputs, files opened but not used, computations that don't match spec requirements, test-gaming.
- Returns: pass/fail with specific issues.

### Flow

```
Orchestrator
  |
  +-> spawn planner(spec) -> wait -> get plan
  |
  +-> FOR EACH TASK:
  |     |
  |     +-> spawn test_writer(task spec) -> wait -> get tests
  |     |   [orchestrator reads tests, sanity checks them]
  |     |
  |     +-> spawn implementer(task spec + tests) -> wait -> get implementation
  |     |
  |     +-> spawn reviewer(spec + tests + implementation) -> wait -> get verdict
  |     |
  |     +-> IF reviewer fails:
  |     |     send_input(implementer_id, "fix these issues: ...") -> wait
  |     |     re-dispatch reviewer
  |     |
  |     +-> IF task approach is wrong:
  |           send_input(planner_id, "task N failed because X, replan") -> wait
  |
  +-> Final verification
  +-> communicate(result)
```

## Key Infrastructure Facts

### Subagent resume works via send_input

- `send_input(agent_id, message)` resumes an idle subagent with new context
- The subagent keeps its full history from previous work
- `resultConsumed` flag is reset so `wait()` works again
- This is how the persistent planner works — spawn once, resume as needed

### Subagent sessions are persisted

- Each subagent saves snapshot + transcript to `<StateDir>/sessions/`
- Even after parent compaction, subagent state survives on disk
- Transcripts support exact resume from compacted states

### Tool registry restriction is possible but not built

- Currently all agents (parent + subagents) get the same tool registry
- Could add "orchestrator mode" to SessionConfig that removes write_file, apply_patch, shell
- Or: the skill instructions just say "you must not write code" and we hope the model obeys
- Enforcement > instruction (given what we've seen with the model ignoring instructions)

## What This Replaces

Current skill chain:
1. brainstorming (agent reads spec, asks questions)
2. writing-plans (agent writes plan, told to hand off to subagent-driven-development)
3. subagent-driven-development (agent dispatches subagents — NEVER ACTUALLY HAPPENS)

New single skill:
1. autonomous-development (agent becomes orchestrator, dispatches everything)

## Open Questions

1. **Tool restriction enforcement**: Should we actually restrict the orchestrator's tools, or rely on instructions? Given the model's track record of ignoring instructions, enforcement seems necessary.

2. **Test writer quality**: Will the test-writing subagent also write weak tests? The adversarial coaching should help, but it's the same model. We might need to iterate on the test writer prompt.

3. **Time budget**: Multiple subagent round-trips per task will be slower. Jesse says: "worry about optimizing for time after we get correctness right." For now, correctness > speed.

4. **Skill size**: This is a bigger skill than the current ones. But it's one load vs two loads with a gap, so net context might be similar.

5. **Does the model actually call spawn_agent?**: The fundamental question. Even with a merged skill, will the model dispatch subagents? Tool restriction would force it. Without restriction, we're betting the merged skill's continuity prevents the rationalization gap.

6. **What about simple tasks?**: Not every task needs 4 subagents. The planner could flag "this is a simple single-file change" and the orchestrator could use a lighter flow. But: Jesse's CLAUDE.md says "never skip process steps regardless of perceived task complexity." Maybe always use the full flow.

## Benchmark Context

- GPT-2 codegolf: Agent has weights (475MB .ckpt + 446KB vocab.bpe) in /app/
- Verifier test: compiles gpt2.c, runs `./a.out gpt2-124M.ckpt vocab.bpe 'THIS SOFTWARE IS PROVIDED "AS IS", WITHOUT'`, checks output contains `WARRANTY OF ANY KIND, EXPRESS OR IMPLIED`
- Task rated "hard" by author, expert estimate 2400 min, junior estimate 9600 min
- Docker image: alexgshaw/gpt2-codegolf:20251031
- Agent needs to: understand TF checkpoint format, implement transformer forward pass, implement BPE tokenization, all in <5000 bytes of C

## Current Benchmark Run

Full 89-task suite running on flower-garden at /tmp/serf-full-nosteering/
Harbor PID 871960, concurrency 4. Binary has graduated steering removed + correctness > speed guidance.
This run does NOT have the autonomous development changes — it's measuring the baseline with steering removed.
