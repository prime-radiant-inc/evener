# Issue 4904: nested delegate visibility for its creator and ancestors

## Scope and acceptance

Resolve [issue #4904](https://github.com/prime-radiant-inc/evener/issues/4904)
without changing the delegate tool's schema, delegate creation, notification
timing/arming/packets, or control authorization. A delegate that was created
successfully is visible through `job_list(type=["delegate"])` and addressable
through `job_status(target=dlg_...)` from the session that created it, and it
appears in that session's ancestors'
`job_list(include_nested|include_descendants)` rows, independently of whether a
completion notification was ever delivered. The only delivery-path effect is
that a delegate parent now resolves its own child instead of treating it as
unreachable (see Change).

Reproduced from the creating session transcript: a delegate with delegation
allowance 4 created two reviewer delegates with `delegation_allowance: 0`. Both
ran and delivered reports, but the creator's `job_list` returned `No jobs.` and
`job_status` returned `not_controllable: stable delegate ... is not visible to
this session`; the tree root could not see either row in its descendant tree.
Its delegate journal shows why: each row carries
`owner_session_id = <tree root>`, `parent_delegate_id = <creator delegate>`, and
`visible_session_id = <creator session>`.

## Cause

`ReserveCreate` (`agent/delegate_tree_start.go`) records `OwnerSessionID` as the
controller's root session — the physical root that admitted the runtime, as
`sessionActivityDelegateOwner` documents. The descriptor's `VisibleSessionID`
records the session that ran the create.

Two filters treat `OwnerSessionID` as the creator:

1. `delegateRowVisibleTo` (`agent/delegate_tree_controller.go`) accepts a base
   row only when `OwnerSessionID == "" || OwnerSessionID == s.id`. For a
   root-created row these coincide. For a delegate-created row the owner is the
   root, so the creator's own roster is empty and `job_status` (which uses the
   same rows with descendants) refuses the id.
2. `stableDelegateRowsForSession` (`agent/session_tools_jobs.go`) walks the
   parent edges to add descendants but skips any row whose
   `VisibleSessionID != s.id`. Every descendant of a delegate was created by
   that delegate, never by the ancestor, so an ancestor's descendant listing
   stops at its direct children.

`delegateRowVisibleTo` is also the direct-child lookup used by watch delivery
(`directStableDelegateForChildSession` via `childResumable` and the child
drive/drain gates), so the same misreading prevents a delegate session from
resolving its own child as resumable or gated. Its other caller,
`hasWakePendingDelegateFor`, feeds the goal wake-pending hold; delegate-child
sessions never wire `kickFunc`, so that call site is alignment with the shared
predicate and has no observable delegate-session hold effect.

## Change

- Give the predicate the descriptor's `OwnerSessionID` and
  `VisibleSessionID`. Parentage remains `parentID == s.owningDelegateID`; no
  authorization changes. A nested row (`ParentDelegateID != ""`) is visible
  once that parentage matches: the parent delegate id belongs to one creator,
  so the row came from s's delegation chain even when a non-delegate subagent
  running under s ran the create. A root-slot row is visible to the physical
  root (`OwnerSessionID`) and to the session that ran the create
  (`VisibleSessionID`, or `OwnerSessionID` when the older field is empty).
- Pass both descriptor fields at every production call site:
  `stableDelegateRowsForSession`, `hasWakePendingDelegateFor`, and
  `directStableDelegateForChildSession`. Update the test-side mirror in
  `delegate_resource_supervision_test.go`.
- Remove the `VisibleSessionID != s.id` skip from the descendant walk. The walk
  starts only from rows the session can already see and follows
  `ParentDelegateID` edges, so every row it reaches is a true descendant; the
  skip is what hides a child delegate's own delegates from its ancestors.

Base listings keep their existing meaning: a session's plain `job_list` shows
its direct delegates, and descendants require `include_nested` or
`include_descendants`.

