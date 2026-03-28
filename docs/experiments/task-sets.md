# Task Sets

## Regression set

Must keep passing after every change. Run at 1 rep each alongside target task experiments.

- sanitize-git-repo
- feal-linear-cryptanalysis
- winning-avg-corewars
- kv-store-grpc
- build-pov-ray
- regex-log
- pypi-server
- adaptive-rejection-sampler
- distribution-search

**Note:** adaptive-rejection-sampler is nondeterministic (~50% pass rate). Don't
block a ship decision on it alone. If it fails but everything else passes, that's
expected noise.

## mteb-retrieve target

The specific task used for iterating on implementer research behavior. The
implementer must read the BGE model README to discover a required Chinese query
prefix for retrieval. Without prefix: 5th result = HumanEval (wrong). With prefix:
5th result = MTEB (correct).

## Historically hard tasks

These 16 tasks have >75% failure rate across all 27 agent/model submissions on the
public leaderboard. Still run them in full baselines — any pass is signal.

- dna-assembly
- make-doom-for-mips
- sam-cell-seg
- install-windows-3.11
- caffe-cifar-10
- filter-js-from-html
- gpt2-codegolf
- extract-moves-from-video
- raman-fitting
- train-fasttext
- video-processing
- torch-tensor-parallelism
- db-wal-recovery
- torch-pipeline-parallelism
- dna-insert
- mteb-leaderboard

## Programmatic definitions

See `tools/task_sets.py` for the full programmatic task set definitions used by
`run_eval.py`. Available sets:

- `discriminators` — 56 tasks with 10-75% failure rate (the useful signal range)
- `solvable` — 72 tasks with ≤75% failure rate

```bash
./tools/run_eval.py launch --list-tasks  # Show available named sets
```
