The independent #1917 review at6449f12662c69a9b8d060701de2c5bc0d0ef6cae identified non-blocking read-path cleanup in mutationOutboxStorage.ts:

- nextDispatchable decodes all target rows before selecting the lowest sequence; inspect selecting one ordered row directly.
- restoreProvenAbsent decodes all target rows and updates restored rows one at a time; reduce work if a clear query preserves the atomic rollback contract.
- listTargetRefs repeats DISTINCT under UNION and then sorts; preserve the web adapter's exact lexical ordering if moving ordering into SQL.
- list duplicates its scoped/unscoped query strings.

Keep this a small follow-up after #1916/#1917. Preserve blockedUnknown ordering, cross-target independence, authoritative-ID filtering, and rollback behavior with the existing behavioral tests. Shared conformance is #1927; SQLite test fixtures are #1928; record builders are #1929.
