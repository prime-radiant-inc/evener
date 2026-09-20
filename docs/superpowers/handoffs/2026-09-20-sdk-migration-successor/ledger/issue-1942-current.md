The simplify reviews of #1915/#1922 identified repeated native store connectionChanged wiring, hub-scope reset bookkeeping, readiness-edge recovery callbacks, and connection fixtures in screen tests.

After the replacement readiness/banner stack and #1922 land, measure which duplication remains and extract the smallest helpers where the lifecycle contracts actually match. Preserve live hub/client identity checks, protocol-close walls, recoverable-flap banners, initial ready reads, automatic reconnect recovery, and offline Cancel/navigation.

Do not unify web/native semantics merely because the code looks similar or add a new lifecycle framework. The active readiness and fatal-close defects remain in the migration stack; this tracks the accepted non-blocking cleanup.


The independent reconnect-readiness C review at 4bf782b77e103c84864eedb8677520ff1de85c0c records two additional Low simplifications: a redundant ready dependency where the live predicate already carries authority, and repeated readiness checks around mutation entry. Preserve invocation-time client/hub identity checks and the actual deferred-confirmation regressions; do not replace live reads with stale captured booleans.

The independent recovery review at affd280cb also records Low follow-ups: ProviderEditor.save uses a disabled snapshot rather than an invocation-time readiness guard; fatal walls omit the compatibility error and offer an ineffective retry; AddMarketplace needs explicit not-ready outcome handling; the preserved tautological test may be removed only with its meaningful replacement coverage established. The original WIP test-deletion branch remains preserved. Modal-hidden reconnect status is a current must-fix in the recovery PR and is not deferred to this issue.

Local RoboRev2569 at57bfcbf15 adds a Low presentation follow-up: plugin-details Modal still covers the reconnect banner, so users dismiss details to reach retry. Unlike the editable provider/marketplace forms, no unsaved form input is lost; those form modals are fixed in #1922. Render the existing ConnectionStatus inside details and cover a disconnect while open. Also update the stale three-outcomes test comment to the current four-outcome contract.



Additional Low from the raw #1922 panel at 57bfcbf15: PluginsScreen.act and MarketplaceBrowser.act clear notices/errors before the live readiness check. A disconnected no-op should preserve existing diagnostic copy; test the blocked action with a pre-existing notice. Keep separate from the Lows-only qualified recovery PR.


Additional measured Low at #1922 head 590c9d62ea9a622b889f097ab51530a084351c43: HubSettingsScreen.tsx:172-188 has both a useFocusEffect keyed by canUseConnection/model/upgrade and useReconnectRecovery. A manual replacement retry changes the client and then reaches ready, so both effects can issue the same overview refresh and upgrade reconciliation. This is redundant work, not stale data or a correctness blocker. Consolidate only after preserving first-focus reads, passive reconnect, and manual replacement behavior in focused tests.
