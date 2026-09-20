# #1810 split state note (written at ~600k tokens, handing off)

Original PR #1810 (closes #1700) was decomposed into three stacked PRs. S1
(#1889) is squash-merged to main as `a81740a362d8c714953b59374cef86bb5dd6cdd4`.
S2 and S3 are rebased on top of that, opus /simplify's must-fixes applied,
gates green, dispositions posted. Both are merge-ready; a fresh lane should
merge S2 then S3 (in that order — S3 stacks on S2) once CI is green, then open
ONE follow-up PR from main covering everything below.

## Current heads

- **S1 #1889**: merged to main as `a81740a362d8c714953b59374cef86bb5dd6cdd4`.
- **S2 #1890** branch `claude/plugins-path-scrub-s2-remove-reorder`: head
  `0f058852583864f6cee1aa9e58956fe7d9f7cf20`. Rebased onto squash-merged main.
  0 must-fix from opus /simplify (`1890-simplify.md`). Round-3 RoboRev Medium
  (stderr logging the raw path) was refuted with #1700's own text quoted in
  the disposition comment — #1700's boundary is the wire, not the daemon's
  stderr. Nothing blocking merge.
- **S3 #1897** branch `claude/plugins-path-scrub-s3-migration-close`: head
  `fc7fd11d53915b96214a74837a2ac8f8ffbaca3b`. Rebased onto S2's new tip.
  Opus /simplify's one must-fix applied (`migrationFileSaveFailed` renamed to
  `storeFileSaveFailed`, `saveRegistry`/`saveMarketplaces` now call it instead
  of duplicating it — net -7 lines, identical rendered strings, verified via
  the 4 existing tests, no test changes). PR body corrected from "closes
  #1700" to "part of #1700" (eleven read-side leak sites remain — see #1896
  item 4 below). Nothing blocking merge.

Both PRs' dispositions are posted as PR comments; #1896 is updated (see below).
Neither branch has uncommitted changes or open stashes as of this note.

## The follow-up list (ONE PR, opened from main after S2+S3 merge)

Format: `file:line` — one-line fix. Line numbers are as reported by the
opus /simplify docs (`reviews/1889-simplify.md`, `1890-simplify.md`,
`1897-simplify.md` in this ledger dir) at the head cited in each; they will
have drifted by the time this PR is opened — re-locate by function/test name,
not by line, before editing.

### S1's 8 (`1889-simplify.md`), reviewed at #1889@0655494cb

1. ~~`install.go:56-59` + `marketplaces.go:104-105`~~ — **DONE**, folded into
   S3's must-fix (`storeFileSaveFailed` consolidation). No action needed.
2. `marketplaces.go:272-278` (`saveFailed`) — replace with one line:
   `func (m *Manager) saveFailed(name string, saveErr error) error { return fmt.Errorf("marketplace %q: %w", name, saveErr) }`
   (drop the `fileName` param and the hand-rolled `errStoreBetweenNames` branch;
   `%w` carries it for free). Verify wire strings and `errors.Is` unchanged at
   all 7 call sites; `marketplaces_path_scrub_test.go` should pass unmodified.
3. `marketplaces.go:291-297` (`storeChangeRollbackFailed`) — same fix as #2
   (wrap with `%w` instead of the if/else sentinel branch); collapses once #2
   lands, or extract one shared `keepSentinel(cause error, msg string, args ...any) error`.
