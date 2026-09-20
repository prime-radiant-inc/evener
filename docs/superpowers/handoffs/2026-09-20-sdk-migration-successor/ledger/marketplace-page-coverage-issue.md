The independent web consumer review at de0e96dbeeda548c99c4a3da7430c7c5b1217088 found no must-fix, but the existing tests do not directly deliver a typed applied-removal outcome after a client replacement or after the whole settings page unmounts.

Add focused behavior coverage for both lifecycle boundaries. Verify an old-client outcome cannot mark or block the new client, a sheet remount retains the current page/client no-repeat marker, and global warning behavior remains truthful. Preserve ordinary retry semantics and the existing SDK generation/list-revision fences. Reuse the current page test harness; avoid rendered-markup snapshots.

This is a Low follow-up to the marketplace warning consumer, separate from the current must-fix work. Related protocol boundary coverage: #1944; applied-array member validation: #1953.
