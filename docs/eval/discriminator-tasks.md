# Discriminator Task Set

## What are discriminator tasks?

Terminal-bench 2.0 contains 89 tasks. Not all tasks are equally useful for
evaluating agent quality:

- **Too hard** (>75% failure rate): No current agent reliably passes these.
  Running them wastes compute without providing signal.
- **Too easy** (<10% failure rate): Every agent passes these. They inflate
  scores without differentiating strong agents from weak ones.
- **Discriminators** (10-75% failure rate): These tasks actually separate
  strong agents from weak ones. An agent's score on this subset is a
  meaningful indicator of capability.

## Methodology

Analysis of the terminal-bench 2.0 public leaderboard as of March 2026:

- **27** agent/model submissions
- **10,947** total trials
- Failure rate computed per-task across all submissions
- Top agent: Droid/GPT-5.3-Codex at 77.3%

Source: https://gist.github.com/simonw/3bff274abcbbbf8766e9437a542db248

## Task tiers

### Too hard (18 tasks, >75% failure)

make-doom-for-mips, sam-cell-seg, install-windows-3.11, caffe-cifar-10,
filter-js-from-html, gpt2-codegolf, extract-moves-from-video, raman-fitting,
train-fasttext, mteb-retrieve, video-processing, torch-tensor-parallelism,
dna-assembly, db-wal-recovery, torch-pipeline-parallelism, dna-insert,
mteb-leaderboard, model-extraction-relu-logits

### Discriminators (56 tasks, 10-75% failure)

Listed by failure rate (hardest first):

| Task | Failure rate |
|------|-------------|
| make-mips-interpreter | 75.4% |
| gcode-to-text | 74.6% |
| regex-chess | 70.1% |
| polyglot-c-py | 65.0% |
| polyglot-rust-c | 63.4% |
| query-optimize | 61.3% |
| path-tracing | 59.3% |
| adaptive-rejection-sampler | 59.0% |
| qemu-alpine-ssh | 57.4% |
| path-tracing-reverse | 54.5% |
| protein-assembly | 52.9% |
| chess-best-move | 52.8% |
| write-compressor | 49.6% |
| configure-git-webserver | 47.1% |
| tune-mjcf | 46.3% |
| winning-avg-corewars | 45.9% |
| cancel-async-tasks | 44.7% |
| financial-document-processor | 43.9% |
| overfull-hbox | 43.7% |
| sanitize-git-repo | 43.4% |
| extract-elf | 43.0% |
| schemelike-metacircular-eval | 39.5% |
| compile-compcert | 37.0% |
| feal-linear-cryptanalysis | 36.4% |
| circuit-fibsqrt | 35.9% |
| break-filter-js-from-html | 33.7% |
| sparql-university | 30.9% |
| largest-eigenval | 30.1% |
| build-pmars | 29.3% |
| mailman | 29.2% |
| large-scale-text-editing | 27.7% |
| bn-fit-modify | 27.6% |
| qemu-startup | 27.6% |
| rstan-to-pystan | 26.9% |
| build-cython-ext | 23.6% |
| password-recovery | 23.6% |
| pytorch-model-cli | 23.6% |
| feal-differential-cryptanalysis | 23.3% |
| count-dataset-tokens | 23.1% |
| sqlite-db-truncate | 22.9% |
| llm-inference-batching-scheduler | 21.9% |
| reshard-c4-data | 20.8% |
| mcmc-sampling-stan | 20.7% |
| fix-ocaml-gc | 20.0% |
| openssl-selfsigned-cert | 19.5% |
| sqlite-with-gcov | 18.7% |
| pytorch-model-recovery | 18.5% |
| build-pov-ray | 17.4% |
| crack-7z-hash | 17.1% |
| kv-store-grpc | 15.4% |
| hf-model-inference | 14.9% |
| headless-terminal | 14.8% |
| merge-diff-arc-agi-task | 12.3% |
| pypi-server | 11.4% |
| regex-log | 11.4% |
| fix-code-vulnerability | 9.0% |

### Too easy (15 tasks, <10% failure)

git-leak-recovery, cobol-modernization, constraints-scheduling, fix-git,
nginx-request-logging, vulnerable-secret, portfolio-optimization,
custom-memory-heap-crash, multi-source-data-merger, prove-plus-comm,
modernize-scientific-stack, log-summary-date-ranges, code-from-image,
distribution-search, git-multibranch

## Usage

```bash
# Run discriminators with lace, 3 reps
./tools/run_eval.py launch --harness lace --task discriminators --reps 3

# Dry run to see the harbor command
./tools/run_eval.py launch --harness lace --task discriminators --reps 3 --dry-run

# Mix named sets with individual tasks
./tools/run_eval.py launch --task discriminators --task git-leak-recovery --reps 1

# List available named sets
./tools/run_eval.py launch --list-tasks
```

Task set definitions are in `tools/task_sets.py`.
