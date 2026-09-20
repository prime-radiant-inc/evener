The simplify reviews of #1915/#1922 identified repeated native store connectionChanged wiring, hub-scope reset bookkeeping, readiness-edge recovery callbacks, and connection fixtures in screen tests.

After the replacement readiness/banner stack and #1922 land, measure which duplication remains and extract the smallest helpers where the lifecycle contracts actually match. Preserve live hub/client identity checks, protocol-close walls, recoverable-flap banners, initial ready reads, automatic reconnect recovery, and offline Cancel/navigation.

Do not unify web/native semantics merely because the code looks similar or add a new lifecycle framework. The active readiness and fatal-close defects remain in the migration stack; this tracks the accepted non-blocking cleanup.
