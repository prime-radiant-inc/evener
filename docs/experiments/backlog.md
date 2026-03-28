# Experiment Backlog

Prioritized queue of next experiments. Updated March 28, 2026.

Based on interrogation of all 55 failing reps from wave-08b8a7f-20260328.
See `experiment-log.md` for the full failure inventory.

## Next up

Priority order for hill-climbing. Each experiment targets a specific systemic
pattern identified by interrogation. Test on affected tasks, verify regression
set holds.

### 1. Coordinator test gate — run verifier before submit

**Targets:** 20 shallow-verification tasks (36% of all failures)
**Root cause:** Coordinator trusts subagent completion reports without running tests.
The "never trust a subagent's completion report" instruction exists but is routinely
deprioritized against "do not re-derive the answer independently."

**Hypothesis:** Add hard-gate instruction: coordinator MUST execute `run_tests`
(or the task's test command) as the FINAL step before `communicate`. Test failure
= do not submit. This resolves the competing-instruction conflict by making the
test gate unambiguous.

**Test tasks (0/3 shallow-verification, most room to improve):**
- configure-git-webserver, db-wal-recovery, model-extraction-relu-logits,
  sam-cell-seg, torch-pipeline-parallelism
- Plus regression: crack-7z-hash, log-summary-date-ranges, chess-best-move

**Reps:** 3 per task

**Risk:** Coordinator may burn budget running tests that require complex setup,
or may fail to find the test command. Monitor for timeout regressions.

### 2. Implementer deliverable-first checkpoint

**Targets:** 8 analysis-paralysis tasks (15% of all failures)
**Root cause:** Implementer spends entire budget analyzing without creating
deliverable files. The "produce deliverables first" instruction is known but
deprioritized against "understand the problem fully."

**Hypothesis:** Hard checkpoint instruction: "After inventory, your FIRST
implementation tool call must create or write to the required deliverable file.
You may not make more than 3 tool calls without producing output. Analysis
without a deliverable file is not permitted."

**Test tasks (0/3 analysis-paralysis, deliverable never created):**
- dna-assembly, mailman, make-mips-interpreter, path-tracing-reverse,
  polyglot-rust-c
- Plus regression: crack-7z-hash, custom-memory-heap-crash

**Reps:** 3 per task

**Risk:** Premature deliverable may be low quality, causing implementer to
submit garbage. The checkpoint should enforce creation, not submission — the
implementer should still iterate after the first write.

### 3. Spec-literal naming instruction

**Targets:** 7 spec-mismatch tasks (13% of all failures)
**Root cause:** Implementers choose "reasonable" field names/formats instead of
matching the spec's exact wording (e.g., `val` vs `value`, arrays vs scalars).

**Hypothesis:** Add instruction: "When the task spec names a parameter, field,
output column, or file format, use the EXACT word from the spec. Do not
abbreviate, rename, or 'improve' spec-provided identifiers. If the spec says
'value', your code must use 'value', not 'val' or 'v'."

**Test tasks:**
- kv-store-grpc, video-processing, mcmc-sampling-stan, overfull-hbox
- Plus regression: log-summary-date-ranges, fix-code-vulnerability

**Reps:** 3 per task

**Risk:** Low — this is additive guidance that shouldn't conflict with other
instructions.

### 4. Anti-fabrication strengthening

**Targets:** 3 fabrication tasks (5% of all failures)
**Root cause:** "Derive from tools" instruction exists but agents describe
efficiency pressure as competing. When tools fail, agents guess rather than
reporting inability.

**Hypothesis:** Strengthen to: "If your tools cannot produce the answer, report
that you could not solve it via communicate. NEVER guess or reconstruct from
memory. A wrong answer is always worse than admitting inability."

**Test tasks:**
- extract-moves-from-video, mteb-leaderboard, chess-best-move
- Plus regression: crack-7z-hash, custom-memory-heap-crash

**Reps:** 3 per task

**Risk:** May cause premature surrender on tasks where the agent should try
harder. Monitor for false-negative increase on currently-passing tasks.

### 5. Reviewer trust hierarchy

**Targets:** 1 task (mteb-retrieve), but fixes a general vulnerability
**Root cause:** Coordinator treats reviewer corrections as authoritative,
following "do not re-derive" too literally.

**Hypothesis:** Amend coordinator instruction: "Do not duplicate large
implementation work, but DO independently verify any final output value that
determines pass/fail. When a reviewer suggests a different answer, verify it
against the test suite before accepting."

**Test tasks:**
- mteb-retrieve
- Plus regression: chess-best-move (reviewer involved in passing pipeline)

**Reps:** 3 per task

### 6. Workspace cleanup clarification

**Targets:** 2 tasks (portfolio-optimization, polyglot-c-py)
**Root cause:** "Clean up working directory — only deliverable files should
remain" causes agents to delete compiled .so files and test artifacts that
the verifier needs.

**Hypothesis:** Amend to: "Remove only temporary build intermediates and scratch
files. Keep compiled extensions (.so, .dll), executables, and any artifacts
required for the submission to import and run."

**Test tasks:**
- portfolio-optimization, polyglot-c-py
- Plus regression: any task with compiled artifacts

**Reps:** 3 per task

## Deprioritized

### Genuine difficulty (9 tasks)

These tasks are algorithmically hard. Prompt changes alone are unlikely to fix
them. Revisit only after all higher-priority experiments are complete.

gcode-to-text, gpt2-codegolf, protein-assembly, rstan-to-pystan, train-fasttext,
winning-avg-corewars, write-compressor, large-scale-text-editing, fix-ocaml-gc,
torch-tensor-parallelism

### Environment issues (2 tasks)

caffe-cifar-10 and vulnerable-secret failed due to session corruption (orphaned
tool calls). These are infrastructure bugs, not agent behavior. The interrogation
tool fix (commit 0d224b5) addresses the interrogation side; the root session
corruption needs investigation in the serf runtime.

### Previously identified (from pre-wave backlog)

- **Delegation info loss** — only 1 task (fix-code-vulnerability) showed this
  pattern in the wave run. Deprioritized vs the 20-task shallow-verification fix.
- **Coordinator bypass (stochastic)** — 3 tasks showed this. chess-best-move went
  2/3 suggesting the existing ops-task removal fix mostly works. Monitor.
- **Budget awareness** — only qemu-alpine-ssh showed pure timeout. Low priority.

## Completed / shipped

### Effective fixes (validated with improved scores)
- **v19-deleg-b** — "quality gate, not worker" framing -> chess-best-move 3/3
- **v19-state-b** — post-test mutation check -> git-multibranch 3/3
- **v17+v18** — harmonize HARD GATE + no-tests case -> log-summary-date-ranges 3/3
- **v23-B** — reviewer: remove "intuit", spec authority -> chess-best-move 3/3
- **v26** — neutral task_list phrasing -> custom-memory-heap-crash 3/3
- **ops-task removal** — deleted embedded skill that primed direct implementation
- **coordinator inventory fix** — "listing, not reading" + anti-rationalization
- **v11** — reviewer positive authority ordering + warnings-are-not-failures -> 6/6
- **v13** — tests-first verification + submit gate -> fix-code-vuln 3/3, git-multi 3/3

### Infrastructure shipped
- Template section ordering (Role before Skills)
- Verification language revert (artifact-only -> run tests)
- Workspace cleanup in shared values
- harbor-runner: removed parent-dir binary copy
- Makefile: `build-linux` target with cache invalidation
- Session interrogation tool (real session resume, subagent support)
- Root-cause prompt template
- Eval tooling: run_eval.sh (unified launcher with wave mode), run_status.sh, post_run.sh, interrogate_failures.sh
- Wave launcher: wave_launcher.py + instance ID parsing fix
- Interrogation fixes: verifier cross-contamination, substring matching, orphaned tool calls

### Tried and superseded (no longer active)
- Scratch directory verification — v9/v10: still violated despite instruction
- Active pre-submit workspace check — v9/v10: still violated
- Anti-gold-plating — v10: overridden by competing instructions (resolved differently in v11)
- Reviewer consistency check — v9: 2/3, v10: 0/3 (superseded by v11 positive authority)
- v25 reviewer experiments — all made things worse, do not revisit
- impl-test-a — 3/3 was stochastic, not shipped
