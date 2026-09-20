Offline preference-draft recovery now updates the retained snapshot for the current hub while preserving unrelated hub and load errors. Native projection keeps the existing draft, hub, and load diagnostic sources separate, so recovering a readable or absent draft clears only its draft error. Late work from another hub or client cannot update the retained view.

Stacked on #1950 and #1956; #1904 provides the recovery screen. This is the final provider portion replacing #1902, with 140 non-test lines. Storage, provider operations, retained projection, and screen behavior remain separate PRs.

Validation: 13 provider tests, 28 native preference tests, 41 draft storage tests, and seven focused shared tests passed, along with native typecheck, package import lint, and diff checks. Production reversal reproduced the diagnostic-source regressions. Independent spec/quality/simplify review passed. Local RoboRev2579's screen-consumption finding is the #1904 dependency; the measured live-model recovery delay is Low #1941, recoverable through the existing screen action. Additional provider load-error coverage remains #1948.

Refreshed head `a13be3c214ab9cb0da38fd187f2c4281f3a3e05f` preserves the reviewed own four-file patch byte-for-byte (SHA-256 `8330b66bb838269c07c8b8ad30cb1b51ee02c9797c62514edab06ae1185c9dcd`). Fresh exact-head CI is required before landing.
