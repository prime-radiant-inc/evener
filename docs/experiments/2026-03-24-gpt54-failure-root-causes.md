# Root Causes: gpt-5.4 Failures on terminal-bench 2.0

**Date:** March 24, 2026
**Model:** gpt-5.4, reasoning_effort=medium
**Serf commit:** a6a5589

## Executive Summary

19 tasks have at least one failure on gpt-5.4 medium. Of these:
- **3 are deterministic knowledge gaps** — fixable with domain knowledge
- **3 are underspecified/benchmark issues** — fixed in terminal-bench 2.1
- **5 are genuinely hard** — at the frontier of model capability
- **4 are configuration fragility** — nondeterministic, partially improvable
- **4 show clear improvement over mini** — 5.4 uses the right approach but needs more time or polish

## Tier 1: Deterministic Knowledge Gaps (highest ROI)

| Task | Rate | Root Cause | Fix |
|------|------|-----------|-----|
| **mteb-retrieve** | 0/6 | BGE models need Chinese query instruction prefix `"为这个句子生成表示以用于检索相关文章："` | Domain knowledge about embedding model prefixes |
| **mcmc-sampling-stan** | 5/10 | Agent uses `refresh=0` in rstan, suppressing sampling output the test checks | Don't suppress sampling messages |
| **dna-insert** | 1/6 | Agent uses Breslauer Tm method; test uses SantaLucia (oligotm default) | Specify SantaLucia Tm calculation |

**Expected impact if fixed:** mteb-retrieve 0→6/6, mcmc-sampling-stan 5→9/10, dna-insert 1→5/6.

## Tier 2: Benchmark Issues (terminal-bench 2.1 fixes)

| Task | Rate | Root Cause | Status |
|------|------|-----------|--------|
| **install-windows-3.11** | 0/13 | Socket path `/tmp/qemu-monitor.sock` not in description | 2.1 PR adds it |
| **sam-cell-seg** | 0/3 | Task says positional args; test uses `--named_args` | 2.1 PR fixes arg format |
| **train-fasttext** | 0/4 | 3600s timeout consumed by hyperparameter search | 2.1 changes test to CLI evaluation |

## Tier 3: 5.4 Gets Close (improvement over mini)

| Task | mini | 5.4 | What changed | What's left |
|------|------|-----|-------------|-------------|
| **model-extraction-relu-logits** | 0/3 (cheated) | 0/3 (correct approach!) | Uses black-box kink detection instead of reading weights | Scout reports wrong dimensions (20 vs 30 neurons) |
| **torch-tensor-parallelism** | 0/3 | 0/5 (5/13 tests pass) | Column-parallel works; row-parallel weight slicing wrong | Fix row-parallel scatter logic |
| **make-mips-interpreter** | 0/3 | 0/7 (2/3 tests pass in 1 run) | Wrote working Python interpreter; pyelftools missing at test time | Rewrite in pure Node.js |
| **video-processing** | 0/2 | 0/3 (4/5 tests pass) | Example video works perfectly | Algorithm doesn't generalize to hidden test video |

## Tier 4: Genuinely Hard

| Task | Rate | Why it's hard |
|------|------|--------------|
| **dna-assembly** | 0/3 | Golden Gate primer design with circular plasmid junctions, Tm constraints |
| **make-doom-for-mips** | 0/3 | Cross-compile DOOM for custom MIPS VM; 900s too short |
| **torch-pipeline-parallelism** | 0/3 | Subtle gradient accumulation bugs in distributed AFAB scheduling |
| **raman-fitting** | 0/5 | Can't determine laser wavelength for nm→cm⁻¹ conversion |
| **path-tracing** | 2/3 | 0.97→0.99 similarity gap is very demanding |

## Tier 5: Configuration Fragility

| Task | Rate | Issue |
|------|------|-------|
| **git-multibranch** | 4/10 | Post-receive hook race conditions + SSH auth flakiness |
| **configure-git-webserver** | 4/8 | nginx + hook config varies per run |
| **gcode-to-text** | 3/26 | Vision task done without vision; passes are lucky convergences |
| **caffe-cifar-10** | 1/3 | Building Caffe is slow; timeout-dependent |

## Key Insight: 5.4 vs Mini Failure Modes

For 4 tasks where mini took the WRONG_APPROACH:
- **model-extraction:** 5.4 uses the CORRECT approach (black-box extraction) vs mini which cheated. Fails only because scout reports wrong network dimensions.
- **torch-tensor-parallelism:** 5.4 gets column-parallel right. Mini couldn't do either.
- **sam-cell-seg:** Both use positional args (benchmark bug, not model capability).
- **raman-fitting:** Both fail on data interpretation (same root cause).

The full model shows genuine reasoning improvement on algorithmic tasks, but gets tripped up by environment details (wrong scout data, missing dependencies, test interface mismatches).
