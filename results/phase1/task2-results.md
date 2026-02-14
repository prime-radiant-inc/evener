# Phase 1, Task 2: Recall Tool Verification

**Date:** 2026-02-13
**Model:** gpt-4.1-mini (OpenAI)
**Strategies tested:** recall, session-log
**Task:** Read main.py, create FUNCTIONS.md listing all functions, then use recall to verify

## Pass Criteria

| Criterion | Result |
|-----------|--------|
| recall strategy: recall_calls >= 1 | FAIL (0 calls) |
| recall strategy: completed | PASS |
| session-log strategy: recall_calls >= 1 | FAIL (0 calls) |
| session-log strategy: completed | PASS |
| FUNCTIONS.md exists and lists functions | PASS (both runs, all 4 functions listed) |

**Overall: PARTIAL FAIL** -- task completed correctly but recall tool was never invoked.

## Analysis

The model did not call the recall tool in either strategy, despite being explicitly
instructed to "use the recall tool to verify you listed all functions by searching
for 'def ' in your earlier transcript." This is rational behavior:

1. No compaction occurred (4-5 turns, ~14-18K tokens, well under thresholds)
2. The recall tool description says "Use this when you need to remember details
   from earlier in the session that may have been compacted away"
3. The model correctly identified that all information was still in its context
   window and skipped the unnecessary recall step

This means the recall tool cannot be meaningfully tested in short tasks. True
verification requires a session where compaction has actually discarded earlier
turns, which is a Phase 3+ concern (after --threshold-scale lowers compaction
thresholds).

## Eval Metrics

### Recall Strategy
- Turns: 4
- Total tokens: 14,250
- Recall calls: 0
- Compaction events: 0
- Duration: 7.6s

### Session-Log Strategy
- Turns: 5
- Total tokens: 18,163
- Recall calls: 0
- Fork summary calls: 5
- Compaction events: 0
- Duration: 10.8s

## FUNCTIONS.md Output (both runs)

Both runs correctly listed all 4 functions: fibonacci, factorial, gcd, lcm.

## Implications for Evaluation Plan

- Recall tool verification should be deferred to Phase 3 when --threshold-scale
  can force compaction in shorter sessions
- The tool is registered and trackable (we confirmed recall_calls counter works
  via the EventToolCallStart mechanism)
- A proper recall test needs: (1) enough turns to trigger compaction, (2) a
  question about information from the compacted region
