# Phase 4: Retention Test Results

## Test Configuration

- Model: gpt-4.1-mini
- Threshold scale: 0.05 (triggers Layer 1 observation masking very aggressively)
- Test repo: config.yaml + app.py + test_app.py (DB_PORT bug, add functions)
- Probes: 4 retention questions about config.yaml details read early in session

## Comparison Table

| Strategy    | Completed | Turns | Total Tokens | Compaction Events | Compaction Layers                        | Recall Calls | Fork Summary Calls | Retention Score | Duration (s) |
|-------------|-----------|-------|-------------|-------------------|------------------------------------------|-------------|-------------------|----------------|-------------|
| compact     | yes       | 25    | 135,246     | 33                | 10x obs_mask, 5x think_clear, 2x ckpt   | 0           | 0                 | 0.60           | 90.3        |
| recall      | yes       | 19    | 101,755     | 23                | 8x obs_mask, 5x think_clear, 2x ckpt    | 0           | 0                 | 0.60           | 90.2        |
| session-log | yes       | 11    | 53,526      | 10                | 6x obs_mask, 2x think_clear, 1x sl_ckpt | 0           | 11                | 0.80           | 66.1        |
| ooda        | yes       | 9     | 39,382      | 6                 | 6x obs_mask                              | 0           | 9                 | 0.80           | 38.1        |

## Retention Score Analysis

### Hypothesis

- compact: lower retention (config.yaml details lost after checkpoint)
- recall: higher retention IF the model calls the recall tool
- session-log: higher retention (config details preserved in session log entries)
- ooda: similar to session-log (orient phase keeps session log visible)

### Results vs. Hypothesis

**Confirmed: session-log and ooda preserve information better than compact.**

- session-log (0.80) and ooda (0.80) both scored 33% higher than compact (0.60)
  on retention probes about early-session config.yaml details.
- The fork summarization mechanism (11 calls for session-log, 9 for ooda)
  captured config.yaml details in structured session log entries, which survive
  compaction checkpoints.

**Confirmed: recall scored the same as compact when the model doesn't use it.**

- recall scored 0.60, identical to compact. The model never called the recall
  tool (recall_calls = 0). This is a known limitation: gpt-4.1-mini doesn't
  reliably invoke the recall tool without prompting or training.
- The recall strategy only helps if the model proactively searches its own
  transcript history. Without recall tool usage, it degrades to compact behavior.

### Efficiency Observations

The session-log and ooda strategies were dramatically more efficient:

- **Turns**: ooda completed in 9 turns vs. compact's 25 (2.8x fewer)
- **Tokens**: ooda used 39K tokens vs. compact's 135K (3.4x fewer)
- **Duration**: ooda finished in 38s vs. compact's 90s (2.4x faster)
- **Compaction events**: ooda triggered only 6 compactions vs. compact's 33

This suggests that the session log entries provide enough context for the model
to work more efficiently, avoiding redundant re-reads and retries that compact
requires after losing context.

### Why the Efficiency Difference?

With aggressive compaction (0.05 scale), the compact strategy frequently masks
observations and clears thinking, forcing the model to re-read files it already
read. The session-log checkpoint preserves a structured summary, so the model
retains enough context to continue without re-reading. This explains both the
higher retention scores AND the lower token/turn counts.

## Key Observations

1. **Session-log is the clear winner** on both retention and efficiency at this
   threshold scale. It preserves 33% more information while using 60% fewer tokens.

2. **OODA matches session-log on retention** and is even more efficient (fewer
   turns, fewer tokens, faster). The orient phase's injection of session log
   context helps the model stay focused.

3. **Recall is ineffective with gpt-4.1-mini** because the model doesn't
   proactively use the recall tool. This may improve with gpt-5.2 in Phase 5.

4. **Compact degrades gracefully but expensively** -- it still completes the task
   but uses far more turns and tokens, and loses early-session details.

5. **All strategies completed the task successfully** despite aggressive compaction,
   which validates the compaction system's correctness even under stress.
