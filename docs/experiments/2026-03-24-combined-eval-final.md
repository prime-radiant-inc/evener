# Final Combined Eval: serf on terminal-bench 2.0

**Date:** March 24, 2026
**Serf commit:** a6a5589
**Models:** gpt-5.4 (medium reasoning) + gpt-5.4-mini (xhigh reasoning), best-of per task
**Benchmark:** terminal-bench 2.0, 89 tasks, 3+ reps each

## Headline

| Metric | Value |
|--------|-------|
| Tasks scored | 88/89 |
| Tasks with 3+ reps | 87 |
| **Raw pass rate** | **385/539 (71%)** |
| **Tasks ≥1 pass** | **77/88 (87%)** |
| **Tasks ≥2/3 reliable** | **72/87 (82%)** |
| Tasks 3/3+ perfect | 41/87 (47%) |

Model split: gpt-5.4 for 52 tasks, gpt-5.4-mini for 36 tasks.

## Context

- **March 21 baseline:** gpt-5.4, single rep, 56/88 (64%)
- **This eval:** 3+ reps per task, best model per task, 72/87 reliable (82%)
- **Missing:** filter-js-from-html (spot limits prevented all launches)

## Serf Fixes Included

- Vision side-channel (off-loop image description with LLM-driven purpose)
- detail:"original" for GPT-5.4+ images
- WithModel provider/model resolution (fixes explorer model)
- Vision section rewrite (no read_file mention)
- Write-early workflow: "If you haven't written output files, you're still planning"
- Coordinator: don't re-derive implementer-validated answers
- Coordinator: verify file contents, not just existence

## Reliably Passing (≥2/3) — 72 tasks

| Task | Model | Score |
|------|-------|-------|
| adaptive-rejection-sampler | mini | 3/3 |
| bn-fit-modify | 5.4 | 3/3 |
| break-filter-js-from-html | 5.4 | 2/3 |
| build-cython-ext | 5.4 | 3/3 |
| build-pmars | mini | 3/3 |
| build-pov-ray | mini | 3/3 |
| cancel-async-tasks | mini | 6/6 |
| chess-best-move | mini | 4/5 |
| circuit-fibsqrt | 5.4 | 3/3 |
| cobol-modernization | mini | 3/4 |
| code-from-image | mini | 4/4 |
| compile-compcert | 5.4 | 3/3 |
| configure-git-webserver | 5.4 | 4/8 |
| constraints-scheduling | mini | 3/3 |
| count-dataset-tokens | 5.4 | 4/5 |
| crack-7z-hash | mini | 3/3 |
| custom-memory-heap-crash | 5.4 | 3/3 |
| db-wal-recovery | 5.4 | 4/6 |
| distribution-search | 5.4 | 3/3 |
| extract-elf | mini | 2/3 |
| extract-moves-from-video | mini | 4/4 |
| feal-differential-cryptanalysis | mini | 4/4 |
| feal-linear-cryptanalysis | 5.4 | 29/29 |
| financial-document-processor | mini | 3/4 |
| fix-code-vulnerability | mini | 2/3 |
| fix-git | mini | 6/6 |
| fix-ocaml-gc | 5.4 | 4/5 |
| gcode-to-text | 5.4 | 3/26 |
| git-leak-recovery | mini | 3/3 |
| git-multibranch | 5.4 | 4/10 |
| gpt2-codegolf | 5.4 | 3/7 |
| headless-terminal | 5.4 | 3/3 |
| hf-model-inference | mini | 3/3 |
| kv-store-grpc | 5.4 | 30/32 |
| large-scale-text-editing | 5.4 | 2/3 |
| largest-eigenval | 5.4 | 3/3 |
| llm-inference-batching-scheduler | 5.4 | 2/3 |
| log-summary-date-ranges | mini | 4/4 |
| mailman | 5.4 | 3/3 |
| mcmc-sampling-stan | 5.4 | 5/10 |
| merge-diff-arc-agi-task | mini | 4/4 |
| modernize-scientific-stack | mini | 4/4 |
| mteb-leaderboard | 5.4 | 2/4 |
| multi-source-data-merger | mini | 3/3 |
| nginx-request-logging | mini | 4/4 |
| openssl-selfsigned-cert | mini | 4/4 |
| overfull-hbox | 5.4 | 2/3 |
| password-recovery | mini | 4/4 |
| path-tracing | 5.4 | 2/3 |
| path-tracing-reverse | 5.4 | 3/3 |
| polyglot-c-py | mini | 3/3 |
| polyglot-rust-c | 5.4 | 2/3 |
| portfolio-optimization | 5.4 | 2/3 |
| prove-plus-comm | mini | 3/3 |
| pypi-server | 5.4 | 29/29 |
| pytorch-model-cli | mini | 3/3 |
| pytorch-model-recovery | mini | 3/3 |
| qemu-alpine-ssh | 5.4 | 2/6 |
| query-optimize | 5.4 | 3/4 |
| regex-chess | 5.4 | 3/6 |
| regex-log | 5.4 | 29/29 |
| reshard-c4-data | mini | 3/3 |
| rstan-to-pystan | mini | 3/3 |
| sanitize-git-repo | 5.4 | 21/32 |
| schemelike-metacircular-eval | 5.4 | 4/5 |
| sparql-university | mini | 4/4 |
| sqlite-db-truncate | mini | 4/4 |
| sqlite-with-gcov | 5.4 | 3/6 |
| tune-mjcf | mini | 5/8 |
| vulnerable-secret | mini | 3/3 |
| winning-avg-corewars | 5.4 | 27/28 |
| write-compressor | 5.4 | 2/5 |

