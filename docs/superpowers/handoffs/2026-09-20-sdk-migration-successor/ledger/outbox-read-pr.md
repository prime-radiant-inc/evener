Adds the read half of the native SQLite mutation-outbox adapter: ordered discovery, accepted/recovery reads, next dispatchable selection, and transactional restoration of mutations proven absent by authoritative state. The class now declares conformance with the complete shared storage port.

Stacked on #1916. Own non-test scope is 100 changed lines (83 additions, 17 deletions). Parent allocator/index/null-display fixes are preserved. Accepted records intentionally omit composerText, matching the web adapter; blockedUnknown records prevent dispatch only on their own target.

Validation: 27 focused storage behavior tests, native typecheck, package-import lint, and diff checks pass. Independent review/simplification and local RoboRev2551 passed at 6449f12662c69a9b8d060701de2c5bc0d0ef6cae. The refresh to main 9cb596336f6b33fa61606f950b99dbf3829a9222 and parent 6776ea4fac57007bc1e6b2fdb87901438f7fb5db leaves the own read patch byte-identical (SHA-256 aea4b58127102b73bc161ee072dede792be3df5597c19dd4cfe8aa39b54febab); final head 73fff46143348e8d88eb5d76909e604b396d03ef passes the same gates.

Query cleanup Lows are #1945; conformance, fixtures, and accepted-record construction remain #1927, #1928, and #1929. Measured separate-handle contention and shared-notes recovery semantics are #1937/#1938. Merge only after #1916 and current-head CI/raw-panel disposition.
