# Failure Root Causes: gpt-5.4-mini Full Eval

**Date:** March 23, 2026
**Eval:** 88 tasks scored, 81 with 3 reps, gpt-5.4-mini + xhigh reasoning

## Headline Classification

| Category | Count | Tasks |
|----------|-------|-------|
| **HARD** (genuinely too hard) | 11 | circuit-fibsqrt, dna-assembly, dna-insert, fix-ocaml-gc, gpt2-codegolf, mailman, make-doom-for-mips, make-mips-interpreter, path-tracing, video-processing, write-compressor |
| **WRONG_APPROACH** (fixable strategy error) | 10 | caffe-cifar-10, db-wal-recovery, gcode-to-text, mcmc-sampling-stan, model-extraction-relu-logits, overfull-hbox, sam-cell-seg, torch-tensor-parallelism, raman-fitting, train-fasttext |
| **UNDERSPECIFIED** (task description gap) | 3 | install-windows-3.11, query-optimize, mteb-leaderboard |
| **NONDETERMINISTIC** (same approach, luck) | 7 | count-dataset-tokens, mteb-retrieve, sanitize-git-repo, qemu-alpine-ssh, llm-inference-batching-scheduler, schemelike-metacircular-eval, sqlite-with-gcov |
| **STRATEGY_CHOICE** (took wrong path) | 5 | configure-git-webserver, path-tracing-reverse, regex-chess, git-multibranch, tune-mjcf |
| **BUG** (specific implementation bug) | 4 | kv-store-grpc, polyglot-rust-c, winning-avg-corewars, torch-pipeline-parallelism |
| **TIMEOUT** (ran out of time) | 2 | protein-assembly, train-fasttext |

*Some tasks have multiple classifications.*

## Systemic Patterns

### Pattern 1: Write-Last / Analysis Paralysis (8 tasks)

Tasks where the agent spent the entire budget analyzing and never wrote the deliverable.

| Task | What happened |
|------|--------------|
| db-wal-recovery | Found 5/11 records, then entered degenerate loop reading own transcript logs |
| protein-assembly | 225 tool calls (179 web searches!), never wrote gblock.txt |
| tune-mjcf | Explored MuJoCo internals endlessly, never wrote model.xml |
| git-multibranch | Coordinator kept re-verifying instead of finishing |
| make-mips-interpreter | 50+ analysis calls, never started writing vm.js |
| path-tracing-reverse (fails) | Manually decompiled every function from assembly instead of writing code |
| configure-git-webserver (fails) | Rabbit-holed into SSH permission debugging |
| regex-chess (fails) | Spent all time debugging regex group references |

**Fix:** The "do the work, then verify" prompt helps but isn't enough. The agent needs stronger guardrails against unbounded analysis. Possible: turn budget warnings, mandatory checkpoint writes at 50% budget.

### Pattern 2: Wrong Interface / API Mismatch (5 tasks)

Tasks where the agent built a working solution but with the wrong interface that the hidden test expects.

| Task | What happened |
|------|--------------|
| sam-cell-seg | Used positional args; test expects `--named_args` |
| torch-tensor-parallelism | Weight shape (in,out) vs PyTorch convention (out,in) |
| kv-store-grpc | Proto field `val` vs test expects `value` |
| winning-avg-corewars | Wrote to `/app/pmars-0.9.4/` vs test expects `/app/` |
| caffe-cifar-10 | Edited solver copy, not original file test checks |

**Fix:** The coordinator should verify deliverables match expected interfaces. For tasks with hidden tests, the agent should use the most conventional/standard approach (standard argparse flags, PyTorch conventions, absolute paths).

### Pattern 3: Underspecified Requirements (3 tasks)

Tasks where the test checks something the description doesn't mention. Terminal-bench 2.1 PR fixes these.

| Task | Gap |
|------|-----|
| install-windows-3.11 | Socket path `/tmp/qemu-monitor.sock` not in description |
| query-optimize | "Don't modify database" not in description; test checks SHA256 |
| mteb-leaderboard | Hardcoded expected model name from a specific date |

**Fix:** Wait for terminal-bench 2.1.

### Pattern 4: Genuinely Hard (11 tasks)

