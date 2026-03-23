# Next Tuning Round: Write-Early, Interface Verification, Nondeterminism

**Date:** March 23, 2026
**Baseline:** gpt-5.4-mini + xhigh, 44/81 reliably passing (54%)
**Target:** Move 8-12 tasks from flaky/failing to reliable

## The Three Problems

### Problem 1: Write-Last / Analysis Paralysis (8 tasks)

The agent spends its entire budget analyzing and never writes the deliverable.
The "do the work, then verify" prompt in core.md isn't strong enough.

**Affected tasks:** db-wal-recovery, protein-assembly, tune-mjcf, git-multibranch,
make-mips-interpreter, path-tracing-reverse (fails), configure-git-webserver (fails),
regex-chess (fails)

**Hypothesis:** The agent treats "do the work" as "do the analysis." It needs to
understand that "the work" means producing the output file. The verification step
is separate.

**Proposed fix:** Rewrite the Workflow section in core.md to be more explicit:

```
## Workflow

Produce deliverables first. Analyze, implement, and write output files before
running any verification or validation. If you haven't written your output files,
you haven't started the work — you're still planning.
```

**Test plan:**
- Target: tune-mjcf (1/3, clearest write-last pattern)
- Control: path-tracing-reverse (1/3, strategy choice + write-last)
- Regression: 3 reliable tasks (cancel-async-tasks, regex-log, fix-git)
- 3 reps each on AWS

### Problem 2: Wrong Interface / API Mismatch (5 tasks)

The agent builds something that works but doesn't match the interface the hidden
test expects. Wrong argument format, wrong field names, wrong file paths, wrong
file edited.

**Affected tasks:** sam-cell-seg (0/3), torch-tensor-parallelism (0/3),
kv-store-grpc (1/3), winning-avg-corewars (1/2), caffe-cifar-10 (0/3)

**Hypothesis:** The agent makes assumptions about interfaces instead of using
the most standard/conventional approach. When the task says "write a script that
takes weights_path, output_path," the agent should default to `--named_args`
(argparse convention), not positional args.

**Proposed fix:** Add to coordinator.md delegation guidelines:

```
- When the task specifies input/output parameters, use standard CLI conventions:
  named arguments with -- prefixes, standard library argument parsing.
- Use absolute paths for all deliverables. Write to /app/ unless told otherwise.
- When editing existing files, verify you're editing the original, not a copy.
```

**Test plan:**
- Target: sam-cell-seg (0/3, clearest interface mismatch)
- Target: caffe-cifar-10 (0/3, edited copy not original)
- Regression: 3 reliable tasks
- 3 reps each on AWS

### Problem 3: Nondeterminism (7 tasks)

Tasks that pass 1/3 because the model's interpretation of ambiguous requirements
varies between runs. The passing run found a key insight; the failing runs didn't.

**Affected tasks:** count-dataset-tokens (1/3), mteb-retrieve (1/3),
sanitize-git-repo (1/3), qemu-alpine-ssh (1/3), llm-inference-batching-scheduler
(1/3), schemelike-metacircular-eval (1/3), sqlite-with-gcov (1/3)

**Hypothesis:** The coordinator doesn't verify carefully enough before submitting.
If the coordinator ran the verifier's actual commands (or similar validation), it
would catch misinterpretations before they become failures.

**Proposed fix:** Strengthen coordinator step 4 (Verify yourself):

```
4. **Verify yourself** — after the implementer finishes, check that deliverables
   exist and meet the requirements. Run test commands if available. Check file
   contents, not just file existence. If the task has specific expected output,
   compare your result against it before submitting.
```

Also: for tasks where interpretation varies (count-dataset-tokens, mteb-retrieve),
the agent should cross-validate its approach — compute the answer two different
ways and check they agree.

**Test plan:**
- Target: sanitize-git-repo (1/3, misses embedded token)
- Target: sqlite-with-gcov (1/3, build config loses .gcno)
- Regression: 3 reliable tasks
- 3 reps each on AWS

## Execution Plan

### Phase 1: Implement fixes (1 change at a time)

**Fix A: Write-early reinforcement (core.md)**
1. Edit core.md Workflow section
2. Build, test, commit on branch `fix-write-early-v2`
3. Launch: tune-mjcf ×3, path-tracing-reverse ×3, regression ×1
4. Root cause any failures from transcripts
5. If improved (≥2/3): ship. If not: revert, try different wording.

**Fix B: Interface conventions (coordinator.md)**
1. Add delegation guidelines about CLI conventions and paths
2. Build, test, commit on branch `fix-interface-conventions`
3. Launch: sam-cell-seg ×3, caffe-cifar-10 ×3, regression ×1
4. Root cause any failures
5. If improved: ship.

**Fix C: Verification depth (coordinator.md)**
1. Strengthen verify step
2. Build, test, commit on branch `fix-verify-depth`
3. Launch: sanitize-git-repo ×3, sqlite-with-gcov ×3, regression ×1
4. Root cause any failures
5. If improved: ship.

### Phase 2: Validate combination

After shipping individual winners, run the combination on all affected tasks
plus regression set to confirm they don't interfere.

### Phase 3: Full eval

Run full 89-task eval with gpt-5.4-mini to measure aggregate impact.

## Anti-Patterns to Avoid

- **Teaching to the test:** Every fix must be a general principle. "Use --named_args"
  is general (CLI convention). "Put the socket at /tmp/qemu-monitor.sock" is not.
- **Bundling changes:** One fix per experiment. If both help, merge them after.
- **Guessing root causes:** Read the actual transcript before proposing a fix.
- **Ignoring regressions:** Every fix must pass the regression set.

## Success Criteria

- Move ≥3 tasks from 0/3 or 1/3 to ≥2/3
- No regressions on the 44 reliably passing tasks
- Each fix validated with 3 reps before shipping
