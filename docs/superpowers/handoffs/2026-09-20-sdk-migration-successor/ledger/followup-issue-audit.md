# Follow-up issue audit

Audit date: 2026-09-18. This is a read-only tracker audit for handoff #1934. Issue identities and open/closed state were checked through the repository Issues REST endpoint; no issues, comments, branches, or code were changed.

## Disposition

| lane | residual disposition | current tracker or conclusion |
| --- | --- | --- |
| #1902 | The unreadable-draft recovery and readable-replacement safety findings are active lane work. The remaining read/discard testkit, projection, duplicate-read, and swallowed-nudge-error items are one cohesive native recovery follow-up; Draft A below. | #1902/#1904 are the active seam; #1918 covers the separate fold/p6a draft-port lows and does not cover these p6b/p6c items. |
| #1915/#1922 | Reconnect/refetch, delayed readiness, fatal-close recovery, not-ready outcome, and `remove()` guard findings belong to the stacked successor. The remaining readiness/recovery deduplication is one follow-up; Draft B below. | #1922 is the current successor; its open issue body still describes the active findings. Do not create another product issue for them. |
| #1916/#1917 | Conformance is #1927; the shared SQLite test double/ports are #1928; accepted-record helpers are #1929; separate-handle contention is #1937; shared-notes recovery is #1938. The `composerText` omission is refuted by the state-note oracle. Remaining query/materialization and builder duplication is review-only cleanup, with no separate product impact found. | Existing #1927, #1928, #1929, #1937, #1938. |
| #1919/#1920 | Rehydrate coverage, chronological merge, and usage-footer accounting findings are in the current #1919 stack; the #1920 footer concern is superseded by that lane. Remaining merge/helper and usage-row simplification is Draft C below. | #1919/#1920 are current PR lanes. Do not duplicate their active findings. |
| #1906/#1907 | The live/restore `textEvidence` difference was measured with no live mismatch, so it is not an issue. Remaining scan/helper/test duplication is review cleanup, not a product residual. | No tracker needed for the measured asymmetry. |
| #1890/#1897 marketplace | Stale applied reconciliation and stale loading/error state remain active lane work. The orphan clone cleanup is #1923; the server/read-side path is #1896. The missing wire-level applied-payload coverage is genuinely untracked; Draft D below. | #1890/#1897 active PRs; #1923 and #1896 existing issues. |
| #1931 | The reducer scan-count and `it.each` parameter findings are already being tracked by the coordinator. | Do not duplicate the new warning-test follow-up. |
| #1936 | This is a CI/reset-race report, with no established product root cause. | Exclude from product residual tracking. |
| #1916 crypto report | Installed `expo-crypto` 57.0.2 does provide `getRandomValues`; the remote finding is refuted. | No issue. |
| #1925/#1930 | Existing C/E/D follow-ups for row-identity duplication and attachment-name truncation. | Do not duplicate. |

The review requirements, merge gates, CI reruns, and `implements MutationOutboxStorage` conformance requirement are lane completion work, not additional product issues. The D25d simplify items already represented by #1927/#1928/#1929 are covered; the remaining D25d items are low-level query/materialization cleanup and do not establish a user-visible defect.

## Draft issue bodies

These are exact small bodies for the four untracked residual groups. They are drafts only; no issue was opened by this audit.

### Draft A — Native offline keybinding draft recovery follow-up

**Title:** `native: consolidate offline keybinding draft read/discard follow-ups`

```text
After the #1902/#1904 unreadable-draft recovery seam lands, a few accepted low-priority items remain in the same native read/discard path:

- the testkit fake duplicates the raw-string draft backend;
- the value-bearing read projection and the exported decode helper are retained without a production caller;
- the provider carries the unreadable classification through multiple parallel values;
- discard performs several synchronous reads of the same draft; and
- the fire-and-forget live-model discard nudge drops a rejection silently.

Consolidate the test backend and read projection, keep one explicit unreadable outcome, reduce the repeated draft reads, and surface the nudge failure through the existing provider error/reporting path. Preserve the #1902 behavior for readable replacements and offline discard, including the unreadable signal needed by native.

Acceptance criteria:

1. The native recovery path has one test backend and one read outcome used by production callers.
2. A single discard operation does not re-read the same storage key for each derived predicate.
3. A rejected live-model nudge is observable through the existing error/reporting contract.
4. Tests cover readable replacement, unreadable draft, offline discard, and nudge rejection.

This follows #1902/#1904. It does not change the fold/p6a draft-port items tracked by #1918.
```

