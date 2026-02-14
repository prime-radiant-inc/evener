# RLM Context Strategy Evaluation Report

**Date:** 2026-02-14
**Author:** Bot (Claude Opus 4.6)
**Project:** Recursive Language Model — Context Management Strategies for Serf

## 1. Executive Summary

We evaluated four context management strategies for Serf, an autonomous coding agent operating under LLM context window constraints. The strategies manage information retention as the agent's conversation history grows beyond the context window and must be compressed.

**Key findings (updated with rigorous N=5 evaluation):**

1. **Compact is the recommended default strategy.** In rigorous testing (5 SWE-bench tasks × 4 strategies × 5 runs = 98 trials), compact achieved the highest mean retention score (0.746 ± 0.018) while consuming the fewest tokens. No other strategy significantly outperformed it (all p > 0.34).

2. **The initial N=1 results (Sections 3-4) were misleading.** The preliminary finding that recall outperformed compact did not replicate. With proper statistical power (N=5), the apparent advantages of recall, session-log, and OODA disappeared into noise.

3. **More sophisticated strategies add 24-76% token overhead** without measurably improving retention. The "compaction cascade effect" — where per-turn overhead triggers more compaction, which loses more information — negates the benefit of the overhead.

4. **OODA is the worst performer** — lowest retention (0.730), highest token cost (2.1M mean), most compaction events (187 mean), zero task wins out of 5.

5. **Task-specific variance exists** but doesn't change the overall picture. Recall won on xarray, session-log won on sympy, but compact won 3/5 tasks and had the tightest confidence intervals.

## 2. Methodology

### 2.1 Strategies Under Test

| Strategy | Description | Overhead |
|----------|-------------|----------|
| **compact** (baseline) | 4-layer compaction: observation masking → thinking clear → checkpoint → summarize | None beyond compaction |
| **recall** | compact + recall sub-agent tool for searching compacted history | Near-zero (tool available but rarely called) |
| **session-log** | compact + fork summarization per turn + session-log checkpoint (replaces L3) + recall tool | High: one LLM call per turn for fork summary |
| **ooda** | session-log + Orient-phase injection of session log before each LLM call | Highest: fork summary + orient message per turn |

### 2.2 Compaction Layers

Compaction fires when context usage crosses configurable thresholds (defaults: 60%/70%/80%/90% of context window):

- **L1 (Observation Mask):** Hides large tool output behind summaries
- **L2 (Thinking Clear):** Removes model's internal reasoning traces
- **L3 (Checkpoint):** Replaces conversation prefix with a deterministic summary (or session-log summary for session-log/ooda strategies)
- **L4 (Summarize):** Calls LLM to summarize remaining context

### 2.3 Evaluation Harness

`serfeval` runs the agent on a task, then asks retention probe questions scored by a judge model on a 0-5 scale. Retention score = mean(probe_scores) / 5.

### 2.4 Threshold Scaling

`--threshold-scale` multiplies all compaction thresholds to induce compaction earlier. Scale 0.15 means L1 fires at 9% of context window instead of 60%.

### 2.5 Task Selection

**Phase 4 — Retention test (gpt-4.1-mini, scale 0.05):**
- Simple bug fix task (DB_PORT config issue), designed to test retention under extreme compaction

**Phase 5 — Synthetic (gpt-5.2, scale 0.3):**
- Task A: Multi-file Python web app (10 implementation steps)
- Task B: Go calculator package (9 implementation steps)
- Task C: Python bug investigation (3 planted bugs)

**SWE-bench — Real-world (gpt-5.2, scale 0.15):**
- django-16560: Add `violation_error_code` to constraint validation (1-4 hour difficulty)
- django-11885: Combine fast delete queries (1-4 hour difficulty)
- pytest-5787: Chained exception serialization (1-4 hour difficulty)

## 3. Results

### 3.1 Phase 4: Extreme Compaction (gpt-4.1-mini, scale 0.05)

| Strategy | Turns | Tokens | Compactions | Retention |
|----------|------:|-------:|------------:|----------:|
| compact  |    25 | 135,246 |          33 |      0.60 |
| recall   |    19 | 101,755 |          23 |      0.60 |
| session-log | 11 |  53,526 |          10 |      0.80 |
| ooda     |     9 |  39,382 |           6 |      0.80 |

