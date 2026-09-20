# /simplify — #1897 (S3) @055565befb6c8eefaae2c478900dd6027c36a85a

Scope: reuse / simplification / efficiency / altitude only. Correctness is RoboRev's.
Reviewed: `git diff -M pr-1890-review..pr-1897-review` (3 files, +380/-24), read
against the whole stack's head.

---

## 1. `internal/plugins/marketplace_migration.go:465-468` — `migrationFileSaveFailed` IS the extraction of two existing boundary scrubs, and does not replace them — **must fix before merge**
At this stack's head the same function body exists three times with byte-identical
format strings:

- `install.go:57-58` — `"warning: saving %s failed: %v\n"` / `"saving %s failed; see the hub's log for detail"` with `registryFileName`
- `marketplaces.go:104-105` — the same two strings with `marketplacesFileName`
- `marketplace_migration.go:466-467` — the same two strings with a `fileName` parameter

The third is a parameterized generalization of the first two, added by this PR,
which leaves both originals in place.
**Cost:** three copies of one two-line helper ship together; the next store file
gets a fourth. #1896's item 1 ("`saveRegistry` … needs its own `saveFailed`-shaped
helper") and this PR's own description item 1 both claim `saveRegistry` is
unscrubbed — it has been scrubbed since S1, so the issue text is stale in a way
that hides the duplication.
**Simpler:** rename to `storeFileSaveFailed(fileName string, saveErr error) error`
(it is not migration-specific — it is the store-file boundary scrub), keep it where
it is or move it beside `saveMarketplaces`, and have `saveRegistry` and
`saveMarketplaces` return it. Net about minus six lines, no test changes: the
rendered strings are identical, so `TestInstall_SaveRegistryFailureNamesNoPath`,
`TestSeedDefaultMarketplaces_SaveFailureNamesNoPath` and the two new marker/record
tests pass unmodified.
**Severity:** must-fix before merge (duplicates an existing helper — the duplicate
and the original are both in the tree being merged).

## 2. `internal/plugins/marketplace_migration.go:1060-1067` — `markerAfterFailedMove` writes its scrubbed sentence twice and repeats S1's sentinel branch a third time
`"marketplace %q: moving its directories failed; see the hub's log for detail"`
appears in both arms, once with `: %w` appended; the `if errors.Is(err, sentinel)`
shape is the third copy of `saveFailed`'s and `storeChangeRollbackFailed`'s.
**Cost:** editing the message means editing it in two places six lines apart; the
"preserve whatever sentinel the cause carried" rule is now encoded three times and
a fourth site that forgets it loses `errors.Is` silently.
**Simpler:** build the sentence once —
```go
scrubbed := fmt.Errorf("marketplace %q: moving its directories failed; see the hub's log for detail", name)
if errors.Is(err, errRenameRollbackIncomplete) {
        return errors.Join(fmt.Errorf("%w: %w", scrubbed, errRenameRollbackIncomplete), m.markerLeftForRecovery("rename"))
}
m.removeRenameMarker()
return scrubbed
```
— or adopt `1889-simplify.md` finding 2 (wrap the already-scrubbed cause with `%w`),
which deletes the branch at all three sites.
**Severity:** follow-up.

## 3. `internal/plugins/marketplace_migration.go:325-332` and `:1036-1043` — the "rollback also failed" block, comment included, is pasted twice
Both sites are `if undoErr != nil { Fprintf(m.stderr(), "warning: marketplace %q:
rolling back its rename also failed: %v\n", …, undoErr) }`, preceded by the same
four-line comment about `restoreRename`'s path, differing only in `marker.From`
vs. `name`.
**Cost:** two copies of one decision (log `undoErr`, return only the scrubbed
cause) plus two copies of its justification.
**Simpler:** `func (m *Manager) logRenameRollbackFailure(name string, undoErr error)`
holding the nil check, the message and the comment; two one-line call sites.
**Severity:** follow-up.

## 4. `internal/plugins/marketplace_migration.go:249`, `:358`, `:501-502` — three sites say the same sentence twice, once with the absolute path and once with the bare filename
Each new scrub writes the message out for the log with `path` and again for the
wire with `renameMarkerFileName`, so the two can drift into disagreement
(`:249`/`:250` and `:358`/`:359` already word theirs differently).
**Cost:** six strings maintaining three messages.
**Simpler:** log the location once and separately — e.g. one
`m.logMarkerPath(path)`-style line, or log the wire error plus `path` (`"warning:
%v (the marker is %s)\n", err, path`) — so each sentence exists once.
**Severity:** follow-up.

## 5. `internal/plugins/marketplace_migration_test.go:2862-2944` — two 40-line tests identical but for their trigger
`…_SaveFailureWhoseOwnRollbackFailsNamesNoPath` and
`…_RecoveryRollbackFailureNamesNoPath` share every line — the same
`oldDir/newDir/oldCache/newCache`, the same `marketplaceRename` stub, the same
`marketplaceAtomicWriteFile` stub, the same four-path assertion loop — and differ
only in `plantRenameMarker` + `m.migrateStore(ctx)` vs. `m.ListMarketplaces(ctx)`.
Likewise `…_MarkerWriteFailureNamesNoPath` (`:2945`) and
`…_RecordWriteFailureNamesNoPath` (`:2976`) differ only in which filename constant
the stub matches and which constant the assertion wants.
**Cost:** ~60 duplicated lines in a 3900-line test file; a change to the injection
has to land in two places, twice.
**Simpler:** one table-driven test per pair — first pair a two-row table over
`{name string, trigger func(*Manager) error}`, second a two-row table over
`{fileName string, path func(*Manager) string}`.
**Severity:** follow-up.

