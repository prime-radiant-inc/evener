Adds the native SQLite mutation-outbox write adapter: enqueue, receipt settlement, optimistic records, recovery transfers, and rollback. Per-target sequence allocation now uses one UPSERT RETURNING statement within the existing savepoint; unique target/sequence indexes prevent duplicate persisted identity. Missing optimistic displays encode as JSON null so a valid intent can be enqueued without one.

This is the write half of the approved D25d storage split; #1917 adds the read methods and full storage-port declaration. Accepted records intentionally omit composerText, matching the web adapter. Repeating a clientMutationId rejects rather than overwriting an earlier enqueue.

Validation at `5a86de604d623a4183ec1ff8616d681ef8bc583f` after integrating main:
- 21 storage behavior tests pass, including allocation interleaving, constraint/rollback, absent display, and receipt settlement. Production-only reversals reproduced the allocation and encoding failures.
- Native typecheck and package-import checks pass.
- Independent spec, quality, and simplification review found no must-fix findings.
- Local RoboRev branch review passed with no findings (job 2543).

The PR is exactly 400 non-test added lines, retained as one storage mechanism. Separate-handle contention is measured and tracked in #1937; the shared-notes recovery-port follow-up is #1938. Existing simplification follow-ups remain #1927, #1928, and #1929. The earlier unrelated daemon timer CI failure is #1936. Current-head CI and raw remote panel review remain required before merge.


Raw-panel disposition at 5a86de604d623a4183ec1ff8616d681ef8bc583f: Muse and DeepSeek found no issues. Luna's High claim that expo-crypto 57.0.2 lacks getRandomValues is refuted by the installed, lockfile-resolved package: build/Crypto.d.ts line 55 declares the generic API, and build/Crypto.js lines 127–128 export it and delegate to the native module. Current-head native CI passes. GLM's accepted-row shape Low is tracked in #1929; full-intent persistence and recovery-settlement coverage are tracked in #1927. All 15 current-head CI checks passed. The refresh head 6776ea4fac57007bc1e6b2fdb87901438f7fb5db merges main 9cb596336f6b33fa61606f950b99dbf3829a9222 with a byte-identical own patch (SHA-256 8a1761fd7d9fc66451884bd6db56a41491a12604324539b3ae5d9d743111b427). The 21 focused tests and native/import checks pass again; fresh CI is required at this head.