**Observation:** Session-log and OODA both achieved 33% higher retention while using 60-71% fewer tokens. Under extreme compaction, the session-log checkpoint and orient-phase injection preserve critical information that deterministic compaction loses.

### 3.2 Phase 5: Synthetic Tasks (gpt-5.2, scale 0.3)

#### Task A — Web App (only task with compaction)

| Strategy | Turns | Tokens | Compactions | Retention |
|----------|------:|-------:|------------:|----------:|
| compact  |    31 | 553,881 |           6 |      0.80 |
| recall   |    27 | 439,370 |           0 |      0.80 |
| session-log | 25 | 446,155 |           5 |      0.85 |
| ooda     |    23 | 366,463 |           0 |      0.75 |

#### Tasks B+C — No Compaction Triggered

| Strategy | Avg Turns | Avg Tokens | Avg Retention |
|----------|----------:|-----------:|--------------:|
| compact  |      18.0 |    252,064 |          0.83 |
| recall   |      25.0 |    384,264 |          0.85 |
| session-log | 22.0 |    315,605 |          0.83 |
| ooda     |      20.0 |    284,304 |          0.85 |

**Observation:** Without compaction pressure, all strategies performed similarly (0.75-0.90). The 128k context window was large enough for these synthetic tasks at scale 0.3.

### 3.3 SWE-bench: Real-World Tasks (gpt-5.2, scale 0.15)

#### django-16560 — violation_error_code (complex, 88+ turns)

| Strategy | Turns | Tokens | Compactions | Layers Hit | Retention |
|----------|------:|-------:|------------:|------------|----------:|
| compact  |    88 | 1,304,625 |     96 | L1+L2+L3 |      0.60 |
| recall   |    55 |   751,211 |     51 | L1+L2+L3+L4 |  **0.85** |
| session-log | 69 | 1,007,027 |     70 | L1+L2 |      0.80 |
| ooda     |    90 | 1,427,451 |    110 | L1+L2+L3 |  **0.85** |

#### django-11885 — fast delete combining (simpler, 25+ turns)

| Strategy | Turns | Tokens | Compactions | Retention |
|----------|------:|-------:|------------:|----------:|
| compact  |    25 |   277,912 |     12 |      0.65 |
| recall   |    92 | 1,710,387 |    117 |      0.70 |
| session-log | 38 |   496,768 |     32 |      0.60 |
| ooda     |    95 | 1,532,593 |    119 |      0.65 |

#### pytest-5787 — chained exception serialization (complex, 83+ turns)

| Strategy | Turns | Tokens | Compactions | Retention |
|----------|------:|-------:|------------:|----------:|
| compact  |    93 | 1,287,323 |     82 |      0.50 |
| recall   |    45 |   608,130 |     37 |      0.55 |
| session-log | 200 | 3,405,222 |    288 |      0.75 |
| ooda     |    83 | 1,251,843 |     94 |      0.60 |

## 4. Analysis

### 4.1 Retention Under Compaction Pressure

Averaging across the two complex SWE-bench tasks (django-16560, pytest-5787) where compaction was heavy:

| Strategy | Avg Retention | Avg Tokens | Δ Retention vs Compact | Δ Tokens vs Compact |
|----------|:------------:|:---------:|:---------------------:|:-------------------:|
| compact  | 0.55 | 1,295,974 | baseline | baseline |
| recall   | **0.70** | 679,671 | **+27%** | **-48%** |
| session-log | 0.78 | 2,206,125 | +42% | +70% |
| ooda     | 0.73 | 1,339,647 | +33% | +3% |

**Recall** achieved the best cost/retention tradeoff: 27% higher retention at 48% lower token cost than compact.

**Session-log** achieved the highest raw retention (0.78) but at enormous cost — 70% more tokens than compact. The runaway on pytest-5787 (200 turns, 3.4M tokens) shows the fork summarization overhead compounding dangerously with compaction.

**OODA** achieved good retention improvement (+33%) at essentially the same token cost as compact. The orient-phase injection helps the model maintain coherence without the runaway risk of session-log.

### 4.2 The Recall Efficiency Paradox

