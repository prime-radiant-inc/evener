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
    category: error_loop
    all:
      - metric: longest_identical_run.errors
        op: "=="
        value: true
      - metric: longest_identical_run.length
        op: ">="
        value: 3
  - title: "Runtime loop-detector fired"
    severity: medium
    category: error_loop
    metric: steering.loop-detected
    op: ">="
    value: 1
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

**Two blind spots in the identical-run check, and why the loop-detector
check exists to cover them:**

- **Free-text argument fields fragment the signature.** The identical-run
  signature hashes a call's whole arguments payload
  (`toolCallSignature`/`shortHash`), so a tool whose arguments carry a
  free-text field the model varies each call (e.g. a browser tool's
  `purpose` rationale string) never repeats the same hash twice, even when
  every other argument and the underlying action are identical. A ~300-call
  MCP tool loop measured `longest_identical_run.length` at 10, not 300,
  purely from this fragmentation — the run was real, the metric just
  couldn't see it.
- **In-band error reporting defeats the errors clause.** Some tools (MCP
  tools especially) report failure inside a successful result body — the
  call is recorded `is_error=false` with `"Error: ..."` text in the content
  — rather than setting the transport-level error flag.
  `longest_identical_run.errors` reads the recorded `is_error` flag, so it can never be true
  for a tool that fails this way, no matter how long or how failed the run
  actually is.
- Because of both gaps, **the loop-detector check is the primary net for
  this failure shape**, not the identical-run check — the runtime's own
  detector operates on live signatures as calls happen, independent of
  argument-field content or which channel a tool used to report failure.
  Treat an identical-run miss as inconclusive, not as evidence of health;
  use `serf-doctor transcript <sid> --count <tool>` to get the tool's real
  call count and `serf-doctor transcript <sid> --format outline` to read
  the run and judge it manually when the mechanical checks disagree or
  both stay silent.

A healthy run emits zero findings.

`category: error_loop` is minted for this runbook — `provider_error` (this
runbook's prior category) named a transport/provider failure class, but a
tool-call loop is a session-behavior defect that can happen with a healthy
provider and a perfectly working tool the session simply keeps calling the
same way; `error_loop` names that shape honestly instead of borrowing a
category about something else.
