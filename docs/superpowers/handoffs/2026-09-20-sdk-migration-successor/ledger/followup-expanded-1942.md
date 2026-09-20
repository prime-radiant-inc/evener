The simplify reviews of #1915/#1922 identified repeated native store connectionChanged wiring, hub-scope reset bookkeeping, readiness-edge recovery callbacks, and connection fixtures in screen tests.

After the replacement readiness/banner stack and #1922 land, measure which duplication remains and extract the smallest helpers where the lifecycle contracts actually match. Preserve live hub/client identity checks, protocol-close walls, recoverable-flap banners, initial ready reads, automatic reconnect recovery, and offline Cancel/navigation.

Do not unify web/native semantics merely because the code looks similar or add a new lifecycle framework. The active readiness and fatal-close defects remain in the migration stack; this tracks the accepted non-blocking cleanup.


The independent reconnect-readiness C review at 4bf782b77e103c84864eedb8677520ff1de85c0c records two additional Low simplifications: a redundant ready dependency where the live predicate already carries authority, and repeated readiness checks around mutation entry. Preserve invocation-time client/hub identity checks and the actual deferred-confirmation regressions; do not replace live reads with stale captured booleans.