Recall was both more efficient AND more effective than compact on complex tasks. This seems counterintuitive — adding a tool should cost more, not less. The explanation:

1. **Recall prevents repetitive exploration.** After compaction removes context, compact-strategy agents often re-read files or repeat work they've already done. Recall lets the agent quickly recover that information instead.
2. **Fewer turns = less compaction = less information loss.** The virtuous cycle: better information retrieval → fewer wasted turns → less context growth → less compaction → more information preserved.
3. **Recall was rarely called** (recall_calls = 0 in most runs). Its benefit came from the *possibility* of recall changing the agent's behavior — the model may plan differently knowing it can recover lost context.

### 4.3 The Session-Log Runaway Problem

On pytest-5787, session-log spiraled: fork summarization → more tokens per turn → faster compaction → more turns needed → more fork summarization. At 200 turns and 3.4M tokens, it consumed 2.6x more than compact while still achieving better retention (0.75 vs 0.50).

The root cause: fork summarization adds a full LLM call per turn. Under aggressive compaction (scale 0.15), this overhead fills the context window faster, triggering more compaction, which requires more turns to complete the task.

**Mitigation options:**
- Reduce fork summarization frequency (every N turns instead of every turn)
- Cap total turns or token budget
- Use a cheaper/faster model for fork summarization
- Only enable fork summarization after first compaction event

### 4.4 Task Complexity Matters

| Task Type | Compaction Pressure | Strategy Differentiation |
|-----------|:-------------------:|:------------------------:|
| Simple (django-11885, Phase 5 Tasks B/C) | Low-Medium | Minimal |
| Complex (django-16560, pytest-5787) | High | Strong |
| Extreme (Phase 4, scale 0.05) | Very High | Strong |

Strategies only meaningfully differentiate under compaction pressure. For tasks that fit within the context window with room to spare, the overhead of session-log and OODA is pure cost with no retention benefit.

### 4.5 Compaction Layer Progression

Across SWE-bench runs, the compaction layer distribution shows how different strategies interact with the compaction cascade:

| Strategy | L1 (Obs Mask) | L2 (Think Clear) | L3 (Checkpoint) | L4 (Summarize) |
|----------|:------------:|:-----------------:|:---------------:|:--------------:|
| compact | 64% | 19% | 1% | 0% |
| recall | 53% | 24% | 19% | 4% |
| session-log | 67% | 17% | 0% | 0% |
| ooda | 60% | 20% | 4% | 0% |

Session-log replaces L3 with its own checkpoint mechanism, preventing the deterministic checkpoint from firing. This is by design — session-log checkpoints contain richer information. However, they also take up more space, contributing to the runaway problem.

## 5. Preliminary Recommendations (Superseded)

> **Note:** The recommendations below were based on the N=1 data in Sections 3-4. They have been **superseded** by the rigorous evaluation in Section 8. See Section 9 for current recommendations.

### 5.1 Strategy Selection (Original)

| Scenario | Recommended Strategy | Rationale |
|----------|---------------------|-----------|
| General use | **recall** | Best cost/retention tradeoff, no overhead penalty |
| Token budget constrained | **recall** | Fewer tokens than any other strategy including compact |
| Maximum retention needed | **ooda** | Near-recall retention without runaway risk |
| Short tasks (<30 turns) | **compact** | Minimal compaction; overhead not justified |
| Research/exploration | **session-log** with turn cap | Best raw retention when cost is secondary |

## 6. Limitations of Initial Evaluation

1. **Small sample size:** 3 SWE-bench tasks × 1 run per condition. Results should be treated as directional, not statistically significant.
2. **Artificial compaction pressure:** `--threshold-scale 0.15` forces early compaction. Real-world behavior with default thresholds may differ.
3. **Single model per phase:** Results with gpt-5.2 may not generalize to other models.
4. **Retention probes are noisy:** 4 questions per task, judged by LLM. Probe quality varies.
5. **No correctness evaluation:** We measured task completion (binary) and retention, but not solution quality against gold patches.

## 7. Initial Raw Data Summary

### Total API Cost Estimate (Phases 1-5 + Initial SWE-bench)

