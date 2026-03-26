# Coordinator Override Experiments

**Date:** March 24, 2026
**Problem:** The coordinator re-derives answers independently, gets them wrong
(because it lacks domain context the implementer discovered), and overwrites
correct implementer work.

**Evidence:**
- chess-best-move rep 3: implementer got correct FEN + Stockfish mate-in-1,
  coordinator re-derived FEN with wrong piece colors, overwrote with 1 of 2 moves
- mteb-retrieve rep 1: implementer researched BGE prefix, applied it, got MTEB
  as rank 5 (correct), coordinator verified WITHOUT prefix, got HumanEval,
  spawned "fix" agent that overwrote correct answer

**Root cause:** coordinator.md step 4 says "verify yourself" and step 5 says
"Fix — if anything is wrong, spawn a fix agent." The coordinator interprets
"verify" as "re-derive the answer" rather than "check the deliverable exists
and is formatted correctly."

**Current coordinator wording (steps 4-5):**
```
4. **Verify yourself** — after the implementer finishes, check deliverables
   thoroughly. Don't just check files exist — read their contents and verify
   they make sense. Run test commands if available. Do NOT re-derive the answer
   independently — if the implementer validated with a domain tool, that
   validation is more trustworthy than your own analysis.
5. **Fix** — if a test or verification command fails, spawn a fix agent with
   the specific failure output. Do not "fix" work that passed the implementer's
   own verification based on your independent analysis.
```

The "do NOT re-derive" instruction exists but is being ignored. The coordinator
still runs its own Python code to check answers.

## Experiments

### Experiment 1: Stronger prohibition in verify step

Move the prohibition to the front of step 4 and make it the primary instruction.

```
4. **Verify yourself** — do NOT re-derive the answer. Check that deliverables
   exist, are correctly formatted, and contain reasonable content. If the
   implementer validated with a domain tool (engine, compiler, test suite),
   trust that validation.
```

**Target:** mteb-retrieve (coordinator override is the root cause)
**Regression:** distribution-search, kv-store-grpc

### Experiment 2: Remove "check contents" which triggers re-derivation

The "read their contents and verify they make sense" wording may be what
triggers the coordinator to run its own analysis code.

```
4. **Verify yourself** — check that deliverables exist and meet format
   requirements. Do NOT re-derive or recompute the answer — the implementer
   already validated it.
```

**Target:** mteb-retrieve
**Regression:** distribution-search, kv-store-grpc

### Experiment 3: Make fix step require test failure evidence

Currently the coordinator spawns fix agents based on its own analysis.
Require a concrete test failure.

```
5. **Fix** — ONLY spawn a fix agent if a TEST or VERIFICATION COMMAND failed
   with specific error output. Your own analysis is not grounds for a fix.
   If no test exists and the implementer reported success, trust it.
```

**Target:** mteb-retrieve
**Regression:** distribution-search, kv-store-grpc

### Experiment 4: Remove verify step entirely for non-test tasks

For tasks without explicit test commands, the coordinator just submits.

```
4. **Verify yourself** — if test commands are available, run them. If no
   tests exist, check that deliverable files exist and submit.
```

**Target:** mteb-retrieve
**Regression:** distribution-search, kv-store-grpc

### Experiment 5: Coordinator cannot run code

Remove `shell` from coordinator tools so it literally cannot re-derive answers.

```
tools: [glob, grep, read_file, spawn_agent, resume_agent, task_list]
```

**Target:** mteb-retrieve
**Regression:** distribution-search, kv-store-grpc (coordinator uses shell
for verification on these — may break them)

### Experiment 6: Coordinator asks implementer to re-verify instead of doing it itself

Instead of running its own verification, the coordinator resumes the
implementer and asks it to double-check.

```
4. **Verify** — resume the implementer and ask it to verify its own work
   from an outsider's perspective. Do not run verification yourself.
```

**Target:** mteb-retrieve
**Regression:** distribution-search, kv-store-grpc

## Results

### The HARD GATE contradiction (debrief finding)

We resumed the coordinator session from mteb-retrieve rep 3 (where it overrode
the correct answer) and asked it why it re-derived the answer despite the
"Do NOT re-derive" instruction. Its response:

