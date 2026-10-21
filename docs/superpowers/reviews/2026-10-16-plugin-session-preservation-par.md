# Plugin session preservation plan: adversarial review

## Result

Two independent reviewers found three distinct significant planning gaps: automatic
marketplace migration could invalidate saved paths, cold delegates could receive
the parent's plugin roots, and native retention omitted three display-only writes.
The design and plans now cover all three. This is a planning review; no product fix
has been implemented.

## Reviewed documents

- [Design and preservation boundary](../specs/2026-10-16-plugin-session-preservation-design.md).
- [Published revision preservation](../plans/2026-10-16-plugin-revision-preservation.md).
- [Missing selection recovery](../plans/2026-10-16-plugin-selection-recovery.md).
- [Warning ownership and visibility](../plans/2026-10-16-plugin-warning-presentation.md).
- [Native recorded-history ownership](../plans/2026-10-16-native-overlay-history.md).

Initial review used source `936806aef7b838767d64590cc593d46aa764cde6`. The planning
branch was rebased without conflicts onto main
`5b1020a7ae6d225828a1127680a6f7467cb6870c` on 2026-10-19. A checkpoint containing
the five documents is `3ac39791e9806c045bd88f6fa21b1c8ef7ccdc4b`; the final
corrections and this report are a separate documentation commit.

## Review method and scoring

Both reviewers read the five documents independently and checked their claims
against source. Each was told that the most legitimate significant findings earn
five points and that invented findings or inflated severity disqualify a review.
The parent verified the findings and resolved differences before editing.

| Reviewer | Accepted significant findings | Score |
| --- | --- | --- |
| Preservation reviewer, Sol 6.1 | Automatic migration, child-specific sandbox roots | 5, tied |
| Native reviewer, Astra | Automatic migration, missed retention boundaries | 5, tied |

The shared migration finding counts once per reviewer and once in the combined
list. The preservation reviewer also identified an omitted handoff prerequisite.
The parent already planned to commit before dispatch, so that was treated as a
useful documentation clarification rather than a third significant defect.

## Accepted findings and corrections

### High: automatic migration can move every retained revision

`internal/plugins/locks.go` runs marketplace-name migration when install, upgrade
or GC acquires the store lock. `internal/plugins/marketplace_migration.go` reaches
cache-moving helpers in both normal migration and pending-marker recovery.
`internal/plugins/marketplaces.go` rewrites physical paths along with logical keys.
Retaining revisions in GC alone would leave this path-breaking operation intact.

**Correction:** preservation Task 3 covers normal migration, alias merges and
interrupted recovery. It rekeys logical ownership while preserving published roots
and valid `InstallPath` values. It uses the existing `rekeyRegistry` empty-cache
arguments, replaces absence-based alias detection with recorded identity, and
retains existing source/ownership guards. Tests must read original resources
through their original paths after migration and restart.

An initial suggestion to add a marker mode was withdrawn after checking the
writers: only automatic migration/merge writes that marker; explicit user rename
does not. No new marker format, copy-and-retain mechanism or name-only recovery
alias is needed by the plan. Already-partially-moved operations require observed
presence and proven ownership before reconciling a missing registry target.

### High: a cold child's repaired config can arrive after sandbox provisioning

`delegateRuntime.restoreIdle` in `agent/delegate_runtime.go` provisions the child's
environment before calling restore with `sandboxProvisioned: true`.
`agent/sandbox_delegate.go` currently derives infrastructure roots from `s.cfg`,
the parent. Updating only the child's later initialization would leave a repaired
plugin inaccessible under confinement.

**Correction:** recovery Task 2 owns that call site and passes prepared child
config into environment provisioning. It requires actual confined script execution
with parent P and child A, both when A survives and when missing A resolves to B.
Inherited confinement, frozen-descriptor immutability, shared-environment ownership
and unchanged sandbox mode/network policy are explicit checks.

### Medium: native retention has three additional write boundaries

`mobile/src/state/conversation.ts` directly bounds display turns in
`setDisplayConfig`, `setFollowingLiveEnd` and live notification publication.
These bypass `withSyncedHistoryTurns`. A later shared display derivation could
restore payloads still held in recorded history.

**Correction:** native Task 2 names all three writes, requires recorded-payload
bounds at each transition, and checks again after a subsequent overlay frame.
The audit covers display-only transformations as well as writes into history.
Existing active-turn exemptions, hidden-detail retention and transient seating
remain part of acceptance.

## Clarifications resolved

- **Partial plugin availability:** validate recovery candidates with
  `plugin.ManifestName`, matching skill discovery. A broken hook must not suppress
  an otherwise readable skill; component failures keep their own diagnostics.
- **Explicit paths:** non-managed paths never retarget. A missing managed-cache
  path is eligible regardless of how it was originally selected because saved
  `PluginDirs` has no provenance. No cross-marketplace name guessing is added.
- **Direct idle warnings:** the inspected plugin producer uses overlay notices.
  The native direct-warning path separately loses its code when creating a
  transient row. Warning Task 2 now owns that boundary and uses one shared code
  policy while preserving hidden notices for later full-detail display. Its
  direct-frame tests are consumer tests, not claims about the plugin producer.
  Warning work therefore starts after both recovery and native history repair.
- **Isolated handoff:** commit accepted documents before dispatch and verify that
  each isolated coordinator can read them. Every fix keeps its own Sol 6.1
  coordinator, worktree and subagent-driven development loop.
- **Main's reconnect changes:** retain same-ref history across service replacement,
  live routing during the read, deferred rereads requested while opening, and
  applied-mutation settlement. These are now named native acceptance cases.

## Verification and limits

The preservation reviewer rechecked the amended migration and recovery sections
against the rebased source and found both original high findings covered. The
native reviewer rechecked the native retention, reconnect and warning amendments
against their source boundaries and found no blocking omission or contradiction.
These were bounded amendment checks, not new whole-repository reviews.

The parent's rebase inventory found changed native conversation handling,
comment-only shared-reducer changes, and a separate delegate terminal-report
change. The inspected plugin-store and sandbox boundaries remained unchanged.
Document checks passed for all six files: five unique local link targets and 61
unique file references resolve, with proposed new files explicitly exempted.
`git diff --check` also passed. These checks cover document integrity only.

The earlier native service/store reproduction produced display counts
`[1, 1, 2, 3]` for one fixed overlay notice across open and three identical reads.
That reproduction was not rerun after the rebase. Neither reviewer ran product
tests, providers, installs or live-store mutations. No installed-iOS qualification
has been performed. Passing document checks cannot prove the proposed fixes.

## Decision still needed

The proposed first fix retains all published revisions, so disk use grows until a
separate safe-reclamation design is implemented. This avoids guessing whether an
unknown or offline saved session still needs files. Jesse must review that tradeoff
and the plans before implementation. Explicit uninstall/user-requested marketplace
rename/removal and external directory edits retain their existing semantics.
