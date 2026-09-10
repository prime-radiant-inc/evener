# SDK evidence handoff

The 2026-09-09 SDK evidence receipts were recovered from the preserved mobile concepts worktree and sanitized for publication in the current main based branch.

Included:

- `assets/2026-09-09-sdk-running-work-outcomes.json`: bounded task, job, output, pagination, and fallback read outcomes.
- `assets/2026-09-09-sdk-shutdown-promote-outcomes.json`: rejected superseded promotion attempt, retained to prevent treating it as acceptance evidence.
- `assets/2026-09-09-sdk-upgrade-current-outcomes.json`: accepted bounded installed upgrade outcome.
- `assets/sdk-upgrade-current-driver.mjs.txt`: redacted historical driver recipe.
- `sdk-upgrade-current-report.md`: concise historical qualification report.

These are historical source evidence, not current SDK qualification. Private paths, session and process identifiers, auth-token locations, and runtime credentials were removed; hashes and behavioral outcomes were retained. The receipts remain bound to the source commits and artifact identities recorded inside each file.
