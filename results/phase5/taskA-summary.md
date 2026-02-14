# Phase 5 — Task A Results (gpt-5.2, threshold-scale 0.3)

Task: Multi-file Python web app refactoring (10 subtasks)

| Metric              | compact    | recall     | session-log | ooda       |
|---------------------|------------|------------|-------------|------------|
| Completed           | yes        | yes        | yes         | yes        |
| Turn count          | 31         | 27         | 25          | 23         |
| Total tokens        | 553,881    | 439,370    | 446,155     | 366,463    |
| Input tokens        | 543,296    | 430,496    | 433,638     | 356,806    |
| Output tokens       | 10,585     | 8,874      | 12,517      | 9,657      |
| Compaction events    | 6          | 0          | 5           | 0          |
| Recall calls        | 0          | 0          | 0           | 0          |
| Fork summary calls  | 0          | 0          | 25          | 23         |
| Retention score     | 0.80       | 0.80       | 0.85        | 0.75       |
| Duration (seconds)  | 149.3      | 124.5      | 196.3       | 160.9      |

## Observations

- All 4 strategies completed the task successfully.
- **ooda** used the fewest turns (23) and fewest total tokens (366k), but had the lowest retention score (0.75).
- **session-log** achieved the highest retention score (0.85) while using moderate turns (25), though it was the slowest (196s) due to fork-summarization overhead (25 calls).
- **compact** used the most turns (31) and most tokens (554k), with 6 compaction events triggered by the lower thresholds.
- **recall** had 0 compaction events and 0 recall calls — the context window was never pressured enough with gpt-5.2's 128k window to trigger either mechanism at this threshold scale.
- No strategy triggered recall calls, suggesting Task A fits comfortably within gpt-5.2's context window even with --threshold-scale 0.3.
- The fork-summarization strategies (session-log, ooda) produced more output tokens on average, reflecting the session log entries being written in parallel.