### Draft B — Connection readiness/recovery simplification

**Title:** `mobile: share retained-screen connection readiness and recovery plumbing`

```text
Once the #1915/#1922 readiness fixes are complete, the retained mobile screens still carry duplicate connection lifecycle plumbing:

- the connectionChanged wiring is repeated in Plugins and Marketplace;
- hub-scope reset bookkeeping is repeated in the connection-display and render-client paths;
- the recovery callback and became-ready edge are each hand-written in more than one place;
- web and native carry parallel readiness/fatal-close rules; and
- the screen tests duplicate the same connection fixtures.

Extract the smallest shared readiness/recovery helpers and fixtures while preserving the current #1922 behavior: a closed client is not reused after fatal close, a dropped connection cannot start a mutation, and stores refetch after reconnection. Keep this follow-up limited to the lifecycle plumbing; the active stale-readiness and not-ready findings remain in #1922.

Acceptance criteria:

1. Plugins and Marketplace share the connectionChanged/recovery wiring.
2. Hub scope is reset in one shared place with the existing semantics.
3. Readiness and fatal-close classification have one shared contract across web and native.
4. Tests exercise reconnect, fatal close, and dropped-connection mutation without duplicated fixtures.

This is a follow-up to #1915/#1922 and is not a replacement for their active product findings.
```

### Draft C — Turn merge and usage projection simplification

**Title:** `mobile: simplify retained-turn merge and usage projection helpers`

```text
After the #1919/#1920 merge and usage fixes, the remaining B3 cleanup is concentrated in the same retained-history projection:

- pageOwnedTurnIds is built but never queried;
- hasOlderTurns rescans with a second identity rule instead of using the merge result;
- rehydrate and loadOlder duplicate the merge operation;
- the instance-identity predicate is repeated; and
- usage accounting repeats the config flag, tuple filtering, and the undefined token-unit sentinel.

Collapse these into shared helpers and derive pagination state from the merge result. Preserve chronological ordering, page ownership, cursor continuity, and the usage totals for both tokenized and whole-session views.

Acceptance criteria:

1. Rehydrate and loadOlder use the same merge/identity helper.
2. hasOlderTurns is derived from the merged result without a second identity scan.
3. Usage projection has one accounting predicate and an explicit whole-session representation.
4. Existing #1919/#1920 cases for older fragments, fallback turns, cursors, and usage totals remain covered.

This is simplify follow-up work for the #1919/#1920 lanes; it does not reopen their active findings.
```

### Draft D — Marketplace applied payload JSON-RPC coverage

**Title:** `test(plugins): cover applied-with-litter payloads through the AppWire JSON boundary`

```text
The #1810 S2/S3 marketplace clone-litter path now returns MarketplaceUnregisteredCloneRemainsData with an applied payload. The current tests exercise the Go-side path, but do not cover both applied-payload states through the AppWire JSON-RPC boundary.

Add boundary tests for these two outcomes:

1. A successful follow-up relist serializes a non-nil applied.marketplaces array (empty when no marketplaces remain).
2. An unavailable applied listing serializes appliedUnavailable: true and preserves the zero applied.marketplaces value as null.

Verify the client decoder/reconciliation fixture consumes the successful array and refuses to treat the unavailable/null payload as an empty authoritative list. Keep the existing wire contract and error discriminator unchanged; this issue is test coverage for the applied payload and its unavailable state.

This follows #1890/#1897. The stale client publication finding remains in that active lane, orphan clone cleanup is #1923, and the server/read-side path is #1896.
```

