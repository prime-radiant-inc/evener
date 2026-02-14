# SWE-Bench Evaluation: pytest-dev/pytest-5787

**Task:** Exception serialization should include chained exceptions
**Model:** gpt-5.2 / openai
**Threshold scale:** 0.15
**Date:** 2026-02-13

## Results

| Strategy    | Turns | Total Tokens | Compaction Events | Retention Score | Task Completed | Duration (s) |
|-------------|------:|-------------:|------------------:|----------------:|:--------------:|-------------:|
| compact     |    93 |    1,287,323 |                82 |            0.50 |       yes      |        202.9 |
| recall      |    45 |      608,130 |                37 |            0.55 |       yes      |        110.0 |
| session-log |   200 |    3,405,222 |               288 |            0.75 |       no       |        770.9 |
| ooda        |    83 |    1,251,843 |                94 |            0.60 |       yes      |        326.5 |

## Notes

- **compact**: Completed in 93 turns. Reported fixing chained exception repr round-tripping through report serialization and adding a regression test. Lowest retention score (0.50).
- **recall**: Most efficient run -- fewest turns (45), lowest token usage (608k), fastest wall time (110s). Completed with retention score of 0.55. No recall sub-agent calls were triggered (recall_calls=0).
- **session-log**: Hit the 200-turn limit and returned an empty result string, indicating it did not produce a final decision. Consumed by far the most tokens (3.4M) and had 198 fork_summary_calls and 288 compaction events. Despite not completing the task, it achieved the highest retention score (0.75), suggesting the session log preserves context well but the overhead may cause the agent to spin.
- **ooda**: Completed in 83 turns with 83 fork_summary_calls. Reported the task as "resolved" but noted that no code changes were required (earlier failures were due to running against installed pytest instead of checkout). Retention score 0.60.

## Observations

1. **Recall is the clear efficiency winner** on this task: 2x fewer turns than the next best, 2x fewer tokens, fastest wall time.
2. **Session-log has the highest retention but worst task completion** -- it burned 3.4M tokens across 200 turns without finishing. The overhead of fork-summarize on every turn (198 calls) appears to slow convergence rather than help it.
3. **Compact and ooda both completed** with similar token budgets (~1.25M), but ooda took 60% longer in wall time due to fork-summarize overhead.
4. **No strategy triggered recall sub-agent calls** (recall_calls=0 across all 4 runs), which means the recall strategy on this task behaved essentially like compact without the recall overhead.
5. **Retention scores increase with context strategy complexity** (compact 0.50 < recall 0.55 < ooda 0.60 < session-log 0.75), but task completion does not follow the same trend.
