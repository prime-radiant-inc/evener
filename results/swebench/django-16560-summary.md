# SWE-bench Evaluation: django__django-16560

**Task:** Allow customization of `violation_error_code` on `BaseConstraint.validate`
**Model:** gpt-5.2 (OpenAI)
**Threshold scale:** 0.15
**Date:** 2026-02-13

## Results

| Strategy    | Turns | Total Tokens | Compaction Events | Retention Score | Task Completed | Duration (s) |
|-------------|------:|-------------:|------------------:|----------------:|:--------------:|-------------:|
| compact     |    88 |    1,304,625 |                96 |            0.60 |       Yes      |       205.3  |
| recall      |    55 |      751,211 |                51 |            0.85 |       Yes      |       227.5  |
| session-log |    69 |    1,007,027 |                70 |            0.80 |       Yes      |       288.5  |
| ooda        |    90 |    1,427,451 |               110 |            0.85 |       Yes      |       369.4  |

## Observations

- All 4 strategies completed the task successfully.
- **recall** was the most token-efficient (751K tokens, 55 turns) and achieved the highest retention score (0.85) tied with ooda.
- **compact** used 1.3M tokens across 88 turns but had the lowest retention score (0.60), suggesting aggressive compaction discarded probe-relevant context.
- **session-log** landed in the middle on tokens (1.0M) and retention (0.80), with 69 fork-summary calls generating session log entries.
- **ooda** was the most expensive (1.4M tokens, 90 turns, 369s) and had the most compaction events (110) including 4 session-log checkpoints. Despite high cost, it matched recall's retention score (0.85).
- Duration varied significantly: compact was fastest (205s), ooda slowest (369s). The extra overhead in ooda comes from orient-phase steering and more frequent compaction.

## Token Efficiency vs Retention

| Strategy    | Tokens per Retention Point | Tokens per Turn |
|-------------|---------------------------:|----------------:|
| compact     |                  2,174,375 |          14,825 |
| recall      |                    883,778 |          13,658 |
| session-log |                  1,258,784 |          14,594 |
| ooda        |                  1,679,354 |          15,861 |

**Best overall: recall** -- fewest tokens, fewest turns, highest retention, reasonable duration.
