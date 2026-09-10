# SDK evidence handoff

The 2026-09-09 SDK evidence receipts were recovered from untracked evidence files in the default checkout; the preserved mobile concepts worktree supplied a read-only copy for recovery and sanitized for publication in the current main based branch.

Included:

- `assets/2026-09-09-sdk-running-work-outcomes.json`: bounded task, job, output, pagination, and fallback read outcomes.
- `assets/2026-09-09-sdk-shutdown-promote-outcomes.json`: rejected superseded promotion attempt, retained to prevent treating it as acceptance evidence.
- `assets/2026-09-09-sdk-upgrade-current-outcomes.json`: accepted bounded installed upgrade outcome.
- `assets/sdk-upgrade-current-driver.mjs.txt`: redacted historical driver recipe.
- `sdk-upgrade-current-report.md`: concise historical qualification report.

These are historical source evidence, not current SDK qualification. Private paths, session and process identifiers, auth-token locations, and runtime credentials were removed; hashes and behavioral outcomes were retained. The receipts remain bound to the source commits and artifact identities recorded inside each file.

The rejected shutdown receipt now has its sanitized `supersededBy` final receipt included alongside it. The original source file hashes were not present in the default checkout at recovery time, so the receipts retain their embedded source and artifact hashes rather than inventing an original-file hash.
