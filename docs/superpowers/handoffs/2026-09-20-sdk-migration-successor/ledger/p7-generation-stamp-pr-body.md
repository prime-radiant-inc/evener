Shortcut drafts need to retain the ready generation in which they were created or confirmed so reconnect handling can distinguish stale drafts even when the server reuses a revision number. This first small replacement for #1792 adds the typed generation stamp and preserves it through editing and saving; ordinary storage restore starts unstamped, and authoritative reads or explicit rebase earn a generation.

The generation conflict fence and identity-aware storage recovery are separate reviewed successors. This PR does not claim the complete reconnect fence on its own.

Validation: 120 focused tests, production-only reverse-patch falsification, TypeScript build, package qualification, import lint, and scoped Biome passed. Independent correctness/simplification review and local RoboRev #2638 passed. Publication refresh preserves the owned change; current-head CI and the remote raw review panel must settle before merge.
