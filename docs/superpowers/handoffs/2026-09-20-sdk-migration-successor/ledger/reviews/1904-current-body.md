An unreadable saved keybinding draft can be discarded from the native recovery screen even when the hub is offline or no live model exists. The screen uses the provider’s guarded storage action, preserves saving and uncertain-write protections, and protects readable replacement drafts through compare-and-swap discard.

Merge order: merged #1950 → merged #1956 → #1964 → #1988 → this PR. Current head `08dd625371c8f314b14514020edec9e951f0faa5` remains preserved until those immediate predecessors land. The same-client disconnected retained-projection finding is fixed by the isolated #1988 successor; it has not yet been integrated into this head.

Validation: 59 focused tests, native typecheck, package-import lint, diff checks, and independent screen review passed. The screen’s own patch is unchanged from the reviewed version. All three raw panel members at the current head completed and were read; two found no issues, and the remaining Medium is the provider connection-state correction in #1988.

Verification-read uncertainty, delayed live-model recovery, and offline retry-message wording remain separate Low follow-ups in #1941. Optional provider and discard-time failure coverage remains #1948. No Low fix is folded into this screen slice. It is held for predecessor integration and fresh current-head CI before merge.
