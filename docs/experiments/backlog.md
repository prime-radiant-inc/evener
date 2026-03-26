# Experiment Backlog

Prioritized queue of next experiments. Updated March 25, 2026.

## 1. SystemPromptAsUser combined message

Combine the system prompt and task delegation into ONE user message so persona
instructions are adjacent to the task, not separated. The current implementation
delivers them as separate messages with system prompt first — GPT-5.4 ignores
instructions at the beginning of context.

**Status:** Not yet implemented. The previous test (0/3 on AWS) put the system
prompt BEFORE the task in a separate message, which doesn't help.

**Test plan:**
- Implement combined message delivery in session.go
- Test locally with the implementer repro harness on mteb-retrieve
- If local signal is positive, run on AWS (mteb-retrieve ×3 + regression set)

## 2. Ship variant A coordinator

Artifact-only verification (`docs/experiments/variants/coordinator-artifact-only.md`).
Already validated 8/9 on AWS regression — only adaptive-rejection-sampler failed
(known nondeterministic). Copy to `agent/agents/coordinator.md` and commit to main.

## 3. Ship workspace tree fix

Replace parent tree scan with workspace tree in `profile.go`. Already validated —
fixes the 78K `/tmp/` dump that was confounding all local tests. Just needs to be
committed to main.

## 4. Full eval with shipped fixes

Run full 89-task eval to measure aggregate impact of everything shipped since the
64% baseline (vision side-channel, write-early, verify-depth, web_fetch, no-rederive,
coordinator artifact-only, workspace tree fix).

## 5. mteb-retrieve deeper investigation

Even with correct message delivery (XML prerequisites in user message), the
implementer may not use the BGE prefix correctly after reading the README. The
pattern observed locally: reads README but still chooses raw query instead of
prefixed query. Need to validate H28 on AWS (local results were contaminated by
the /tmp/ dump) and investigate whether the implementer correctly applies what it
reads.
