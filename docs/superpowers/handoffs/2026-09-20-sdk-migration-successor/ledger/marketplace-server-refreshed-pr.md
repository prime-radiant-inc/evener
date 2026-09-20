Marketplace removal now saves unregistration before deleting clone files. A failed save leaves the clone intact; failed clone cleanup returns a typed applied outcome so callers can distinguish a removed registry entry from a refused operation. Unknown names return before deriving or touching a filesystem path.

The hub includes the authoritative marketplace snapshot when available and marks it unavailable when the follow-up read fails. The CLI reports that removal applied but clone files remain. Caller-facing errors do not expose filesystem paths.

This is the server portion of #1890, reduced to 115 non-test changed lines. The client reconciliation replacement follows separately and must be ready before this PR merges; #1897's migration follows that client change.

Validation at `afbf897deb43931066fb8841d5a8463464c77d97`:
- Scoped manager, hub, AppWire, and CLI tests, including save failure, clone cleanup failure, missing lookup, and unavailable reconciliation, pass.
- Formatting, normal/tagged/Windows vet, and touched-scope lint pass.
- Independent spec, quality, and simplification review found no must-fix issues. The lookup-miss test uses a non-destructive filesystem recorder.
- Local RoboRev branch review (job2549) reports only the documented client-consumer seam, assigned to the immediate follow-up.

Current-head CI and raw remote panel review remain required. JSON-RPC round-trip coverage for the applied payload is a Low follow-up.


Refreshed server head 2d8d94a8df0e308149293f2e97a773546ef4d599 includes main 9cb596336f6b33fa61606f950b99dbf3829a9222 with the production patch unchanged (stable patch ID 3d3845fb7b40291af88a6f2fd0c121ac4dee74f9). Scoped tests, vet variants, lint, formatting, and diff checks pass. The raw server panel's client-state concern is addressed by the client replacement, while the measured presentation/retry concern is a separate required web/native/TUI consumer follow-up; hold this stack until those consumers are qualified. The deliberate consume-then-rethrow contract remains intact. Raw-panel logging Low is #1951; serialization coverage is #1944.
