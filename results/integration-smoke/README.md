# Integration Smoke Test Results

**Date:** 2026-02-13
**Model:** GPT-5.2 (OpenAI)
**Task:** Multi-step Python task (add 3 functions + write tests + run tests)

## Results

| Strategy | Turns | Total Tokens | Fork Calls | Retention Score | Duration |
|----------|-------|-------------|------------|-----------------|----------|
| compact | 14 | 134,952 | 0 | 0.87 | 30s |
| recall | 13 | 125,806 | 0 | 0.80 | 34s |
| session-log | 16 | 164,952 | 16 | 0.73 | 70s |
| ooda | 16 | 198,198 | 16 | 0.60 | 64s |

## Notes

- All strategies completed the task successfully
- No compaction events or recall calls — task was too short to trigger context pressure
- session-log and ooda strategies show overhead from forked summarization (expected)
- ooda uses the most tokens due to Orient-phase session log injection each turn
- These strategies are designed for long sessions (100+ turns); proper evaluation requires SWE-bench Pro or similar long-horizon tasks
- Retention score differences are likely noise on such a short task
