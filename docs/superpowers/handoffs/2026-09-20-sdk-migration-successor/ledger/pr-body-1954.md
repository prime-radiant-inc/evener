## Result

When marketplace removal succeeds but clone cleanup fails, the SDK consumes a usable applied snapshot through the original mutation revision and generation, then rethrows the original typed outcome so cleanup failure remains visible. List, loading, error, and browse-cache state settle together. An older failed request cannot overwrite a newer success, while a newer retracted failure can release an older applied snapshot.

`AppliedUnavailable`, `null`, and malformed list payloads do not become authoritative empty lists.

## Scope and stack

This is the SDK reconciliation successor for the remainder of superseded #1890, stacked on #1940. Own scope is 78 changed non-test lines across the revision helper and marketplace store. Presentation and retry handling remain consumer work: web #1960, TUI-A #1966 with its asynchronous ordering-B work in progress, and the native consumer still under review. The stack remains held until those consumers are qualified; #1897 remains the separate server migration successor.

## Validation and status

The 153 extension tests with deterministic race, reset, malformed-data, and failing-first cases pass. Frontend/native typechecks, package qualification, Biome/lint, and package-import checks pass. Independent review and simplification found no must-fix issues. Local RoboRev2556 passed the reviewed implementation.

The coordinator refresh to main `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` verified the current head `64db435f21928a000006c420b1b9b103d1ba0c30` preserves the own patch byte-for-byte (SHA-256 `45d75049486f12acc8a7adf5868cb74fed4f9b6a981e89d1817b5888ffcc3ae4`). Fresh exact-head CI and raw panel qualification are required before merge.

Follow-ups are #1953 for typed marketplace-row validation, #1944 for JSON-RPC round-trip coverage, #1951 for secondary server read-failure logging, and #1959 for late page/client outcome coverage.
