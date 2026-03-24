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

## Test Plan

All experiments run locally first using the implementer harness at
/tmp/impl-harness/ to iterate quickly, then the best variant goes to AWS
with mteb-retrieve ×3 + regression ×3.

## Success Criteria

- mteb-retrieve passes (implementer's correct answer survives to verifier)
- Regression tasks still pass (coordinator verification still works for
  tasks where it's helpful)