Four production call sites of `stableDelegateRowsForSession`, plus
`directStableDelegateForChildSession` (which shares the predicate), see the
corrected rows for a delegate session, each intentionally:

- `job_list` (`session_tools_jobs.go`) and `job_status` now show the creator's
  own delegates.
- `populateDelegateStopEvidence` now fills resumable/scratch/worktree evidence
  for `job_stop` on the creator's own delegate.
- `markDrainAbandonedDelegates` (`session_jobtree_drain.go`) now tracks the
  creator's own child delegates during its process-end drain, matching the
  file's stated rule that `DrainJobTree` runs for nested delegates too.
- `directStableDelegateForChildSession` (`job_watch.go`) now resolves the
  creator's own child, so `childResumable` and the child drive/drain gates see
  the real row instead of a missing one. Notification timing, arming and
  packets are unchanged; what changes is that a false "child unreachable"
  fallback is no longer produced for a delegate parent and a stopped child is
  no longer driven.

## Non-goals

No change to descriptor fields, `ReserveCreate`/`CommitStart`, delegate creation
limits, `stableDelegateOwnedBySessionLocked` control rules, `job_stop`/`delegate_send`
authorization, notification timing/arming/packets, shell-job visibility, the
delegate tool schema, or session-activity projections. The remaining
`OwnerSessionID`-as-creator readers in jobs activity and the worktree disposal
hint are filed as
[issue #5012](https://github.com/prime-radiant-inc/evener/issues/5012).

## Verification

Deterministic, in-process, scripted-provider test: the canvas fixture
(`newRealDelegateTree`) has a root session create a delegate whose scripted run
creates a grandchild through the real `delegate` tool, so the grandchild's
descriptor has root-owned, delegate-parented, creator-visible lineage.

1. Red before the change: creator `job_list(type=["delegate"])` is empty, creator
   `job_status` returns the not-visible error, ancestor
   `job_list(include_nested, type=["delegate"])` omits the grandchild, ancestor
   `job_status` for it fails, and the creator's `childResumable` lookup fails.
2. Green after the change, same test. Also cover root cold restore: rebuild the
   root session from its persisted meta and journal and assert the ancestor's
   descendant listing and `job_status` still reach the grandchild.
3. Preserve the base-roster assertion (root's plain list shows only its direct
   delegate) and run the focused suites:
   `go test ./agent -run 'Test.*(Delegate|Job|JobList|Visible|Tree)' -count=1`,
   `go test ./agent -run 'Test.*(Drain|Supervision|Child|Goal|Watch)' -count=1`,
   `go test ./agent -run 'TestDelegateVisibility_' -race -count=1`, and
   `make build`. CI owns the full suite and its race lane.

## Round 1 PAR outcomes

Two independent reviewers verified the frozen hash and reproduced the red test.
Applied: corrected the new test's expected row order (a sorted UUIDv7 id list
cannot be compared with a reversed literal); named the extra readers and their
intended effects; corrected the goal-gate claim to alignment-only; restored the
legacy empty-`VisibleSessionID` fallback to `OwnerSessionID`; updated the stale
`VisibleToSessionID` comment; widened the named verification. Refuted as
out-of-scope for this change: jobs-activity attribution and the worktree
disposal hint, filed as #5012.

## Round 2 PAR outcomes (implementation diff)

Two independent reviewers verified the diff, test and spec hashes, and both
proved the new test red on pre-change source (a `-overlay` run) and green after.
Applied: nested rows now key on logical parentage alone, so a row created by a
non-delegate subagent under a delegate stays visible to (and controllable by)
that delegate; root-slot rows stay visible to the physical root as well as
their creator; the reader count is corrected to four call sites plus the
shared-predicate child lookup; and the notification scope names the one
delivery-path effect instead of denying any. One reviewer's wider run showed
four failures that reproduce on pre-change source and come from the read-only
sandbox's missing skill cache and an unrelated memory fixture, not this diff.