4. `marketplaces.go:644` (the between-names branch inside `saveRename`'s
   caller) — replace the long composed-message call with
   `return m.saveFailed(name, errors.Join(err, restoreErr, errStoreBetweenNames))`
   (once #2 lands `saveFailed` takes 2 args, not 3).
5. `marketplaces.go:642-647` (`saveRename`'s two branches) — restore the lost
   "the store is back as it was" distinction on the clean branch (its own safe
   sentence, no path) so a caller can tell a clean refusal from a between-names
   failure by text again, not just `errors.Is`.
6. `marketplaces_path_scrub_test.go` (whole file) + `install_test.go:415-451`
   + `seed_test.go:55-80` — add two shared test helpers in
   `marketplaces_path_scrub_test.go`: `failWriteTo(t *testing.T, path string)`
   (stub + `t.Cleanup`) and `assertScrubbed(t *testing.T, err error, paths []string, want ...string)`;
   reuse from `install_test.go`/`seed_test.go`. Keep the per-test comments.
7. Efficiency, `marketplaces_path_scrub_test.go` — the 4 `TestEditMarketplace…`
   tests differ only in which primitive fails; make them subtests of one
   parent sharing a single `makeMarketplaceRepoWithPlugin` source repo (fresh
   `Manager` per subtest only). Low priority, wall-time only.
8. Altitude — `marketplaces.go:85-93` (`saveMarketplaces`'s caller list),
   `install.go:45-50` (`saveRegistry`'s caller list) — drop the hand-kept
   caller enumerations from the doc comments; state the invariant once
   ("every store write is scrubbed here, so no caller can leak the path").
   Low priority.

### S2's 6 (`1890-simplify.md`), reviewed at #1890@d5af6cefe

1. `marketplaces.go:316-319` (`cloneRemovalFailed`) — leave the call site (own
   message, own sentinel), but once a shared `m.scrubbed(raw error, format string, args ...any) error`
   exists (see S3 remaining #6 below) reduce this body to one line calling it.
2. `errors.go:17-24` (`ErrMarketplaceUnregisteredCloneRemains`) — exported
   with zero production consumers (S3's own test comment says so explicitly).
   Either unexport it (`errMarketplaceUnregisteredCloneRemains`, tests are
   in-package) until a caller branches on it, or classify it at the RPC
   boundary (`cmd/evener-hub/app_plugins.go`'s `marketplaceRefusalToWire`) in
   this same follow-up so the export has a user.
3. `marketplaces_path_scrub_test.go:162-252` — `TestRemoveMarketplaceRetryAfterCloneRemovalFailureReportsNotFound`
   repeats `TestRemoveMarketplaceCloneRemovalFailureNamesNoPath` line for
   line before adding the retry. Merge into one test (second half is the
   retry), or extract `removeWithFailingCloneRemoval(t) (*Manager, string)`.
4. `marketplaces_path_scrub_test.go:128-289` — same pasted injection-and-assert
   block as S1's #6; absorbed by the same two helpers once added there.
5. Efficiency — the 4 `gitAvailable()`-gated tests in this file each do their
   own `makeMarketplaceRepo(t, "market-a")` + `AddMarketplace`; one parent test
   with 4 subtests sharing one source repo (fresh `Manager` per subtest). Low.
6. `marketplaces.go:320-332` + `errors.go`'s sentinel comment + 2 test comments
   — the `""`/`".."`-resolves-to-marketplaces-dir rationale is written out 4
   times. Keep it once in `RemoveMarketplace`'s doc comment and the enforcing
   test's comment; cross-reference (don't restate) in the other two. Low.

### S3's remaining 6 (`1897-simplify.md`), reviewed at #1897@055565bef — finding 1 (must-fix) already applied

2. `marketplace_migration.go:1060-1067` (`markerAfterFailedMove`) — build the
   scrubbed sentence once into a local var, `errors.Join` it with
   `errRenameRollbackIncomplete` in the one branch that needs it, instead of
   writing the sentence twice:
   ```go
   scrubbed := fmt.Errorf("marketplace %q: moving its directories failed; see the hub's log for detail", name)
   if errors.Is(err, errRenameRollbackIncomplete) {
       return errors.Join(fmt.Errorf("%w: %w", scrubbed, errRenameRollbackIncomplete), m.markerLeftForRecovery("rename"))
   }
   m.removeRenameMarker()
   return scrubbed
   ```
3. `marketplace_migration.go:325-332` and `:1036-1043` — the "rollback also
   failed" log block (with its 4-line comment) is pasted twice, differing only
   in `marker.From` vs `name`. Extract `func (m *Manager) logRenameRollbackFailure(name string, undoErr error)`
   holding the nil check + message + comment; two one-line call sites.
4. `marketplace_migration.go:249`, `:358`, `:501-502` — each scrub logs the
   sentence once with the absolute path (for the server log) and once with
   the bare filename (for the wire); two already disagree in wording. Log the
   location once, separately from the wire message (e.g.
   `m.logMarkerPath(path)`, or `"warning: %v (the marker is %s)\n", err, path`).
5. `marketplace_migration_test.go:2862-2944` — `…_SaveFailureWhoseOwnRollbackFailsNamesNoPath`
   and `…_RecoveryRollbackFailureNamesNoPath` are identical 40-line tests
   differing only in trigger (`m.migrateStore` vs `m.ListMarketplaces` after
   `plantRenameMarker`); likewise `…_MarkerWriteFailureNamesNoPath` (`:2945`)
   and `…_RecordWriteFailureNamesNoPath` (`:2976`) differ only in which
   filename constant. Two table-driven tests instead of four.
6. `cmd/evener-hub/app_plugins_test.go:598-659` — the RPC-boundary test's
   "not a `WireError`" assertion pins today's non-classification; the day
   S2's follow-up #2 above classifies the sentinel, this assertion breaks and
   reads as a regression. Drop that one assertion (or invert to a comment),
   keep the two that are the point (sentinel survives, message has no path).
7. Altitude, remaining code half (the issue-filing half — #1896 item 4 — is
   already done): one `storeFileError(op, fileName string, err error) error`
   used by BOTH the read side (the eleven sites in #1896 item 4) and the write
   side (collapses S1 #2/#3, S2 #1, S3 #2/#3 above into one helper). This is
   the biggest single simplification available and the one worth doing before
   any of the line-level ones above, since it makes several of them moot.

### The "boom" assertion Low (from #1890 round-3 RoboRev, `reviews/raw/1890-d5af6cefe.md`, member 2)

- `marketplace_migration_test.go` — `TestMarketplaceNameMigration_ARecoveryThatRollsBackRemovesTheMarker`
  (was line 2694 in that round) lost its `"boom"`-cause discriminator: the
  assertion now only checks `strings.Contains(err.Error(), marketplacesFileName)`,
  which `saveFailed` injects unconditionally regardless of the real cause, so
  the test is trivially true by construction. Fix: point `m.Stderr` at a
  `bytes.Buffer` (`var stderr bytes.Buffer; m.Stderr = &stderr` — pattern
  already used elsewhere in this file, e.g. around line 1439) and assert the
  buffer contains `"boom"`, keeping the existing wire-error/marker assertions.

## #1896 state (as of this note)

Four items, edited directly in the issue body (not just comments):

1. **CLOSED** (struck through) — `saveRegistry` never scrubbed
   `installed_plugins.json`. Fixed on #1889 (S1) via the `storeFileSaveFailed`
   boundary scrub.
2. **OPEN** — `recoverMarkedRename`'s own `moveMarketplace`/
   `movePluginCachesToNewName` failures are unscrubbed; can't reuse
   `markerAfterFailedMove` because its marker-removal-on-clean-failure is
   wrong for a recovery (the marker there names an *earlier* run's pending
   rename, not one this call just wrote). Needs a design decision on marker
   retention, not just text scrubbing.
3. **OPEN** — `EditMarketplace`'s `fail` closure returns
   `moveMarketplace`/`swapInClone`'s raw error when its own rollback succeeds
   (`marketplaces.go:456`-ish). Investigated and NOT fixed: a blanket scrub
   breaks `TestEditMarketplace_FailedUndoNamesWhatItCouldNotRestore` and
   `TestEditMarketplace_TreatsOnlyAMissingPathAsAbsent`, two existing tests
   that deliberately want the raw path in two different failure sub-cases
   (own-rollback-incomplete; a `pathPresent` stat-check refusal like a
   symlink loop). Needs a fix that distinguishes three sub-cases, not one
   blanket scrub.
4. **OPEN**, new — eleven read-side sites still interpolate the absolute
   store path into errors reaching `ListMarketplaces`/`List`:
   `marketplaces.go:73,77`; `registry.go:46,50,53`;
   `marketplace_migration.go:430,434,547,551`; `catalog.go:98,102`; `gc.go:66`.
   This is why S3's PR body says "part of #1700", not "closes". Suggested
   direction: the same `storeFileError(op, fileName string, err error) error`
   noted in S3's remaining #7 above, used by both read and write sides.

## Go gates cheat-sheet for `internal/plugins` (and `cmd/evener-hub` when touching the RPC boundary)

Run from the repo root. Never use the PATH `gofmt` — it disagrees with the
toolchain version and will reformat unrelated lines.

```sh
# format
$(go env GOROOT)/bin/gofmt -l internal/plugins/*.go

# vet (plain, fuzz-tagged, and windows cross-vet)
go vet ./internal/plugins/...
go vet -tags evenerfuzz ./internal/plugins/...
GOOS=windows go vet -tags evenerfuzz ./internal/plugins/...

# lint (root go.work module — run from repo root, not from internal/plugins)
golangci-lint run ./internal/plugins/...

# tests (plain and fuzz-tagged)
go test -count=1 ./internal/plugins/...
go test -count=1 -tags evenerfuzz ./internal/plugins/...
```

If the follow-up PR touches `cmd/evener-hub` (e.g. S2 follow-up #2, classifying
`ErrMarketplaceUnregisteredCloneRemains` at the RPC boundary), add:

```sh
go vet ./cmd/evener-hub/...
golangci-lint run ./cmd/evener-hub/...
go test -count=1 ./cmd/evener-hub/...
```

Do NOT run the full root suite (`make test`, `go test ./...`) locally — CI
builds the merge ref and runs the whole matrix; a red 'tests' job whose
failing test is outside your touched paths (e.g. `agent`'s
`TestRetirementSafetyDetachedLifetime`) is very likely the tracked #1879
retention flake, not a real failure — check touched-paths overlap before
attributing.

Falsification recipe used throughout this stack: for a genuine bugfix,
revert only the production file(s) with `git checkout <prior-commit> -- <file>`,
confirm the new/strengthened test goes red, then `git checkout HEAD -- <file>`
to restore. For a pure refactor (no behavior change, e.g. the
`storeFileSaveFailed` consolidation), there is no red state to produce —
"falsifying" means confirming the existing tests pass identically before and
after, not chasing a failure that doesn't exist.
