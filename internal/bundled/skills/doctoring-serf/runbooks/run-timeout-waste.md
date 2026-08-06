# Runbook: run-timeout-waste

**Question:** did this session burn budget hitting the job watchdog over and
over, discarding the run's output each time, instead of adapting?

## HEALTHY

- Few or no terminal jobs ended by `run_timeout`. `serf-doctor transcript
  --health` reports `jobs.run_timeout` below the threshold.
- Note: a single `run_timeout` is not itself a Finding — it becomes a
  wasted-budget pattern only once the session keeps re-running a doomed
  command instead of adapting (raising the tool's own timeout, running in
  background, narrowing scope).

## INSPECT

Take the target session id from the runbook invocation — never hardcode one.

```
serf-doctor transcript <selector> --health --json
# per-job command/output_bytes detail, if the summary count needs unpacking:
serf-doctor jobs <selector> --json
```

## CLASSIFY

```yaml
audit:
  - title: "Run-timeout jobs wasting budget"
    severity: high
    category: timeout
    metric: jobs.run_timeout
    op: ">="
    value: 5
```

- Cross-check `jobs.zero_output_terminal` in the same `--health` result — the
  2026-08-05 session study's worst cases combined `run_timeout` with zero
  recorded output (everything the killed process printed was thrown away),
  which is what turns the retries into blind repeats instead of informed
  ones (`0341CrVzn6z2CM0sgd87F2`: 90 of 145 jobs ended `run_timeout`, 89 with
  zero output, ~90 minutes lost; `0340xUxn3kwKp6BNy6J7Tj`: 15 timeouts then
  committed without ever seeing a pass). `serf-doctor jobs <sel>` shows each
  job's own command, so confirm the flagged session was actually re-running
  the *same* command rather than five distinct long-but-legitimate jobs.
- Fewer than the threshold's `run_timeout` jobs is not a Finding — a session
  can legitimately hit the watchdog once or twice on a genuinely slow build.

A healthy run emits zero findings.
