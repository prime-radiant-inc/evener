# Runbook: watch delivery health

**Question:** are this session's watches delivering cleanly — distinct deliveries
settling as expected, with coalescing recognised as normal and drops explained?

## HEALTHY

- Each watch's distinct deliveries settled to `delivered` (or to a `dropped` /
  `evicted` terminal with an expected, benign `diagnostic_reason`).
- `pending_lines > distinct_deliveries` is **expected coalescing**, not a fault —
  latest-wins collapse of `watch_send_pending` frames. The `coalesced` flag being
  true is normal.
- `still_pending == 0` for a session that has finished (no frame stuck
  un-settled).

## INSPECT

Take the target session id from the runbook invocation.

```
serf-doctor watches <selector> --json
```

Per watch, read: `distinct_deliveries`, `delivered` / `dropped` / `evicted`,
`pending_lines`, `coalesced`, `still_pending`, and each `deliveries[].terminal` /
`deliveries[].diagnostic_reason`.

## CLASSIFY

- A `dropped` delivery whose `diagnostic_reason` indicates an **unexpected** loss
  (not a benign send-to-gone / superseded case) → emit:
  - `category`: `dropped_delivery`, `severity`: `medium`
  - `signature`: `watch-delivery-health:dropped_delivery:<weekBucket>`
  - `evidence`: `watchIds`, `deliveryIds`, the `diagnostic_reason` in
    `description`, `doctorCommand`.
  - `suggestedFix.type`: `diagnosis`.
- `still_pending > 0` on a **finished** session (a frame that never settled) →
  emit `category`: `stuck_processing`, `severity`: `low`, signature keyed to the
  watch; `suggestedFix.type`: `diagnosis`.
- `coalesced == true` (pending lines exceed distinct deliveries) → **PASS with a
  note**, never a finding. This is the expected coalescing the tool already
  collapses.
- All deliveries cleanly settled → **PASS, emit nothing.**

Do not emit for expected coalescing or for a normal `delivered` count — those are
visibility, not violations.
