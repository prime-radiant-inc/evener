# Phase 5 - Task C Summary (gpt-5.2, --threshold-scale 0.3)

Task: Python bug investigation (3 bugs in data_processor.py, fix + add edge-case tests + write BUGFIX_NOTES.md)

## Comparison Table

| Metric              | compact    | recall     | session-log | ooda       |
|---------------------|------------|------------|-------------|------------|
| Completed           | yes        | yes        | yes         | yes        |
| Turn count          | 21         | 26         | 28          | 21         |
| Total tokens        | 282,114    | 385,300    | 391,110     | 276,224    |
| Input tokens        | 279,548    | 382,037    | 388,264     | 273,575    |
| Output tokens       | 2,566      | 3,263      | 2,846       | 2,649      |
| Compaction events    | 0          | 0          | 0           | 0          |
| Recall calls        | 0          | 0          | 0           | 0          |
| Fork summary calls  | 0          | 0          | 25          | 20         |
| Retention score     | 0.80       | 0.80       | 0.85        | 0.90       |
| Duration (seconds)  | 53.5       | 66.3       | 104.7       | 79.6       |

## Observations

- All 4 strategies completed the task successfully. No compaction was triggered in any run (task fit within the 0.3-scaled thresholds with gpt-5.2's 128k context window).
- **ooda** achieved the highest retention score (0.90) while tying for the fewest turns (21) and using the fewest total tokens (276k).
- **compact** was the fastest (53.5s) with identical turn count to ooda, but had a lower retention score (0.80).
- **session-log** had the highest token usage (391k) and longest duration (104.7s), likely due to the 25 fork-summarize calls adding latency. It achieved a slightly better retention score (0.85) than compact/recall.
- **recall** used more turns (26) and tokens (385k) than compact but achieved the same retention score (0.80). No recall sub-agent calls were triggered (no compaction occurred to create searchable history).
- The lack of compaction events across all strategies means recall and compact behaved essentially identically (both are just baseline without compaction). The differentiation comes from session-log and ooda which run their fork-summarize/orient phases regardless of compaction.