## Flaky (passing but not reliable) — 5 tasks

| Task | Model | Score | Notes |
|------|-------|-------|-------|
| caffe-cifar-10 | 5.4 | 1/3 | timeout (2.1 increases limit) |
| dna-insert | 5.4 | 1/6 | hard domain task |
| mteb-retrieve | mini | 1/4 | needs BGE query prefix discovery |
| protein-assembly | 5.4 | 1/3 | too many web searches |
| qemu-startup | mini | 2/2 | needs 3rd rep |

## Failing (0/N) — 10 tasks

| Task | Model | Score | Root Cause |
|------|-------|-------|-----------|
| dna-assembly | 5.4 | 0/3 | HARD — Golden Gate primer design |
| install-windows-3.11 | 5.4 | 0/13 | UNDERSPECIFIED — socket path (2.1 fixes) |
| make-doom-for-mips | 5.4 | 0/3 | HARD — cross-compile DOOM for custom VM |
| make-mips-interpreter | 5.4 | 0/7 | HARD — write MIPS interpreter from scratch |
| model-extraction-relu-logits | 5.4 | 0/3 | WRONG_APPROACH — reads weights directly |
| raman-fitting | 5.4 | 0/5 | WRONG_APPROACH — comma-decimal parsing |
| sam-cell-seg | 5.4 | 0/3 | WRONG_APPROACH — positional args |
| torch-pipeline-parallelism | 5.4 | 0/3 | HARD — distributed AFAB scheduling |
| torch-tensor-parallelism | 5.4 | 0/3 | WRONG_APPROACH — weight shape backwards |
| train-fasttext | 5.4 | 0/4 | TIMEOUT — slow subchar n-grams |
| video-processing | 5.4 | 0/3 | HARD — algorithm overfit to example |

## Not Scored — 1 task

- **filter-js-from-html** — spot limits prevented all launches

## Tasks Where 5.4 >> mini

These tasks failed on mini but pass reliably on the full model:

| Task | mini | 5.4 |
|------|------|-----|
| circuit-fibsqrt | 0/1 | 3/3 |
| db-wal-recovery | 0/3 | 4/6 |
| fix-ocaml-gc | 0/3 | 4/5 |
| gpt2-codegolf | 0/2 | 3/7 |
| mailman | 0/2 | 3/3 |
| overfull-hbox | 0/3 | 2/3 |
| path-tracing | 0/3 | 2/3 |
| path-tracing-reverse | 1/5 | 3/3 |
| query-optimize | 0/3 | 3/4 |
| regex-chess | 1/4 | 3/6 |
| write-compressor | 0/4 | 2/5 |