## 6. `cmd/evener-hub/app_plugins_test.go:598-659` — the RPC-boundary test pins the *absence* of classification
`if _, ok := errors.AsType[appwire.WireError](err); ok { t.Fatalf("want it left
unclassified …") }` asserts that today's pass-through stays a pass-through.
**Cost:** the PR that eventually classifies `ErrMarketplaceUnregisteredCloneRemains`
(the reason S2 exported it) must edit this assertion, and the failure will read as
a regression rather than as the intended change.
**Simpler:** keep the two assertions that are the point — the sentinel survives to
the wire, the message names no path — and drop the "not a `WireError`" one, or
invert it into a comment. The real-permission setup (`chmod 0o555`, Unix/root
skips) is otherwise the right call: a genuine removal failure, no injected seam.
**Severity:** follow-up, low.

## 7. Altitude — the per-site layer is still needed, but six helpers now encode one rule, and the class is not closed
Answering the brief's question directly:

- **Is the per-site layer still needed once the boundary scrub exists?** For the
  *text*, only where a marketplace name has to appear (`saveFailed`,
  `storeChangeRollbackFailed`, `cloneRemovalFailed`, `markerAfterFailedMove`) —
  and the name is information the RPC caller supplied, so even that is arguable.
  For the *mechanism*, no: `saveFailed`'s log line re-prints an already-scrubbed
  string, so the per-site logging adds nothing the boundary did not already write
  (`1889-simplify.md` finding 2).
- **Would one helper close the whole class?** The bodies, yes: one
  `m.scrubbed(raw error, format string, args ...any) error` that logs `raw` and
  returns `fmt.Errorf(format, args...)` reduces all six to one line each, leaving
  only the sentences — about minus fifty lines of helper body and doc comment.
  But the *class* would still be open, because the leak has two halves and this
  stack only closed the write half:
  - the read half is untouched and names the absolute path in plain text on the
    same RPC paths: `marketplaces.go:73,77` ("reading/parsing
    `/abs/…/known_marketplaces.json`"), `registry.go:46,50,53`,
    `marketplace_migration.go:430,434,547,551`, `catalog.go:98,102`, `gc.go:66`.
    A corrupt or unreadable store file reaches `ListMarketplaces` with the path in
    it, which is the same defect #1700 describes, on the read side.
  - the two write-side residues #1896 already tracks (`recoverMarkedRename`'s own
    move failures, `EditMarketplace`'s `fail` closure when its rollback succeeds).
  So "closes #1700" overstates it: after this PR, one grep still shows eleven
  path-naming sites on the same wire.
**Simpler:** one `storeFileError(op, fileName string, err error) error` used by both
the load and the save side would make the invariant checkable by grep ("no
`fmt.Errorf` in `internal/plugins` interpolates a `storePath`") instead of by
per-site review. Worth a decision on #1896 (or a new issue for the read side)
rather than a fourteenth helper.
**Severity:** follow-up (plus: #1896 item 1 is obsolete and this PR's description
repeats it — both should be corrected so the read side does not get lost behind a
stale "saveRegistry is unscrubbed" note).

Also checked, no finding: `plantRenameMarker`, `plantMergeMarker`,
`plantLegacyMarketplace`, `installedAt`, `mustExist`, `renameMarkerFile` are all
reused rather than re-invented; the existing-test edits at `:2659`, `:2823`,
`:3376`, `:3831` are minimal (swap the absolute path for the constant, add a
no-path assertion) and leave no dead helper behind; nothing scans repeatedly.

---

## Verdict: fix before merge (7 findings, 1 must-fix)

One must-fix, and it is six lines: `migrationFileSaveFailed` is exactly the
generalization of the two boundary scrubs S1 added in `install.go` and
`marketplaces.go`, and this PR adds it as a third copy instead of calling it from
both. Renaming it `storeFileSaveFailed` and pointing `saveRegistry` and
`saveMarketplaces` at it removes the duplication with no test changes, because all
three render identical strings; it also invalidates #1896's item 1, which still
claims `saveRegistry` never scrubs. Everything else is follow-up: a scrubbed
sentence written twice in `markerAfterFailedMove`, a "rollback also failed" block
pasted twice with its comment, three log/wire sentence pairs, and two pairs of
40-line tests that differ only in their trigger or their filename constant. The
altitude answer is that one `scrubbed(raw, format, args…)` helper would collapse
all six helper bodies — but it would still not close the class, because eleven
read-side sites (`reading %s` / `parsing %s` in `marketplaces.go`, `registry.go`,
`marketplace_migration.go`, `catalog.go`, `gc.go`) still put the absolute store
path on the same wire; "closes #1700" should be reread before this merges.
