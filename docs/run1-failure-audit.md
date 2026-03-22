# Run 1 Failure Audit (3x Full Suite)

Binary: c526807 (decomposition emphasis prompt), Model: gpt-5.2-codex, 100 rounds, 900s timeout

## Scores
- **Run 1**: 40/89 (44.9%) — 10 AgentTimeoutErrors
- **Run 2**: 49/89 (55.1%) — still completing, 2 tasks pending (likely timeout)
- **Run 3**: not started yet

## Stability (Run 1 vs Run 2)
- **35 consistently pass** both runs
- **33 consistently fail** both runs
- **19 nondeterministic** — pass in one run, fail in the other
- **2 pending** in Run 2

### Nondeterministic tasks (19)
R1 FAIL → R2 PASS (14): break-filter-js-from-html, build-cython-ext, cancel-async-tasks,
code-from-image, crack-7z-hash, kv-store-grpc, llm-inference-batching-scheduler,
mcmc-sampling-stan, pypi-server, pytorch-model-cli, pytorch-model-recovery, qemu-alpine-ssh,
query-optimize, regex-log

R1 PASS → R2 FAIL (5): fix-code-vulnerability, git-leak-recovery, password-recovery,
protein-assembly, schemelike-metacircular-eval

---

## 49 Run 1 Failures — Categorized

### TIMEOUT (10) — Agent killed at 900s

| Task | Sessions | Tools | Notes |
|------|----------|-------|-------|
| caffe-cifar-10 | 1 | 75 | Building Caffe from source consumed all time |
| crack-7z-hash | 1 | 55 | Hash cracking inherently slow |
| extract-moves-from-video | 1 | 25 | Video download/processing |
| llm-inference-batching-scheduler | 1 | 81 | Heavy compute (**R2: PASS**) |
| polyglot-rust-c | 1 | 8 | Compilation took too long, only 8 tools used |
| pytorch-model-recovery | 2 | 17 | Likely downloading model (**R2: PASS**) |
| rstan-to-pystan | 1 | 52 | R/Stan compilation |
| sanitize-git-repo | **5** | **100** | 839KB transcripts, massive effort, still timed out |
| train-fasttext | 2 | 30 | Training timeout |
| write-compressor | 1 | 17 | Compute timeout |

### MAX ROUNDS (4) — Used all 100 tool calls without result

| Task | Sessions | Tools | Notes |
|------|----------|-------|-------|
| mailman | 1 | 99 | Complex email server setup, ran out of budget |
| mteb-leaderboard | 1 | 100 | MTEB evaluation pipeline, ran out of budget |
| path-tracing | 1 | 100 | Ray tracer implementation, ran out of budget |
| qemu-alpine-ssh | 1 | 99 | QEMU + SSH setup (**R2: PASS**) |

### GAVE UP / CRASH (2) — Agent quit early

| Task | Sessions | Tools | Issue |
|------|----------|-------|-------|
| **adaptive-rejection-sampler** | **1** | **1** | Listed dir, sent empty result. 4 seq total. CATASTROPHIC. |
| gpt2-codegolf | 1 | 17 | Claimed impossible (missing checkpoint index) |

**adaptive-rejection-sampler detail**: Agent received full task, called `list_dir` once (got
empty result), then immediately called `communicate(result)` with empty message and
`decision: "implemented"`. The directory was empty because the agent was supposed to CREATE
the files. This is likely a model hallucination — treating an empty workspace as "already done."

### CLOSE/PARTIAL (18) — Agent did real work, verifier disagrees

