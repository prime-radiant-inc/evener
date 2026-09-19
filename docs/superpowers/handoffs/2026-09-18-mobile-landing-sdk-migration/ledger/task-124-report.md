# Task 124 — PR #1145 RoboRev round 25

Status: done. Four commits, main merged, pushed, PR body appended.

- `3342549e2` ci: put the record's whole state table behind one function
- `2d7b94bb4` ci: stop the parent and the child writing the same record
- `34171642c` ci: give every attempt a marker of its own, and keep it visible
- `21572af9a` ci: refuse what cannot be signalled, and fail the spawn that cannot be recorded
- Pushed head: `73f10784c` (merge of `origin/main` @ `cd306af03`)

Shared reader: `stop_recorded_job RECORD GRACE MARKER_PREFIX`
```
(no file)      nothing to do
(empty)        wait GRACE for the spawn to name itself, then decide
pid:N:MARKER   one process in the caller's group: marker decides, then the group
               N may have become is stopped too
pgid:N:MARKER  the job's own group: survivors decide (the marker cannot speak for
               a child whose parent carried it)
survivor:N     already given both signals and still there: kept, not re-signalled
status 0 stopped/gone (record removed) · 1 not this caller's (removed) ·
       2 unconfirmed (kept) · 3 known survivor (kept); reason in pgroup_stop_reason
```
Both callers are thin: no record-state branch, group signal, or group-number `ps`
parse in either script.

High (installer descendants): `attempt group members before the trap: 4 ; after: 0
; scratch removed`.
Record race: `before record after the parent note: [pid:4242:…]` → `after:
[pgid:4242:…]` (copy with the parent's note paused between its two steps).
Marker: `evener-package-list-58884-1534332282`, record
`pgid:58890:evener-package-list-…`, child exit 7 → wrapper exit 7,
`pgroup_owned_by(live job, that marker) = 0`.

Gates: `bash -n` on all three before and after the merge; envvars PASS 0.52s, `.`
PASS 211.31s, agent PASS 82.59s; `make tools-golangci` real network plus the
slow-curl hand-run; `make lint-generated` PASS. Fixtures kept to the 122a rules
(own group, pgid-differs assertion, cleanup by group id, no pkill).

Concerns:
- My edit in `2d7b94bb4` silently deleted `pgroup_survivors`/`pgroup_survivor_report`
  (a replaced span). The gate stayed green because nothing on the ordinary path
  calls them; only the timeout hand-run caught it. Restored in `34171642c` and
  called out in that message. This is the second slip of the same shape this
  round — a note for whoever weighs #1205: this file has no automated coverage,
  and every guard here is one careless span away from silently vanishing.
- The wrapper no longer execs, so each attempt now costs one extra live process
  (a perl parent per bounded enumeration). Measured runs are unchanged, but that
  is a real shape change for anything counting processes.