Tasks that require deep domain expertise or extreme engineering that's beyond gpt-5.4-mini.

| Task | Why it's hard |
|------|--------------|
| circuit-fibsqrt | Synthesize multi-thousand-gate arithmetic circuit from scratch |
| dna-assembly | Golden Gate primer design with Tm/overhang constraints |
| dna-insert | Site-directed mutagenesis primer design |
| fix-ocaml-gc | OCaml compiler bootstrap after runtime C changes |
| gpt2-codegolf | Correct GPT-2 inference in <5000 bytes of C |
| mailman | Orchestrate Postfix + Mailman3 daemons in container |
| make-doom-for-mips | Cross-compile DOOM to run in custom MIPS VM |
| make-mips-interpreter | Write a MIPS interpreter that can run DOOM |
| path-tracing | 0.97 similarity achieved, 0.99 threshold too demanding |
| video-processing | Algorithm overfit to example video, fails on hidden test |
| write-compressor | Reverse-engineer custom arithmetic coding format |

**Fix:** These likely need gpt-5.4 (full model) or domain-specific scaffolding. Some (path-tracing at 0.97, mailman) are close and might pass with more time budget.

### Pattern 5: Flaky Due to Nondeterminism (7 tasks)

Tasks that pass sometimes based on which reasoning path the model takes.

| Task | What varies |
|------|------------|
| count-dataset-tokens | Which fields the model interprets as "deepseek tokens" |
| mteb-retrieve | Whether model discovers BGE query instruction prefix |
| sanitize-git-repo | Whether grep finds HF token embedded in JSON diff blob |
| qemu-alpine-ssh | Serial console timing in QEMU boot sequence |
| llm-inference-batching-scheduler | Optimization quality (8% from threshold) |
| schemelike-metacircular-eval | Implementer quality varies wildly (broken vs slow vs correct) |
| sqlite-with-gcov | Build config variation loses .gcno files |

**Fix:** Most of these need the coordinator to verify more carefully before submitting. For count-dataset-tokens and mteb-retrieve, the model needs to validate its interpretation against multiple methods. For qemu-alpine-ssh, more robust expect scripts.

## Failing Tasks Detail

### 0/3 Tasks (26 total)

| Task | Classification | Root Cause |
|------|---------------|------------|
| caffe-cifar-10 | WRONG_APPROACH | Edited solver copy, not original file test validates |
| circuit-fibsqrt | HARD | Can't synthesize boolean arithmetic circuit |
| db-wal-recovery | WRONG_APPROACH | Entered degenerate self-reading loop after finding 5/11 records |
| dna-assembly | HARD | Golden Gate primer design too specialized |
| dna-insert | HARD | SDM primer design too specialized |
| fix-ocaml-gc | HARD | OCaml bootstrap build breaks even with correct fix |
| gcode-to-text | WRONG_APPROACH | Read G-code comments instead of analyzing toolpath geometry |
| gpt2-codegolf | HARD | 5091 bytes (91 over limit); can't get both correct AND small |
| install-windows-3.11 | UNDERSPECIFIED | Socket path not in task description (2.1 fixes) |
| mailman | HARD | Services never started; analysis paralysis |
| make-doom-for-mips | HARD | DOOM doesn't boot in custom VM |
| make-mips-interpreter | HARD | Writing MIPS interpreter from scratch too ambitious |
| mcmc-sampling-stan | WRONG_APPROACH | Suppressed RStan sampling output that test checks for |
| model-extraction-relu-logits | WRONG_APPROACH | Read weights directly instead of black-box extraction |
| mteb-leaderboard | UNDERSPECIFIED | Web lookup returns wrong model; expected answer hardcoded |
| overfull-hbox | WRONG_APPROACH | Invalid synonym substitution (younger→young not in list) |
| path-tracing | HARD | 0.97 similarity, 0.99 threshold too demanding |
| protein-assembly | TIMEOUT | 179 web searches, never wrote output |
| query-optimize | UNDERSPECIFIED + WRONG_APPROACH | SQL rewrite slower than original; no profiling |
| raman-fitting | WRONG_APPROACH | Failed to parse comma-decimal data format |
| sam-cell-seg | WRONG_APPROACH | Positional args; test expects --named_args |
| torch-pipeline-parallelism | HARD | Subtle gradient bugs in distributed AFAB scheduling |
| torch-tensor-parallelism | WRONG_APPROACH | Weight shape convention backwards |
| train-fasttext | TIMEOUT + WRONG_APPROACH | Slow subchar n-grams; never saved model |
| video-processing | HARD | Algorithm overfit to example video |
| write-compressor | HARD | Can't reverse-engineer custom arithmetic coding |