| Task | Sessions | Tools | What Happened |
|------|----------|-------|--------------|
| build-cython-ext | 2 | 99 | Massive Cython/NumPy compat fixes, ran out of rounds (**R2: PASS**) |
| cancel-async-tasks | 1 | 10 | Own tests pass, verifier tests different semantics (**R2: PASS**) |
| db-wal-recovery | 1 | 53 | Recovered 11 records, probably wrong count |
| install-windows-3.11 | 1 | 28 | All setup done, verifier fails on detail |
| kv-store-grpc | 1 | 17 | Server works, verifier tests specific behavior (**R2: PASS**) |
| largest-eigenval | 1 | 29 | Implementation incorrect |
| model-extraction-relu-logits | 1 | 19 | Extraction done but inaccurate |
| overfull-hbox | 1 | 30 | Edited LaTeX but still has overfull boxes |
| pypi-server | 1 | 30 | Setup done, verifier wants specific detail (**R2: PASS**) |
| pytorch-model-cli | 1 | 38 | Built tool, verifier finds issue (**R2: PASS**) |
| query-optimize | 2 | 22 | SQL timed out, couldn't optimize (**R2: PASS**) |
| raman-fitting | 1 | 35 | Fitted but parameters off |
| regex-log | 1 | 10 | Wrote regex but wrong (**R2: PASS**) |
| torch-pipeline-parallelism | 1 | 16 | Verifier fails on implementation detail |
| torch-tensor-parallelism | 1 | 31 | Verifier fails on implementation detail |
| video-processing | 1 | 18 | Jump analysis wrong |
| winning-avg-corewars | 2 | 103 | Massive effort on strategy, still loses |
| break-filter-js-from-html | 3 | 19 | XSS bypass payload didn't survive filter (**R2: PASS**) |

### WRONG APPROACH (11) — Fundamentally wrong solution

| Task | Sessions | Tools | What Went Wrong |
|------|----------|-------|----------------|
| chess-best-move | 1 | 75 | Wrong move (c1g5), burned 75 tools trying |
| code-from-image | 1 | 85 | Couldn't read code from image accurately (**R2: PASS**) |
| compile-compcert | 1 | 89 | Built CompCert but likely wrong location |
| configure-git-webserver | 1 | 10 | Wrote scripts instead of configuring live server |
| dna-assembly | 1 | 60 | Primer design wrong |
| dna-insert | 1 | 27 | SDM primer design wrong |
| filter-js-from-html | 1 | 9 | Sanitization missed edge cases |
| gcode-to-text | 1 | 9 | Read comment label instead of decoding toolpath geometry |
| mteb-retrieve | 1 | 14 | Wrong result |
| path-tracing-reverse | 3 | 77 | Wrong analysis of mystery program |
| regex-chess | 1 | 89 | Regex backreference issues |

### DOMAIN HARD (4) — Specialized knowledge required

| Task | Sessions | Tools | Notes |
|------|----------|-------|-------|
| make-doom-for-mips | 1 | 82 | Cross-compilation, wrote placeholder ELF |
| make-mips-interpreter | **9** | **254** | Heroic effort (9 sessions, 856KB!), still failing |
| mcmc-sampling-stan | 2 | 32 | Linker failed for rstan (**R2: PASS**) |
| sam-cell-seg | 1 | 31 | Cell segmentation domain knowledge |

---

## Key Observations

### 1. Nondeterminism dominates the variance
14 tasks that failed in Run 1 passed in Run 2 with the identical binary. These account for
the 9-point gap between runs (40 vs 49). This means the "real" score for this binary is
somewhere in the 44-55% range, and we need all 3 runs to pin it down.

### 2. The adaptive-rejection-sampler crash is a bug
The agent listed an empty directory and immediately reported success. This may be a model-level
issue (gpt-5.2-codex confusing an empty workspace with "task complete") or possibly a content
filter that silently ate the first response.

### 3. 33 tasks consistently fail — the improvement target
Of these 33:
- 6 timeout (caffe-cifar-10, polyglot-rust-c, rstan-to-pystan, sanitize-git-repo, train-fasttext, write-compressor)
- 4 hit max rounds (mailman, mteb-leaderboard, path-tracing)
- 2 gave up (adaptive-rejection-sampler, gpt2-codegolf)
- 21 are wrong approach / close-but-wrong / domain hard

### 4. make-mips-interpreter is the hardest worker
9 sessions, 254 tool calls, 856KB of transcript. The agent is trying everything but can't
crack it. This is the opposite of the "gave up" pattern — it's persistence without progress.

### 5. Several tasks use surprisingly few tools
configure-git-webserver (10), filter-js-from-html (9), gcode-to-text (9), regex-log (10) —
the agent declares victory too early. These are candidates for prompt improvements around
"run the verifier's tests before declaring done."
