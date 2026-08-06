# Runbook: error-loop

**Question:** did this session get stuck retrying the same failing tool call
instead of recognizing the failure and changing approach?

## HEALTHY

- `longest_identical_run` is either short, or wasn't all errors —
  `serf-doctor transcript --health` reports a run below the threshold, or one
  whose results weren't all failures.
- Note: a repeated call that keeps SUCCEEDING (e.g. two identical greps that
  both return the same empty match) is not a problem — length alone never
  trips this runbook, only length **and** all-errors together.

## INSPECT

Take the target session id from the runbook invocation — never hardcode one.

```
serf-doctor transcript <selector> --health --json
```

## CLASSIFY

```yaml
audit:
  - title: "Long identical-error tool-call run"
    severity: medium
    category: provider_error
    all:
      - metric: longest_identical_run.errors
        op: "=="
        value: true
      - metric: longest_identical_run.length
        op: ">="
        value: 3
```

- Read the flagged session's transcript around the identical run
  (`serf-doctor transcript <sel> --format outline` or `--range last:N`) to
  confirm the calls really are identical retries of a failing operation, not
  a legitimate scripted retry with backoff — and check whether the runtime's
  own loop-detector steering (`steering.loop-detected` in the same
  `--health` output) already fired, and whether the session heeded it. The
  2026-08-05 session study found this pattern reach ~300 consecutive
  failures in one session (`034163AU8MmLapfXKT7nMu`, a `set_viewport` loop
  that survived two loop-warnings and three user interventions), so absence
  of a detector firing is not the same as absence of the loop.
- A run below the threshold, or one whose failures cleared before length 3,
  is not a Finding — retrying once or twice after a transient error is
  normal and often correct.

A healthy run emits zero findings.
