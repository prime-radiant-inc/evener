# SDK evidence handoff

The 2026-09-09 SDK evidence receipts were recovered from untracked evidence files in the default checkout; the preserved mobile concepts worktree supplied a read-only copy for recovery and sanitized for publication in the current main based branch.

Included:

- `assets/2026-09-09-sdk-running-work-outcomes.json`: bounded task, job, output, pagination, and fallback read outcomes.
- `assets/2026-09-09-sdk-shutdown-promote-outcomes.json`: rejected superseded promotion attempt, retained to prevent treating it as acceptance evidence.
- `assets/2026-09-09-sdk-upgrade-current-outcomes.json`: accepted bounded installed upgrade outcome.
- `assets/sdk-upgrade-current-driver.mjs.txt`: redacted historical driver recipe.
- `sdk-upgrade-current-report.md`: concise historical qualification report.

These are historical source evidence, not current SDK qualification. Private paths, session and process identifiers, auth-token locations, and runtime credentials were removed; hashes and behavioral outcomes were retained. The receipts remain bound to the source commits and artifact identities recorded inside each file.

The rejected shutdown receipt now has its sanitized `supersededBy` final receipt included alongside it. The original default-checkout file hashes observed during recovery were: running-work `e7c517ed1ca3bcabb0bbbf8a67b3447ef61ea79925436999e2ddc7e3614a054f`, rejected shutdown `6354035d76560703d5e73bb0b326daac4f4637c974dc0cad0770bfb14021b866`, upgrade-current `66c50d671f6709e85fb8b7028f26d56bbd5687860970808ae6fff064381044f7`, driver `32ec03ae9c41a74fc34a055f9567e4bd95d7168de75159e31487c2bfb973d7ed`, and original report `00789b77ee44b15dd5f493f069faa8ffa7c54e434868b149302611e1ee81f1c4`. The sanitized published files intentionally have different hashes.