> The HARD GATE says "You MUST NOT call communicate until your verification
> passes. If you haven't run verification commands yourself, you haven't
> verified." That means I must run verification commands. The no-rederive
> instruction conflicts with the gate, so I followed the gate.

The contradiction was between step 4 ("Do NOT re-derive") and the HARD GATE
("If you haven't run verification commands yourself, you haven't verified").
The model followed the HARD GATE because it was framed as a blocking
requirement for the communicate action.

### Variant design

Based on the debrief, we designed 4 coordinator variants that each resolve the
contradiction differently:

| Variant | Step 4 | HARD GATE | Fix approach |
|---------|--------|-----------|--------------|
| **A: artifact-only** | "Coordinator verification is artifact-only. Check deliverables exist, inspect contents and format, confirm workspace cleanliness, review implementer's reported verification evidence. You MUST NOT rerun the implementation or execute any command that recomputes the task output." | "Verification means inspecting files, logs, and workspace state — not rerunning the computation." | Align both step 4 and the gate on artifact inspection |
| **B: inspect-not-run** | "Inspect the results. Read deliverable files, check format and content. Do not rerun the implementation or recompute the answer." | "Call communicate after inspection passes." | Rename "verify" to "inspect" throughout |
| **C: trust-implementer** | "Check deliverables exist. If a file is missing, spawn a fix agent. If files exist, submit." | "Call communicate after the implementer finishes and deliverable files exist." | Remove verification entirely |
| **D: test-only** | "Run the project's test suite if one exists. If no test suite exists, check that deliverable files exist and are non-empty, then submit. Never write your own verification code or rerun the implementation." | "If no test suite exists, verification passes when deliverable files exist and are non-empty." | Only allow running existing tests, no custom verification |

### Local results (mteb-retrieve x 3 reps each)

| Variant | Rep 1 | Rep 2 | Rep 3 | Rate |
|---------|-------|-------|-------|------|
| A: artifact-only | PASS | PASS | PASS | **3/3** |
| B: inspect-not-run | FAIL (coordinator ran Python to "inspect") | FAIL | FAIL | 0/3 |
| C: trust-implementer | FAIL (implementer didn't research) | PASS | FAIL | 1/3 |
| D: test-only | FAIL (no test suite, coordinator wrote its own) | FAIL | FAIL | 0/3 |

**Why A won:** It's specific about what "artifact-only" means — file existence,
format, workspace state, implementer's reported evidence. It explicitly
prohibits "any command that recomputes the task output." Both step 4 and the
HARD GATE say the same thing, so there's no contradiction to resolve.

**Why B failed:** "Inspect" is vague enough that the coordinator interpreted it
as "run Python to inspect the answer." The coordinator is creative about
interpreting ambiguous verbs as justification for running code.

**Why C failed:** Without any verification, tasks that need coordinator-level
checking (e.g., file format, workspace cleanliness) regress. Also, the 1/3
pass was because the implementer happened to research on that rep — C doesn't
help with the research problem.

**Why D failed:** Most benchmark tasks don't have a standalone test suite. The
"if no test suite exists" branch just checks file existence, which isn't enough.
Worse, "Never write your own verification code" was ambiguous — rep 1's
coordinator wrote a test suite ("this isn't verification code, it's a test").

### AWS regression (variant A, 9 tasks x 1 rep)

| Task | Result |
|------|--------|
| distribution-search | PASS |
| kv-store-grpc | PASS |
| sanitize-git-repo | PASS |
| feal-linear-cryptanalysis | PASS |
| winning-avg-corewars | PASS |
| build-pov-ray | PASS |
| regex-log | PASS |
| pypi-server | PASS |
| adaptive-rejection-sampler | FAIL (known nondeterministic) |

**8/9 passed.** Variant A shipped.

## Key Takeaway

When a GPT-5.4 agent disobeys an instruction, **resume the session and ask it
why**. The model will tell you which competing instruction won. In this case,
the HARD GATE overrode the no-rederive rule. The fix was to align both
instructions instead of having them compete.
