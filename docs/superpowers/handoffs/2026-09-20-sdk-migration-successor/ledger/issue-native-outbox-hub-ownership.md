The unpushed native dispatcher slice d66c6d8fc51e837a1834f28a68e0d058a35eb8f3 accepts hubId but stores only the raw conversation targetRef. Same refs are valid on different hubs. After unregistering a target or restarting, another hub can register that ref and receive the first hub's durable pending action. The in-memory hub check does not protect persisted ownership.

Local RoboRev2593 and independent review confirm this High. Do not merge or expose the dispatcher until durable ownership and client binding are qualified.

The proposed narrow design is a native-only composite storage key JSON.stringify([hubId, ref]), with payload.ref kept as the raw wire ref. Client lookup, pending projection, and recovery lookup must use the same durable scope. The alternative is adding hub ownership to the shared outbox schema/ports across web and native. Jesse approved the native-only composite durable key on 2026-09-19, keeping the wire protocol unchanged. Shared schema changes are not authorized by that decision.

Validation must use a real SQLite adapter: enqueue on hub A, unregister/restart, register the same raw ref on hub B, and prove the action cannot dispatch there. Verify it dispatches only on the original hub and that registration uses a client owned by that hub. Preserve fresh secure mutation IDs and the single-handle contention boundary tracked separately in #1937.

The dispatcher slice is also being decomposed into runtime/client lifecycle and real native host wiring before further changes. Readiness wakeups and blocked/refused outcome consumers remain separate qualification holds, not reasons to weaken hub isolation.


Threat-model clarification: this is a demonstrated possible routing/correctness error with duplicate refs across hubs, not an observed incident or an established adversarial exploit. Tests should establish durable routing isolation without inventing a hostile-hub threat model. The review's High severity describes possible wrong-destination dispatch; it is not evidence of an attack.
