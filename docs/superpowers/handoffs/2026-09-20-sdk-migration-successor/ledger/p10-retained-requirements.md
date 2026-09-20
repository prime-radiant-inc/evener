# P10 retained requirements before closing superseded #1845

Source: live #1845 body at737b7cebd995baf0402d7a2f82569b4c219281a8, inspected against qualified local P10c c660aee9999ef0b0e80dcf53eac9d88e62a2a108. This is scope accounting, not evidence of landing.

- Wire decoder/error contracts: #2051 mergedcf85b5c62. Documentation Low #2053 mergedfded33afe; post-merge CI35486790423 passed.
- Hub defaults/read lifecycle, support, notifications, missed-before-confirmation refresh, lower-revision restart: P10b. Read publication reentrancy corrected in frozen parent0d57f3b88. Remaining review2691 direct-generation pending loading issue fixed by tiny successore2b62bb85; local RoboRev2692, full web/package gates passed. Both await PR CI and landing.
- Direct PATCH and per-layout previews, canonical conflict/post-apply state, malformed reply handling, current-value fenced returns, concurrent layouts, preview contradiction reconciliation: P10c. Existing behavioral tests cover these paths; real hub internal failures reconcile through authoritative GET. Inherits P10b correction before landing.
- Ready-generation first-payload fence: P10b additive fence API and tests; preserve keybindings behavior.
- Export/build surface: P10b already adds store root exports and build entry; do not duplicate in A1. Installed-package behavior qualification remains A1.
- Browser adoption is independently retained A5/A6; package exports alone do not complete migration.
- Explicit original deferrals remain A8/A9: checkpointed draft port, restore/stale/invalid checkpoint handling, draft/storage/error/conflict fields, generation stamping, save/discard/rebase, settledWrite/whyFenced and direct-write gating against checkpoint writes. Do not drop them when closing #1845.

Close #1845 only after retained P10 replacements are merged and verified. Keep A8/A9 open until implemented and verified separately. Existing local tests and reviews do not establish completion on main.
