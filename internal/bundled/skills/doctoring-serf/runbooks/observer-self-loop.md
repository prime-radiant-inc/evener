# Runbook: observer self-loop

**Question:** did any watch re-deliver its own downstream output back to itself —
an observer/watch feedback loop the causal-provenance suppression should have
prevented?

## HEALTHY

- No settled delivery's provenance `Chain` contains a **prior hop of the same
  `watch_id`** (a hop with a different `delivery_id` than the delivery's own
  stamp). `serf-doctor watches --self-loops` reports **no watches**.
- Note: every recorded delivery's `WatchKeys` contains its own
  `(watch_id, watch_generation)` by construction (the delivery-time stamp). That
  is **not** the health signal — do not read it as one. The signal is the absence
  of a same-`watch_id` prior `Chain` hop.

## INSPECT

Take the target session id from the runbook invocation — never hardcode one.

```
# Self-loop verdict for every watch on the session (empty output ⇒ healthy):
serf-doctor watches <selector> --self-loops --json

# Optional: enumerate observer sessions feeding this worker, to widen the sweep:
serf-doctor tree <selector> --observers
```

For a fleet sweep, run the `--self-loops` check for each session the tree
surfaces.

## CLASSIFY

- For each watch in the `--self-loops` output, `self_loop.detected == true` is a
  **confirmed self-loop**. Emit one Finding:
  - `category`: `watch_self_loop`
  - `severity`: `high`
  - `signature`: `watch_self_loop:<sessionID>:<watchID>`
  - `evidence`: `watchIds` = the watch, `deliveryIds` = `self_loop.delivery_ids`,
    `doctorCommand` = the `serf-doctor watches … --self-loops` invocation.
  - `suggestedFix.type`: `diagnosis` (a self-loop that escaped suppression is a
    bug in serf's suppression, not in the doctor's machinery — report it).
  - If `self_loop.chain_truncated == true`, say so in `description`: the `Chain`
    was truncated (`maxDiagnosticChain`), so the loop may be deeper than shown.
- Empty `--self-loops` output (no watches) ⇒ **PASS, emit nothing.**

A run that finds no self-loops is the expected, correct outcome.
