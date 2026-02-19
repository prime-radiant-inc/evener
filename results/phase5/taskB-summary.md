# Phase 5 — Task B (Go Calculator Refactoring) — GPT-5.2

Model: `gpt-5.2` | Threshold scale: `0.3` | Context window: 128k

## Results

| Metric              | compact | recall  | session-log | ooda    |
|---------------------|---------|---------|-------------|---------|
| Completed           | Yes     | Yes     | Yes         | Yes     |
| Turn count          | 15      | 24      | 16          | 19      |
| Total tokens        | 222,014 | 383,228 | 240,100     | 292,383 |
| Input tokens        | 214,200 | 375,179 | 232,421     | 284,467 |
| Output tokens       | 7,814   | 8,049   | 7,679       | 7,916   |
| Compaction events    | 0       | 0       | 0           | 0       |
| Recall calls        | 0       | 0       | 0           | 0       |
| Fork summary calls  | 0       | 0       | 16          | 19      |
| Retention score     | 0.85    | 0.90    | 0.80        | 0.80    |
| Duration (seconds)  | 99.0    | 114.2   | 124.4       | 136.6   |

## Observations

- **No compaction triggered**: With `--threshold-scale 0.3`, the 30% threshold (roughly 38k tokens) was never reached by any strategy. The task completed within a single context window for all strategies. This means the strategies were not differentiated by their compaction behavior on this task.
- **Recall was never invoked**: The recall strategy had 0 recall calls because compaction never triggered (recall runs post-compaction to retrieve lost context).
- **Fork summary and OODA orient ran every turn**: Session-log and OODA both fired their fork-summarize calls (one per turn), adding token overhead without compaction to benefit from. This explains their higher total token counts relative to compact.
- **Compact was fastest and most token-efficient**: Fewest turns (15), lowest total tokens (222k), shortest duration (99s).
- **Recall had highest retention but highest cost**: 0.90 retention, but 383k total tokens and 24 turns — the most expensive run despite no recall calls being made.
- **Session-log and OODA tied on retention (0.80)**: Both scored lower than compact (0.85) and recall (0.90). The per-turn summarization overhead did not translate to better retention for this task.
- **Output tokens were consistent**: All strategies produced roughly the same amount of output (~7.7k-8.0k tokens), confirming the difference is in context management overhead, not task execution.
