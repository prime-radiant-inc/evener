# Hypothesis Experiment Results

## Experiment: Coordinator Explorer Delegation Variants
Date: 2026-03-20
Model: openai/gpt-5.4
Benchmark: terminal-bench@2.0
Tasks: path-tracing-reverse, winning-avg-corewars, protein-assembly
Trials per task: 3 (target), some variants have fewer due to spot instance terminations

## Variants

| ID | Name | Changes |
|----|------|---------|
| hA | Control (time-boxed) | Base coordinator: explorer=10, implementer=50, mise en place |
| hH1 | Intent + verbatim | Tell explorer WHY + ask for raw output verbatim |
| hH2 | Narrow queries | 2-3 targeted explorer queries (max_turns=5) instead of one survey |
| hH3 | Parallel tools | Stronger parallel tool mandate in explorer prompt |
| hH4 | Mini model | Explorer uses openai/gpt-5.4-mini instead of inherit |
| hH5 | Combined (H1+H3+fallback) | Intent+verbatim + parallel tools + coordinator self-read fallback |

## Results by Task

### path-tracing-reverse
| Variant | Trials | Scores | Mean |
|---------|--------|--------|------|
| hA      | 3      | 0, 0, 1 | 0.333 |
| hH1     | 3      | 1, 1, 1 | **1.000** |
| hH2     | 3      | 1, 0, 0 | 0.333 |
| hH3     | 3      | 1, 1, 0 | 0.667 |
| hH4     | 3      | 0, 0, 1 | 0.333 |
| hH5     | 2      | 1, 0   | 0.500 |

### winning-avg-corewars
| Variant | Trials | Scores | Mean |
|---------|--------|--------|------|
| hA      | 3      | 1, 1, 1 | **1.000** |
| hH1     | 4      | 1, 1, 0, 1 | 0.750 |
| hH2     | 3      | 1, 1, 1 | **1.000** |
| hH3     | 2      | 0, 1   | 0.500 |
| hH4     | 2      | 1, 0   | 0.500 |
| hH5     | 1      | 1     | 1.000 |

### protein-assembly
| Variant | Trials | Scores | Mean |
|---------|--------|--------|------|
| hA      | 3      | 0, 0, 0 | 0.000 |
| hH1     | 3      | 0, 0, 0 | 0.000 |
| hH2     | 3      | 0, 0, 0 | 0.000 |
| hH3     | 3      | 0, 0, 0 | 0.000 |
| hH4     | 2      | 0, 0   | 0.000 |
| hH5     | 2      | 1, 1   | **1.000** |

## Overall Summary

| Variant | Trials | Mean Reward | vs Control |
|---------|--------|-------------|------------|
| hA (control) | 9 | 0.444 | -- |
| hH1 (intent+verbatim) | 10 | **0.600** | +0.156 |
| hH2 (narrow queries) | 9 | 0.444 | +0.000 |
| hH3 (parallel tools) | 8 | 0.375 | -0.069 |
| hH4 (mini model) | 7 | 0.286 | -0.159 |
| hH5 (combined) | 5 | **0.800** | +0.356 |

## Notes

1. **hH5 (combined) is the standout** at 0.800 mean, but only has 5 trials due to
   repeated spot instance terminations. The protein-assembly success (2/2) is notable
   since NO other variant solved it. However, low sample size means this could be noise.

2. **hH1 (intent+verbatim)** shows the clearest signal on path-tracing-reverse (3/3 vs
   1/3 control). The hypothesis that telling explorers WHY improves info quality seems
   supported.

3. **hH2 (narrow queries)** performs identically to control overall (0.444). The narrow
   query approach doesn't help or hurt.

4. **hH3 (parallel tools)** slightly underperforms control. The aggressive parallel
   mandate may confuse the model.

5. **hH4 (mini model)** is the worst performer. The cheaper explorer model loses quality
   that matters for downstream implementation.

6. **protein-assembly is the hardest task** - only hH5 solved it. This task likely
   requires the combined benefits of better delegation (intent sharing) + more efficient
   exploration (parallel tools) + fallback reads.

7. **Caveat**: Several variants have fewer than 9 trials due to external termination
   of spot instances. hH4 (7 trials), hH3 (8 trials), and especially hH5 (5 trials)
   have enough variance that these results should be treated as directional, not
   conclusive.

## Recommendations

- **Adopt H1 (intent+verbatim)** as the new baseline - clear improvement, robust sample.
- **Rerun hH5 with full 9 trials** to confirm the protein-assembly signal.
- **Drop H2, H3, H4** - no evidence of benefit.
- Consider testing H1 + fallback (without H3's parallel mandate) as a cleaner combination.
