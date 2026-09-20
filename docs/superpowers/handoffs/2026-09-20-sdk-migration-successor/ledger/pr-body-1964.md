## Result

Offline preference-draft recovery now updates the retained snapshot for the current hub while preserving unrelated hub and load errors. Native projection keeps the existing draft, hub, and load diagnostic sources separate, so recovering a readable or absent draft clears only its draft error. Late work from another hub or client cannot update the retained view.

## Scope and stack

This retained-snapshot slice is stacked on #1950 and #1956; #1904 provides the recovery screen. It is the final provider portion replacing #1902, with storage, provider operations, retained projection, and screen behavior kept in separate PRs. The current retained slice also carries the measured 17-line truth-table correction. The old #1902 branch remains superseded and preserved separately.

## Validation and status

The provider, native preference, draft-storage, and focused shared tests recorded for this slice pass, along with native typecheck, package-import lint, and diff checks. Production reversal reproduced the diagnostic-source regressions. Independent spec/quality/simplify review passed, and local RoboRev2596 passed with the remaining screen-consumption seam assigned to #1904. Fifty-nine screen integration tests passed at local screen head `e8de`; that #1904 screen head is not yet pushed. The measured live-model recovery delay remains Low #1941, recoverable through the existing screen action; additional provider load-error coverage remains #1948.

The coordinator's `offline-refresh-6cf.json` receipt records the current target `main` at `d25d5afaa7fee061801258c6b64fee558d958bb3`, this head at `0674003fc26f3584a42fae7e229403f986d10c1b`, and the reviewed own-patch proof SHA-256 `d63b027c04b6d14a7453c693fc0f2de7de932d1588a97c014575f2f7138ab235`. Current CI and the raw review panel are still pending; Fresh exact-head CI and complete raw-panel qualification are required before landing. Screen qualification remains a separate #1904 dependency.

Current main includes #1975. This merge-only refresh changes only the inherited retirement test; the entire owned patch is byte-identical. Current-head CI and complete substantive raw member reviews remain required.
