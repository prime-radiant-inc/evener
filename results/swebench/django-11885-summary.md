# SWE-bench Evaluation: django__django-11885

**Task:** Combine fast delete queries in Django's deletion.Collector
**Model:** gpt-5.2 (OpenAI)
**Threshold scale:** 0.15
**Date:** 2026-02-13

## Results

| Strategy    | Turns | Total Tokens | Compaction Events | Retention Score | Task Completed | Duration (s) |
|-------------|------:|-------------:|------------------:|----------------:|:--------------:|-------------:|
| compact     |    25 |      277,912 |                12 |            0.65 |      Yes       |        45.6  |
| recall      |    92 |    1,710,387 |               117 |            0.70 |      Yes       |       204.2  |
| session-log |    38 |      496,768 |                32 |            0.60 |      Yes       |       135.4  |
| ooda        |    95 |    1,532,593 |               119 |            0.65 |      Yes       |       342.0  |

## Observations

- **All 4 strategies completed the task successfully.** Each agent implemented the fast-delete combining logic and reported passing tests.
- **compact** was the most efficient by a wide margin: fewest turns (25), fewest tokens (278k), fewest compactions (12), and fastest wall time (46s). It achieved a retention score of 0.65.
- **recall** used the most total tokens (1.71M) with 92 turns and 117 compaction events, but achieved the highest retention score (0.70). Its result message suggests it reverted test changes rather than implementing the fix from scratch, raising a question about whether its solution approach differed.
- **session-log** was a middle ground: 38 turns, 497k tokens, 32 compaction events, 135s. It had the lowest retention score (0.60) despite the session-log mechanism.
- **ooda** was the slowest (342s) with the most turns (95) and second-highest token usage (1.53M). It used 119 compaction events and 7 session-log checkpoints. Retention score matched compact at 0.65.

## Token Efficiency (tokens per turn)

| Strategy    | Tokens/Turn |
|-------------|------------:|
| compact     |      11,116 |
| recall      |      18,591 |
| session-log |      13,073 |
| ooda        |      16,132 |

## Key Takeaways

1. **compact dominated on efficiency** -- it solved the task in under a minute with 5-6x fewer tokens than the recall and ooda strategies.
2. **recall had the best retention** (0.70) but at enormous cost -- 6.2x the tokens of compact.
3. **ooda was the most expensive overall** in wall time, despite not achieving better retention or fewer turns than recall.
4. The low threshold-scale (0.15) triggered aggressive compaction across all strategies, which is reflected in the high compaction event counts for recall (117) and ooda (119).
