A disconnected native hub can retain the same client object. The draft probe then reports current storage contents while the retained preferences snapshot keeps stale draft rules or an old restore error. This allows contradictory draft messages during recovery.

Reconcile the retained snapshot for that same client whenever it is not ready. Both guards still leave a ready live model in control and preserve hub/client fencing. Own production change: two guard replacements; one provider regression covers readable → unreadable → readable → absent storage with the same client and verifies the ready-state guard.

Follows merged #1964; #1904 consumes the final recovery state in the screen. This isolates the connection-state correction from the retained-projection slice after its fifth product review round. The original #1964 patch remains unchanged.

Validation: provider 15/15, native preferences 28/28, draft storage 41/41, seven focused shared tests, native typecheck, package-import lint, and diff checks passed. Production reversal reproduced the stale-draft failure. Independent correctness/simplification review and local RoboRev2625 found no issues. All three remote raw members completed at the reviewed head af83b51: two found no issues, and the remaining screen-consumer finding is implemented in #1904. After #1964 merged, this branch was rebased to `9ffba9a3396d4639ae74a1f6f442539d7753fa9b` on main `d9f2cd953a6d8796abfed733b0bdfc0bfc35104d`. The complete owned binary patch is identical, SHA-256 `078593afb0b0e15e65510f1fb5ab82e6dd8434c5128fd8388386ba93a4d63939`; review carries under the handoff rule. Fresh current-head CI is pending.

The separate copy, genuine storage-port uncertainty, and optional coverage Lows remain tracked in #1941/#1948.
