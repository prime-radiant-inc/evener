# Runbook: observer self-loop (runaway fuse)

**Question:** did any watch's self-influence go **unbounded** — did the runaway
fuse fire? Self-influenced deliveries (a watch reacting to a session that its own
earlier delivery influenced) are **normal and expected** under inform+breaker;
the Finding is a loop that climbed until the machinery had to cut it.

## HEALTHY

- No watch has a `runaway` drop. `serf-doctor watches --self-loops` reports
  **no watches** (it surfaces only watches whose fuse FIRED).
- Self-influenced deliveries with a **bounded** `self_influence_depth` are fine —
  the sidecar saw its own echo, was informed by the depth-gradient line, and
  either disengaged or stayed shallow. A non-zero `max_self_influence_depth` that
  never reached the fuse is healthy, not a problem.

## INSPECT

Take the target session id from the runbook invocation — never hardcode one.

```
# Watches whose runaway fuse FIRED on the session (empty output ⇒ healthy):
serf-doctor watches <selector> --self-loops --json

# Optional: enumerate observer sessions feeding this worker, to widen the sweep:
serf-doctor tree <selector> --observers
```

For a fleet sweep, run the `--self-loops` check for each session the tree
surfaces.

## CLASSIFY

- For each watch in the `--self-loops` output, `runaway_drops > 0` is the
  **Finding** — the fuse fired, so a real runaway loop was bounded by the
  machinery (sends at `self_influence_depth >= 8` were dropped with
  `diagnostic_reason: "runaway"`). Emit one Finding:
  - `category`: `watch_runaway`
  - `severity`: `high`
  - `signature`: `watch_runaway:<sessionID>:<watchID>`
  - `evidence`: `watchIds` = the watch, `deliveryIds` = the dropped-send delivery
    ids, `doctorCommand` = the `serf-doctor watches … --self-loops` invocation.
    Put `max_self_influence_depth` and `runaway_drops` in the `description`.
  - `suggestedFix.type`: `diagnosis` (a runaway that the fuse had to cut is a
    sidecar/watch-topology problem in serf, not in the doctor's machinery —
    report it).
- A watch that is self-influenced but **bounded** (`runaway_drops == 0`, any
  `max_self_influence_depth`) is **not** a Finding — the inform+breaker design
  expects it. `--self-loops` will not list it.
- Empty `--self-loops` output (no watches) ⇒ **PASS, emit nothing.**

A run where no fuse fired is the expected, correct outcome.
