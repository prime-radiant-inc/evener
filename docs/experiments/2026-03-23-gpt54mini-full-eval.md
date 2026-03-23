# Full Eval: serf + gpt-5.4-mini + xhigh reasoning

**Date:** March 23, 2026
**Model:** gpt-5.4-mini, reasoning_effort=xhigh
**Benchmark:** terminal-bench 2.0, 89 tasks × 3 reps
**Serf version:** commit 493fa49 (vision side-channel + all fixes)
**Infrastructure:** AWS spot, m6i.xlarge/m6i.2xlarge, ~250 instances across 4 waves

## Headline

| Metric | Value |
|--------|-------|
| Tasks scored | 88/89 |
| Tasks with 3 reps | 81 |
| **Raw pass rate** | **140/260 (54%)** |
| **Tasks reliably passing (≥2/3)** | **44/81 (54%)** |
| Tasks passing 3/3 | 31/81 (38%) |
| Tasks passing ≥1 | 62/88 (70%) |
| Tasks failing all reps | 26 |

For comparison: gpt-5.4 (full model) scored 56/88 (64%) on a single rep in our March 21 baseline.
gpt-5.4-mini at 54% reliable with 3-rep confirmation is competitive at a fraction of the cost.

## Task Results

### Reliably Passing (≥2/3) — 44 tasks

| Task | Score | Task | Score |
|------|-------|------|-------|
| adaptive-rejection-sampler | 3/3 | build-pmars | 3/3 |
| build-pov-ray | 3/3 | cancel-async-tasks | 3/3 |
| code-from-image | 3/3 | constraints-scheduling | 3/3 |
| crack-7z-hash | 3/3 | extract-moves-from-video | 3/3 |
| feal-differential-cryptanalysis | 3/3 | financial-document-processor | 3/3 |
| fix-git | 3/3 | git-leak-recovery | 3/3 |
| headless-terminal | 3/3 | hf-model-inference | 3/3 |
| largest-eigenval | 3/3 | log-summary-date-ranges | 3/3 |
| merge-diff-arc-agi-task | 3/3 | modernize-scientific-stack | 3/3 |
| multi-source-data-merger | 3/3 | nginx-request-logging | 3/3 |
| openssl-selfsigned-cert | 3/3 | password-recovery | 3/3 |
| polyglot-c-py | 3/3 | pypi-server | 3/3 |
| pytorch-model-cli | 3/3 | pytorch-model-recovery | 3/3 |
| regex-log | 3/3 | reshard-c4-data | 3/3 |
| rstan-to-pystan | 3/3 | sqlite-db-truncate | 3/3 |
| vulnerable-secret | 3/3 | bn-fit-modify | 2/3 |
| break-filter-js-from-html | 2/3 | build-cython-ext | 2/3 |
| chess-best-move | 2/3 | cobol-modernization | 2/3 |
| compile-compcert | 2/3 | custom-memory-heap-crash | 2/3 |
| distribution-search | 2/3 | extract-elf | 2/3 |
| feal-linear-cryptanalysis | 2/3 | fix-code-vulnerability | 2/3 |
| large-scale-text-editing | 2/3 | portfolio-optimization | 2/3 |

### Flaky (1/3 or partial) — 18 tasks

| Task | Score | Notes |
|------|-------|-------|
| prove-plus-comm | 2/2 | needs 3rd rep |
| qemu-startup | 2/2 | needs 3rd rep |
| sparql-university | 2/2 | needs 3rd rep |
| winning-avg-corewars | 1/2 | needs 3rd rep |
| configure-git-webserver | 1/3 | |
| count-dataset-tokens | 1/3 | |
| git-multibranch | 1/3 | |
| kv-store-grpc | 1/3 | |
| llm-inference-batching-scheduler | 1/3 | |
| mteb-retrieve | 1/3 | |
| path-tracing-reverse | 1/3 | |
| polyglot-rust-c | 1/3 | |
| qemu-alpine-ssh | 1/3 | |
| sanitize-git-repo | 1/3 | |
| schemelike-metacircular-eval | 1/3 | |
| sqlite-with-gcov | 1/3 | |
| tune-mjcf | 1/3 | |
| regex-chess | 1/4 | |

### Failing (0/N) — 26 tasks

| Task | Score | Category |
|------|-------|----------|
| caffe-cifar-10 | 0/3 | timeout (2.1 increases limit) |
| circuit-fibsqrt | 0/3 | |
| db-wal-recovery | 0/3 | |
| dna-assembly | 0/3 | |
| dna-insert | 0/3 | |
| fix-ocaml-gc | 0/3 | |
| gcode-to-text | 0/3 | |
| gpt2-codegolf | 0/3 | |
| install-windows-3.11 | 0/3 | underspecified (2.1 fixes) |
| mailman | 0/3 | |
| make-doom-for-mips | 0/3 | |
| make-mips-interpreter | 0/3 | |
| mcmc-sampling-stan | 0/3 | |
| model-extraction-relu-logits | 0/3 | |
| mteb-leaderboard | 0/3 | |
| overfull-hbox | 0/3 | |
| path-tracing | 0/3 | |
| protein-assembly | 0/3 | |
| query-optimize | 0/3 | underspecified (2.1 fixes) |
| raman-fitting | 0/2 | |
| sam-cell-seg | 0/3 | |
| torch-pipeline-parallelism | 0/3 | |
| torch-tensor-parallelism | 0/3 | |
| train-fasttext | 0/5 | |
| video-processing | 0/2 | |
| write-compressor | 0/2 | |

## Key Observations

1. **Vision side-channel works on gpt-5.4-mini.** chess-best-move passes 2/3 — the
   architecture generalizes across model sizes.

2. **31 tasks at 3/3** is a rock-solid core. These are tasks where gpt-5.4-mini +
   serf reliably solves the problem every time.

3. **Several failing tasks are known benchmark issues** being fixed in terminal-bench
   2.1: caffe-cifar-10 (timeout), install-windows-3.11 (underspecified socket path),
   query-optimize (underspecified DB constraint).

4. **The flaky tier (18 tasks at 1/3)** represents opportunities. These tasks are
   solvable but unreliable — root-causing the failures could move them to reliable.

5. **gpt-5.4-mini at 54% reliable compares well to gpt-5.4 at 64% single-rep.**
   The mini model is substantially cheaper. With terminal-bench 2.1 fixes, several
   failing tasks should move to passing.

## Serf Fixes Included in This Run

- Vision side-channel (off-loop image description with LLM-driven purpose)
- detail:"original" for GPT-5.4+ images
- WithModel provider/model resolution
- Vision section rewrite (no read_file mention)
- "Do the work, then verify" workflow guidance
- Coordinator: don't re-derive implementer-validated answers
- Parent directory tree in system prompt (shows sibling dirs)
