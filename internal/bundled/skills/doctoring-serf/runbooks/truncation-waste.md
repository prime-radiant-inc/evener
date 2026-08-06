# Runbook: truncation-waste

**Question:** did this session keep re-triggering the tool registry's
output-truncation banner instead of narrowing its request after the first
truncated result?

## HEALTHY

- Few or no tool results carry the truncation banner. `serf-doctor
  transcript --health` reports `truncation_warnings` below the threshold.
- Note: one truncated result is not a Finding by itself — the pattern is
  *repeated* truncation without narrowing (a smaller glob, a bounded
  read/paging affordance, an excluded worktree) after the first warning.

## INSPECT

Take the target session id from the runbook invocation — never hardcode one.

```
serf-doctor transcript <selector> --health --json
```

## CLASSIFY

```yaml
audit:
  - title: "Repeated truncated tool output"
    severity: medium
    category: truncation
    metric: truncation_warnings
    op: ">="
    value: 3
```

- Read the flagged session's transcript (`serf-doctor transcript <sel>
  --format outline`) around each truncated result to see whether the session
  narrowed its request afterward, or repeated the same unscoped call. The
  2026-08-05 session study found this truncation happens **from the front**
  (dropping the top-level, usually most-relevant entries) with only a prose
  warning — worst cases removed 24.6M and 9.36M characters in a single call
  — so a session that never narrows can lose the most relevant results
  repeatedly, not just once.
- Fewer than the threshold's truncated results is not a Finding — one large
  result that gets truncated once, with the session adapting afterward, is
  expected tool behavior, not a defect pattern.

`category: truncation` is minted for this runbook — none of
`finding-contract.md`'s existing categories name "the tool registry
truncated a result and the session didn't adapt," and it is a distinct
forensic shape from `provider_error` or `timeout`.

A healthy run emits zero findings.
