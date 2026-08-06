# Runbook: stale-notification

**Question:** did this session keep receiving notification steering turns
after it had already declared itself done (`end_turn=true`)?

## HEALTHY

- Few or no notification-kind steering turns arrive after the session's
  final `end_turn=true` result-tool call. `serf-doctor transcript --health`
  reports `stale_notifications` below the threshold.
- Note: a notification delivered BEFORE the session's final `end_turn` is
  normal — the session is still working and can act on it. Only post-done
  delivery counts here.

## INSPECT

Take the target session id from the runbook invocation — never hardcode one.

```
serf-doctor transcript <selector> --health --json
```

## CLASSIFY

```yaml
audit:
  - title: "Stale notifications after end_turn"
    severity: low
    category: stale_notification
    metric: stale_notifications
    op: ">="
    value: 2
```

- Read the flagged session's transcript around each stale notification
  (`serf-doctor transcript <sel> --format outline`) to see how late it
  arrived relative to the final `end_turn` call, and whether it forced a
  wasted acknowledgment turn — the 2026-08-05 session study found completion
  events arriving up to 54 minutes after the final answer, ~14 sessions
  affected. If the notification's source watch itself looks suspect, widen
  the sweep with `serf-doctor tree <sel> --observers` and pair this with the
  `observer-self-loop` runbook.
- A single stale notification is not a Finding — the machinery notifying a
  session that has just finished is expected under normal timing; the
  pattern worth flagging is *repeated* late delivery.

`category: stale_notification` is minted for this runbook — none of
`finding-contract.md`'s existing categories name "a notification arrived
after the session was already done," and it is a distinct forensic shape
from `dropped_delivery` (which is about a send never arriving at all).

A healthy run emits zero findings.