| Phase | Runs | Total Tokens | Est. Cost (@ $10/M input, $30/M output) |
|-------|-----:|-------------:|-----------------------------------------:|
| Phase 4 (gpt-4.1-mini) | 4 | 329,909 | ~$3 |
| Phase 5 Synthetic (gpt-5.2) | 12 | 4,037,824 | ~$40 |
| SWE-bench N=1 (gpt-5.2) | 12 | 14,010,492 | ~$140 |
| **Subtotal** | **28** | **18,378,225** | **~$183** |

---

## 8. Rigorous Evaluation (N=5, 5 SWE-bench Tasks)

This section supersedes the initial findings. We ran 5 SWE-bench Verified tasks × 4 strategies × 5 runs = 100 trials (98 completed, 2 infrastructure failures).

### 8.1 Tasks

| Task | Repo | Patch Size | F2P Tests | Rationale |
|------|------|-----------|-----------|-----------|
| django-11276 | django/django | 2,760 bytes | 26 | Large codebase navigation, multi-file fix |
| astropy-13977 | astropy/astropy | 5,777 bytes | 20 | Scientific computing, complex type system |
| pylint-4604 | pylint-dev/pylint | 1,098 bytes | 21 | AST analysis, targeted fix in large checker |
| xarray-6992 | pydata/xarray | 8,857 bytes | 12 | Data structures, multi-method fix |
| sympy-13091 | sympy/sympy | 17,385 bytes | 2 | Symbolic math, large patch size |

### 8.2 Aggregate Results

| Strategy | N | Retention | StdDev | 95% CI | Mean Turns | Mean Tokens | Compactions |
|----------|---|-----------|--------|--------|------------|-------------|-------------|
| **compact** | **25** | **0.746** | **0.045** | **±0.018** | **86.3** | **1,217,213** | **75.2** |
| recall | 24 | 0.742 | 0.064 | ±0.025 | 99.8 | 1,510,035 | 98.2 |
| session-log | 24 | 0.731 | 0.073 | ±0.029 | 106.0 | 1,659,694 | 123.9 |
| ooda | 25 | 0.730 | 0.071 | ±0.028 | 129.4 | 2,142,997 | 186.9 |

### 8.3 Statistical Comparisons (Retention)

Welch's t-test, two-tailed. Significance: `***` p<0.001, `**` p<0.01, `*` p<0.05, `ns` = not significant.

| Comparison | Δ Mean | Cohen's d | Effect | p | Sig |
|------------|--------|-----------|--------|---|-----|
| compact vs recall | -0.004 | -0.079 | negligible | 0.785 | ns |
| compact vs session-log | -0.015 | -0.243 | small | 0.400 | ns |
| compact vs ooda | -0.016 | -0.269 | small | 0.341 | ns |
| recall vs session-log | -0.010 | -0.152 | negligible | 0.599 | ns |
| recall vs ooda | -0.012 | -0.173 | negligible | 0.543 | ns |
| session-log vs ooda | -0.001 | -0.017 | negligible | 0.952 | ns |

**No comparison reaches statistical significance.** All p > 0.34.

### 8.4 Token Efficiency

| Strategy | Retention/Mtok | Total Tokens (all runs) | Overhead vs Compact |
|----------|---------------|------------------------|-------------------|
| compact | 0.888 | 30,430,320 | — |
| recall | 0.875 | 36,240,849 | +19% |
| session-log | 0.689 | 39,832,661 | +31% |
| ooda | 0.450 | 53,574,921 | +76% |

### 8.5 Per-Task Winners

| Task | Winner | Best Ret | Worst | Worst Ret |
|------|--------|---------|-------|-----------|
| django-11276 | compact | 0.720 | ooda | 0.690 |
| astropy-13977 | compact | 0.720 | session-log/ooda | 0.700 |
| pylint-4604 | compact | 0.760 | session-log | 0.710 |
| xarray-6992 | recall | 0.825 | session-log | 0.700 |
| sympy-13091 | session-log | 0.820 | ooda | 0.720 |

Compact won 3/5 tasks. Recall and session-log each won 1/5. OODA won 0/5.

### 8.6 The Compaction Cascade Effect

The data reveals a key architectural insight: strategies that add per-turn overhead trigger more compaction, which removes more information, potentially negating the benefit:

| Strategy | Mean Compactions | Overhead vs Compact |
|----------|----------------:|-------------------:|
| compact | 75.2 | — |
| recall | 98.2 | +31% |
| session-log | 123.9 | +65% |
| ooda | 186.9 | +149% |

More overhead → more context pressure → more compaction → more information loss. The sophisticated strategies' retention benefit is consumed by the additional compaction they trigger.

### 8.7 Why Initial Results Were Misleading

The N=1 SWE-bench results (Section 3.3) suggested recall was superior. Several factors explain the discrepancy:

1. **High variance in single runs.** Individual run outcomes vary by ±0.15 retention. N=1 captures a single sample from a noisy distribution.
2. **Task selection bias.** The initial 3 tasks happened to favor recall's pattern. With 5 tasks, the advantage vanished.
3. **The "recall efficiency paradox" was an artifact.** The initial observation that recall used fewer tokens than compact on django-16560 did not replicate across the larger sample.

### 8.8 Rigorous Evaluation Cost

| Item | Runs | Total Tokens |
|------|-----:|-------------:|
| Rigorous SWE-bench (gpt-5.2) | 98 | 160,078,751 |

### 8.9 Retention Score Distributions

#### compact (N=25)
```
0.65 █ (1)
0.70 ████████ (8)
0.75 ████████ (8)
0.80 ████████ (8)
```

#### recall (N=24)
```
0.60 █ (1)
0.65 █ (1)
0.70 ██████████ (10)
0.75 ███ (3)
0.80 ███████ (7)
0.85 ██ (2)
```

#### session-log (N=24)
```
0.60 ██ (2)
0.65 ██ (2)
0.70 █████████ (9)
0.75 ████ (4)
0.80 █████ (5)
0.85 █ (1)
0.90 █ (1)
```

#### ooda (N=25)
```
0.50 █ (1)
0.60 █ (1)
0.70 ██████████ (10)
0.75 █████ (5)
0.80 ████████ (8)
```

---

## 9. Final Recommendations

### 9.1 Strategy Selection (Updated)

| Scenario | Recommended Strategy | Rationale |
|----------|---------------------|-----------|
| **All scenarios** | **compact** | Best retention, fewest tokens, simplest |

The data does not justify deploying recall, session-log, or ooda as defaults. None significantly improves retention, and all add token overhead.

### 9.2 Keep the Strategy Interface

The ContextStrategy interface and 4 implementations should be preserved:
- Valuable for future experiments with different models or compaction approaches
- May show different results with weaker models that benefit more from explicit context management
- The infrastructure was cheap to build relative to the architectural insight gained

### 9.3 Future Work

1. **Investigate the compaction cascade.** A strategy that *reduces* per-turn token usage (rather than adding to it) might actually improve retention.
2. **Test with weaker models.** GPT-5.2 may be robust enough not to need context management help. Weaker models (GPT-4.1-mini, Claude Haiku) might benefit from recall or session-log.
3. **Consider larger N for future evals.** N=5 was sufficient to establish the lack of improvement, but N≈15 per condition would give tighter CIs for publication.
4. **Improve retention probes.** The narrow score range (0.50-0.90) suggests probes may lack sensitivity. More questions or finer-grained scoring might reveal real differences.
5. **Evaluate without threshold scaling.** Default thresholds (60%/70%/80%/90%) may produce different results than the aggressive 0.15 scaling used here.

## 10. Limitations

1. **Artificial compaction pressure:** `--threshold-scale 0.15` forces aggressive compaction. Real-world behavior with default thresholds may differ.
2. **Single model:** Results with GPT-5.2 may not generalize. Weaker models may benefit more from sophisticated strategies.
3. **Retention probes are coarse:** 4 questions per task, LLM-scored 0-5, normalized to 0-1. The effective measurement resolution is ~0.05.
4. **No correctness evaluation:** We measured retention only, not solution quality against gold patches.
5. **All tasks are Python.** Different languages or problem domains may produce different results.

## 11. Raw Data

- Rigorous results (98 JSON files): `results/rigorous/{task}/{strategy}/run-{n}.json`
- Analysis script: `results/rigorous/analyze.py`
- Task selection: `results/rigorous/selected_tasks.json`
- Earlier phase results: `results/phase1/` through `results/swebench/`
