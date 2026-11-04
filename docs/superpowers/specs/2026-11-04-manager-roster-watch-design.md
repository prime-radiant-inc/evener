# Global Manager roster watch

**Decision:** A Manager can subscribe to its hub's entire top-level session
roster. Project scoping is parked and is not part of this feature. The roster
is not a project picker or a new authority for target operations.

Register one aggregate `job_watch` with
`{"operation":"create","hub":{"scope":"top_level"}}`. The hub uses the
existing Manager observation binding, not a per-target fanout or a new queue.
The first delivery is a full snapshot of the current top-level population. It
excludes the Manager's own workspace ref, even after Clear changes its session
ID. Each entry includes a source-qualified `ref`, `level` (`working`,
`needs_you`, `error`, or `idle`), event kind, and structured `askPending`,
`approvalPending`, and `needsResponse` flags. The fixed meaningful event kinds
are `new`, `needs_you`, `error`, and `disappeared`; the initial snapshot can
also describe a current idle state. Quiet changes do not notify. A per-target
watch held alongside the roster watch can wake the Manager twice for one
change.

The broker publishes ordered revisions. The daemon deduplicates revisions,
accumulates changes to distinct refs while delivery is pending, and replaces
an earlier pending entry for the same ref with its latest state. A later
change must not erase an unrelated pending ref. After an interruption or lost
revision, re-register for a fresh full snapshot rather than guessing from the
last delta. The frame contains structured entries, not a synthesized summary
or badge count. Discovery (`job_list` with `hub.mode="discovery"`) is a secondary
read and may be unavailable independently of the watch.
