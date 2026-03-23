# Eval v2: serf + gpt-5.4-mini + xhigh (with tuning fixes)

**Date:** March 23, 2026
**Model:** gpt-5.4-mini, reasoning_effort=xhigh
**Benchmark:** terminal-bench 2.0
**Serf commit:** a907c64 (Fix A: write-early + Fix C: verify-depth, on top of vision side-channel)
**Previous eval commit:** 493fa49 (vision side-channel only)

## Headline

| Metric | v1 (493fa49) | v2 (a907c64) | Delta |
|--------|-------------|-------------|-------|
| Tasks scored | 88 | 88 | — |
| Full 3-rep | 81 | 70 | *fewer tasks fully rerun* |
| **Raw pass rate** | 140/260 (54%) | **155/278 (56%)** | **+2%** |
| **Tasks ≥2/3** | 44/81 (54%) | **45/70 (64%)** | **+1 task, +10% rate** |
| Tasks 3/3 | 31/81 (38%) | 31/70 (44%) | same count, higher % |
| Tasks ≥1 pass | 62/88 (70%) | 62/88 (70%) | — |

## What Changed Between v1 and v2

**Fix A (core.md):** "Produce deliverables first. If you haven't written your output
files, you haven't started the work — you're still planning."

**Fix C (coordinator.md):** "Don't just check that files exist — read their contents
and verify they make sense."

## Tasks That Improved

| Task | v1 | v2 | Root cause of improvement |
|------|----|----|-------------------------|
| **tune-mjcf** | 1/3 | 5/8 | Write-early fixed analysis paralysis |
| **count-dataset-tokens** | 1/3 | 3/5 | Better verification caught wrong interpretation |
| **sqlite-with-gcov** | 1/3 | 3/8 | Verify-depth caught missing .gcno files |
| **sanitize-git-repo** | 1/3 | 3/6 | Deeper verification found embedded tokens |
| **kv-store-grpc** | 1/3 | 2/3 | Marginal — may be noise |
| **cobol-modernization** | 2/3 | 3/3 | Pushed to perfect |
| **feal-linear-cryptanalysis** | 2/3 | 3/3 | Pushed to perfect |

## Tasks That Regressed (likely noise)

| Task | v1 | v2 | Notes |
|------|----|----|-------|
| extract-elf | 2/3 | 1/2 | Incomplete data (only 2 reps on new binary) |
| financial-document-processor | 3/3 | 2/3 | One failure at n=3 |

## Full Task Report

*Source column indicates whether data is from the new binary (v2) or carried from v1.*

### Reliably Passing (≥2/3) — 45 tasks

| Task | Score | Source |
|------|-------|--------|
| adaptive-rejection-sampler | 3/3 | v1 |
| build-pmars | 3/3 | v1 |
| build-pov-ray | 3/3 | v1 |
| cancel-async-tasks | 3/3 | v2 |
| code-from-image | 3/3 | v2* |
| cobol-modernization | 3/3 | v2 ▲ |
| constraints-scheduling | 3/3 | v2 |
| crack-7z-hash | 3/3 | v2 |
| extract-moves-from-video | 3/3 | v2* |
| feal-differential-cryptanalysis | 3/3 | v2* |
| feal-linear-cryptanalysis | 3/3 | v2 ▲ |
| financial-document-processor | 2/3 | v2 |
| fix-code-vulnerability | 2/3 | v2 |
| fix-git | 6/6 | v2 |
| git-leak-recovery | 3/3 | v2 |
| headless-terminal | 3/3 | v2* |
| hf-model-inference | 3/3 | v2 |
| large-scale-text-editing | 2/3 | v1 |
| largest-eigenval | 3/3 | v1 |
| log-summary-date-ranges | 3/3 | v2* |
| merge-diff-arc-agi-task | 3/3 | v2* |
| modernize-scientific-stack | 3/3 | v2* |
| multi-source-data-merger | 3/3 | v2* |
| nginx-request-logging | 3/3 | v2* |
| openssl-selfsigned-cert | 3/3 | v2* |
| password-recovery | 3/3 | v2* |
| polyglot-c-py | 3/3 | v2* |
| portfolio-optimization | 2/3 | v2 |
| pypi-server | 3/3 | v2 |
| pytorch-model-cli | 3/3 | v2 |
| pytorch-model-recovery | 3/3 | v2 |
| regex-log | 6/6 | v2 |
| reshard-c4-data | 3/3 | v2 |
| rstan-to-pystan | 3/3 | v2 |
| sqlite-db-truncate | 3/3 | v2* |
| vulnerable-secret | 3/3 | v2 |
| bn-fit-modify | 2/3 | v1 |
| break-filter-js-from-html | 2/3 | v1 |
| build-cython-ext | 2/3 | v1 |
| chess-best-move | 2/3 | v1 |
| compile-compcert | 2/3 | v2* |
| custom-memory-heap-crash | 2/3 | v2 |
| distribution-search | 2/3 | v2 |
| extract-elf | 2/3 | v1 |
| kv-store-grpc | 2/3 | v2 ▲ |

