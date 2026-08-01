# tui-steer-in-idle-fails-fast: optimistic-rendering reject path (unit-driven)

TUI counterpart of `web-steer-in-idle-fails-fast.md`. The production
TUI intentionally does not drive Ctrl+S as a force-steer action in IDLE:
`handleSessionForceSteer` returns early unless the composer is in queue
mode (`cmd/serf-tui/hub_session_keys.go#handleSessionForceSteer`). That gate is correct. This scenario documents the deterministic
falsification path for the underlying reject behavior without asking a
live tmux driver to bypass production UI state.

The behavior is covered by unit test:

```bash
go test ./cmd/serf-tui -run TestHubModel_SteerFailsFastOnRPCUnavailable
```

## Expected

- The test registers an optimistic `turn/steer` pending entry.
- The scripted AppWire transport rejects with
  `"steer is not available for this session"`.
- The pending coordinator emits a failed message whose reason contains
  `"not available"`.
- No authoritative steering message is appended.

## Falsification

- The test times out waiting for the failed pending message.
- The failure reason is `"server did not confirm"` instead of the daemon's
  immediate unavailable response.
- The TUI production Ctrl+S gate is weakened to send force-steer requests
  while the session is IDLE.

## Related

- Web browser scenario:
  `test/scenarios/web-steer-in-idle-fails-fast.md`
- Unit coverage:
  `cmd/serf-tui/pending_test.go:TestHubModel_SteerFailsFastOnRPCUnavailable`
  and `cmd/serf-tui/internal/pending/pending.go:53`
  (exported `PendingCoordinator`; the package was extracted out of
  `cmd/serf-tui/pending.go`)
- Underlying daemon gate: kata `wymv`
