The final raw review of #1916 identified a generated clientMutationId collision across active outbox, optimistic, and recovery stores. Both current adapters reject collisions within the outbox but do not check the other two stores. A colliding settlement can therefore overwrite or remove an older active record.

The current production IDs use secure UUID generation and producers require a fresh ID for every enqueue, so the collision is not an observed production failure. Native is still unwired. The current implementation matches the web oracle; do not add a native-only behavior change while claiming shared parity.

Qualify a shared invariant for ID uniqueness across all three active stores, implement its check atomically in web and native enqueue transactions, and cover collisions in each state plus sequence-allocation rollback with behavioral conformance tests. Preserve existing duplicate-outbox rejection and do not silently regenerate an ID unless that policy is explicitly chosen.

Evidence: #1916 at6776ea4fac57007bc1e6b2fdb87901438f7fb5db and #1917 at73fff46143348e8d88eb5d76909e604b396d03ef; native mutationOutboxStorage.ts enqueueIntent and web mutationOutboxIndexedDB.ts outbox.add use table-local primary keys. Related contract suite #1927.