### Flaky Tasks Detail (18 total)

| Task | Score | Classification | Divergence |
|------|-------|---------------|------------|
| configure-git-webserver | 1/3 | STRATEGY_CHOICE | Pass used simple http.server; fails over-engineered |
| count-dataset-tokens | 1/3 | NONDETERMINISTIC | Wrong field interpretation in 2/3 runs |
| git-multibranch | 1/3 | TIMEOUT_MARGIN | SSH auth debugging spiral in fails |
| kv-store-grpc | 1/3 | BUG | Proto field `val` vs `value` |
| llm-inference-batching-scheduler | 1/3 | NONDETERMINISTIC | 8% off optimization threshold |
| mteb-retrieve | 1/3 | NONDETERMINISTIC | Missing BGE query instruction prefix |
| path-tracing-reverse | 1/3 | STRATEGY_CHOICE | Pass: behavioral cloning. Fails: manual decompilation |
| polyglot-rust-c | 1/3 | BUG | C++ compilation failure + leftover scratch files |
| qemu-alpine-ssh | 1/3 | NONDETERMINISTIC | Serial console timing |
| sanitize-git-repo | 1/3 | NONDETERMINISTIC | Misses HF token in JSON diff blob |
| schemelike-metacircular-eval | 1/3 | STRATEGY_CHOICE | Implementer quality varies wildly |
| sqlite-with-gcov | 1/3 | NONDETERMINISTIC | Build config loses .gcno files |
| tune-mjcf | 1/3 | TIMEOUT_MARGIN | Analysis paralysis; never writes file |
| regex-chess | 1/4 | STRATEGY_CHOICE | Regex generator bugs can't recover in time |
| winning-avg-corewars | 1/2 | BUG | Wrong output path |
| prove-plus-comm | 2/2 | RELIABLE | -- |
| qemu-startup | 2/2 | RELIABLE | -- |
| sparql-university | 2/2 | RELIABLE | -- |

## Actionable Fix Priorities

### Tier 1: Easy wins (prompt/coordinator fixes, no code changes)

1. **"Write deliverables before verifying"** — already in core.md but not working for tune-mjcf, protein-assembly, path-tracing-reverse. Needs stronger enforcement or turn budget warnings.
2. **Coordinator verifies paths and interfaces** — would fix caffe-cifar-10 (edited wrong file), winning-avg-corewars (wrong path), polyglot-rust-c (leftover files).
3. **"Use standard conventions"** — would help sam-cell-seg (use --flags), torch-tensor-parallelism (PyTorch weight convention), kv-store-grpc (standard field names).

### Tier 2: Model behavior improvements (prompt tuning)

4. **Don't suppress output** — mcmc-sampling-stan just needs to not use `refresh=0`.
5. **Behavioral cloning over manual decompilation** — path-tracing-reverse pass wrote code and compared output; fails disassembled everything.
6. **Don't cheat** — model-extraction-relu-logits read weights directly instead of black-box extraction.
7. **Analyze data, not metadata** — gcode-to-text read comments instead of toolpath coordinates.

### Tier 3: Environmental / benchmark fixes

8. **Terminal-bench 2.1** — fixes install-windows-3.11, query-optimize, caffe-cifar-10 timeout, sam-cell-seg arg format.
9. **Larger time budgets** — protein-assembly, train-fasttext, regex-chess could pass with more time.

### Tier 4: Genuinely hard (needs better model or domain scaffolding)

10. **Domain expertise** — dna-assembly, dna-insert, fix-ocaml-gc, circuit-fibsqrt need specialized knowledge.
11. **Extreme engineering** — gpt2-codegolf, write-compressor, make-mips-interpreter are at the frontier.