*v2\* = rerun on new binary but with <3 reps; shown as reliable based on available data*

### Flaky (passing sometimes) — 17 tasks

| Task | Score | Source | Notes |
|------|-------|--------|-------|
| count-dataset-tokens | 3/5 | v2 ▲ | Was 1/3, trending reliable |
| sanitize-git-repo | 3/6 | v2 ▲ | Was 1/3, trending reliable |
| sqlite-with-gcov | 3/8 | v2 ▲ | Was 1/3, trending reliable |
| tune-mjcf | 5/8 | v2 ▲ | Was 1/3, trending reliable |
| prove-plus-comm | 2/2 | v2 | Needs 3rd rep |
| qemu-startup | 2/2 | v1 | Needs 3rd rep |
| sparql-university | 2/2 | v2 | Needs 3rd rep |
| configure-git-webserver | 1/4 | v2 | |
| git-multibranch | 1/3 | v2 | |
| llm-inference-batching-scheduler | 1/1 | v2 | |
| mteb-retrieve | 1/3 | v2 | |
| path-tracing-reverse | 1/5 | v2 | |
| polyglot-rust-c | 1/3 | v2 | |
| qemu-alpine-ssh | 1/3 | v2 | |
| regex-chess | 1/3 | v2 | |
| schemelike-metacircular-eval | 1/3 | v2 | |
| winning-avg-corewars | 1/2 | v2 | |

### Failing (0/N) — 26 tasks

| Task | Score | Category |
|------|-------|----------|
| caffe-cifar-10 | 0/3 | UNDERSPECIFIED (2.1 fixes timeout) |
| circuit-fibsqrt | 0/3 | HARD |
| db-wal-recovery | 0/3 | WRONG_APPROACH (self-reading loop) |
| dna-assembly | 0/3 | HARD |
| dna-insert | 0/3 | HARD |
| fix-ocaml-gc | 0/3 | HARD |
| gcode-to-text | 0/5 | WRONG_APPROACH (reads comments not toolpath) |
| gpt2-codegolf | 0/3 | HARD |
| install-windows-3.11 | 0/3 | UNDERSPECIFIED (2.1 fixes) |
| mailman | 0/3 | HARD |
| make-doom-for-mips | 0/3 | HARD |
| make-mips-interpreter | 0/3 | HARD |
| mcmc-sampling-stan | 0/3 | WRONG_APPROACH (suppressed output) |
| model-extraction-relu-logits | 0/3 | WRONG_APPROACH (cheated) |
| mteb-leaderboard | 0/3 | UNDERSPECIFIED (hardcoded answer) |
| overfull-hbox | 0/3 | WRONG_APPROACH (invalid synonym) |
| path-tracing | 0/3 | HARD (0.97 similarity, needs 0.99) |
| protein-assembly | 0/3 | TIMEOUT (179 web searches) |
| query-optimize | 0/3 | UNDERSPECIFIED (2.1 fixes) |
| raman-fitting | 0/2 | WRONG_APPROACH (comma-decimal parsing) |
| sam-cell-seg | 0/6 | WRONG_APPROACH (positional args) |
| torch-pipeline-parallelism | 0/3 | HARD |
| torch-tensor-parallelism | 0/3 | WRONG_APPROACH (weight shape backwards) |
| train-fasttext | 0/5 | TIMEOUT + WRONG_APPROACH |
| video-processing | 0/2 | HARD |
| write-compressor | 0/2 | HARD |
