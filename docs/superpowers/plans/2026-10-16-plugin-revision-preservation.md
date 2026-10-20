# Published Plugin Revision Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking. Jesse requires a dedicated Sol 6.1 coordinator in its own worktree for this fix.

**Goal:** upgrades and cache GC leave the whole selected revision available to live and resumable sessions.

**Architecture:** retain published revisions conservatively and publish validated installs without replacing existing directories. Automatic marketplace migration and explicit marketplace rename rekey logical ownership without moving published paths. Keep staging cleanup separate from published-content ownership. Do not implement selective GC in this fix.

**Tech Stack:** Go, local filesystem, existing plugin-store flock and local Git fixtures.

**Spec:** [Plugin session preservation design](../specs/2026-10-16-plugin-session-preservation-design.md), shared constraints and Fix 1.

## Global Constraints

- Preserve the session's selected revision whenever it exists.
- Preserve whole plugin directories, including scripts, hooks, MCP files and companion resources.
- Run targeted red/green checks locally. Full tests and lint gates belong in CI.
- Tests assert typed state, identities, counts and side effects, never prose.
- Conservative retention increases disk use. Get acceptance of this design before implementation.
- Explicit uninstall/marketplace removal and externally edited directory sources keep their current semantics.

## Review Focus

1. Return to revision A after A→B while another session reads A: never replace A. Task 1.
2. Empty SHA/`unknown` and destination collisions: publish elsewhere, preserve prior bytes. Task 1.
3. Validation, rename or registry-save failure: never delete someone else's published directory. Task 1.
4. Restart, custom roots, cold delegates and automatic name migration: old physical paths remain usable, including after a crash. Tasks 2 and 3.
5. Cache full or permission denied: report install failure while the old session remains useful. Task 1 and Task 2.

---

## Ownership and execution

Use coordinator `codex-jesse-at-pr/gpt-6.1-sol` with `isolation="worktree"`.
This fix can run alongside the native-history fix. Own only the files below and
focused adjacent tests needed by these interfaces. Leave recovery and warning
policy to their plans. Read `docs/developing-evener/testing.md`, product principles
and S17–S19 of `docs/product/subsystems.md` before changes.

### Task 1: publish without replacing existing revisions

**Files:**
- Modify: `internal/plugins/install.go` (`Install`, `upgradeLocked`, `commitStaged`).
- Test: `internal/plugins/install_test.go` and new `internal/plugins/revision_preservation_test.go`.

**Interfaces:**
- Keep `Manager.Install(ctx context.Context, plugin, marketplace string) (InstallEntry, error)` and `Manager.Upgrade` unchanged.
- Keep `commitStaged(marketplace, plugin, staging, sha string) (string, error)` as the publication helper, called under the store lock.
- Its input must already have fallback manifest synthesis and validation applied in owned staging. Its returned directory is a newly published path, never a pre-existing directory.
- Keep the existing unchanged-SHA upgrade no-op. For an absent nonempty SHA destination, retain that naming scheme. On any existing destination, or an empty SHA, allocate a fresh unique sibling under `cache/<marketplace>/<plugin>/` and publish there. Use standard-library unique temporary naming under the lock, then rename only into the path this call reserved. Do not add a hashing library or overwrite `unknown`.

- [ ] Add `TestPublishedRevisionSurvivesRevisit`, `TestPublishedRevisionSurvivesUnversionedInstall`, and `TestPublishedRevisionSurvivesPublicationFailure`. Use real local repositories and independent reads of the old files. Include hooks/scripts and a companion resource, not just a manifest. Assertions:

```go
// Test fixture provides oldPath, oldBytes, beforeInfo and a real install result.
if got, err := os.ReadFile(filepath.Join(oldPath, "resource.txt")); err != nil || !bytes.Equal(got, oldBytes) { t.Fatal(err, got) }
if after, err := os.Stat(oldPath); err != nil || !os.SameFile(beforeInfo, after) { t.Fatal("published identity changed", err) }
if replacement.InstallPath == oldPath { t.Fatal("publication reused a protected destination") }
```

- [ ] Run `go test ./internal/plugins -run '^TestPublishedRevision' -count=1`. Observe a behavioral failure against current overwrite/cleanup, not just a missing symbol.
- [ ] Move synthesis/validation before publication; change `commitStaged` to reserve a collision-free destination. Cleanup only paths allocated by this invocation. On registry-save failure, leaving a newly published orphan is acceptable; damaging the previous registry target is not. Exercise existing fault-injection seams at filesystem boundaries, including `ENOSPC` and permission errors.
- [ ] Rerun the new tests and `go test ./internal/plugins -run 'Test(Install|Upgrade|PublishedRevision)' -count=1`. Confirm unchanged-SHA behavior and directory-source behavior remain intact. Review the local Git fixture helpers before relying on their assertions.
- [ ] Stage the named implementation/test files, read the staged diff, and commit `fix(plugins): preserve published revision directories`. Run specification and quality review before Task 2.

### Task 2: make manager GC and doctor honor retained revisions

**Files:**
- Modify: `internal/plugins/gc.go`, `internal/plugins/doctor.go`.
- Modify tests: `internal/plugins/gc_test.go`, `internal/plugins/doctor_test.go`, affected focused GC fuzz seeds in `internal/plugins/coverage_doctor_gc_fuzz_test.go`.
- Test: new `agent/plugin_revision_preservation_test.go`.
- Update: `docs/product/subsystems.md` S17–S19 and `docs/getting-started.md` if it describes cache GC.
- Inspect callers: `cmd/evener-hub/main_background.go`, `cmd/evener/plugincmd.go`; change their help/comments only where the new manager contract requires it.

**Interfaces:**
- Keep `Manager.Gc(ctx context.Context) ([]string, error)` and its empty-array result shape.
- Remove published revision deletion from this shared manager entry point, so CLI and startup have the same preservation rule.
- Keep the separate protected marketplace-clone sweep and its errors. Doctor reports unregistered published revisions as `DoctorFinding{Level: LevelOK, Category: catRegistry, Check: "retained_cache_revision"}` with no deletion remediation. Retain its inaccessible-store and lock-failure findings. Update tests by typed check/level, never prose.

- [ ] Replace the old orphan-revision deletion expectation with `TestGcPreservesPublishedRevisions`; retain tests proving genuinely owned staging/clone cleanup and protection of registered directory sources. Add `TestPluginRevisionSurvivesUpgradeGcAndRestore`: create a real selected plugin A, save a session in a custom temporary state root, upgrade to B, GC, then restore and use A. Assertions:

```go
if slices.Contains(removed, oldPath) { t.Fatal("GC removed a selected revision") }
if !slices.Contains(restored.cfg.PluginDirs, oldPath) { t.Fatal("resume changed the selected revision") }
if bytesReadFromCompanion != originalCompanion { t.Fatal("restored session lost its companion resource") }
```

  Exercise actual plugin skill loading and an actual local hook/script from A. Include a frozen/cold delegate retaining A and a session whose initial metadata write fails. No metadata inventory is needed to retain these paths.
- [ ] Run `go test ./internal/plugins -run 'Test(Gc|Doctor).*' -count=1` and `go test ./agent -run '^TestPluginRevisionSurvives' -count=1`. Record the deletion failure before the fix. Use the narrow new test if unrelated package startup makes the red run costly.
- [ ] Change the shared GC policy, doctor guidance and evergreen docs. Document disk growth and the explicit-delete boundary. Do not add an age timeout, current-process scan or default-state-root scan as a deletion shortcut.
- [ ] Rerun those commands. Add caller-level checks using the existing CLI and startup test harnesses to show both still reach the protected `Manager.Gc`; do not launch a live hub or mutate the user's plugin store. Verify a failed new install leaves A usable and a successful fresh launch selects B.
- [ ] Commit only the named touched files as `fix(plugins): retain revisions needed by resumable sessions`. Run specification and quality review before Task 3. Record red/green commands and retained directory identities; whole-fix review follows migration coverage.

### Task 3: preserve physical paths through automatic marketplace migration

**Files:**
- Modify: `internal/plugins/marketplace_migration.go`; use existing helpers in `internal/plugins/marketplaces.go`. Explicit `EditMarketplace` also preserves cache paths under the scope amendment below.
- Test: new `internal/plugins/migration_preservation_test.go` and affected existing `internal/plugins/marketplace_migration_test.go` cases.
- Extend: `agent/plugin_revision_preservation_test.go` and preservation docs from Task 2.

**Interfaces:**
- `lockStore` must still establish valid logical marketplace names. Keep its existing signature and lock ordering.
- Automatic normal migration, `recoverMarkedRename`, `finishMarkedMerge` and `mergeIntoMigrated` use logical rekeying with existing `rekeyRegistry(reg, owners, oldName, newName, "", "")`, preserving existing `InstallPath` values. Never invoke a published-cache move through `moveMarketplace` or `movePluginCachesToNewName` on those paths. Separate any still-needed clone handling from cache handling.
- Keep the existing `renameMarker` and recorded-rename formats. Their writers are automatic migration/merge only; explicit edit writes no marker. No new marker mode or compatibility schema is required.
- `migratedUnderAnAlias` must use recorded migration family/source identity, not absence of the old cache. Preserve different-source, physical ownership and collision guards. Retained old caches are expected, not evidence that migration has not occurred.
- For an interrupted operation from the existing move-based implementation, preserve every surviving old tree and every valid old `InstallPath`. Reconcile a missing recorded path to an already-moved destination only when the existing marker and ownership evidence prove that exact move. Never move surviving old content or overwrite a destination to make the record fit. An unknown/unreadable path stays an error, not permission to delete. Do not add a name-only alias fallback to `UpdatedInstallDir`.

- [ ] Add `TestAutomaticMigrationPreservesPublishedPaths`, `TestAutomaticMigrationAliasPreservesPublishedPaths`, and `TestAutomaticMigrationRecoveryPreservesPublishedPaths`. Fixtures include an invalid recorded marketplace name, two superseded revisions, same-source aliases, a different-source collision, and interruption between the registry and marketplace writes. Assert original directory identity and bytes, valid rekeyed registry keys, retained physical `InstallPath`, and idempotent second acquisition.
- [ ] Run `go test ./internal/plugins -run '^TestAutomaticMigration' -count=1` before changing the migration. Observe old-path disappearance under `Install`, `Upgrade` or `Gc` through the real `lockStore` call.
- [ ] Implement logical-only cache migration and marker/alias recovery under the existing lock. Preserve the existing source guards and journal recovery. Explicit edits follow the stationary-cache amendment below. Do not replace old paths with symlinks or copy entire caches as a workaround.
- [ ] Rerun the new tests and `go test ./internal/plugins -run 'Test.*(Migrat|Marked|Alias|EditMarketplace)' -count=1`. Extend the real saved-session test so its exact-source skill and companion hook remain usable after automatic migration plus restart, not only ordinary A→B upgrade.
- [ ] Update docs, stage exact touched paths, inspect the staged diff, commit `fix(plugins): preserve session paths during automatic migration`, then run task reviews and the whole-fix review. The preservation fix is complete only after all three tasks pass.

### Explicit-rename scope amendment

- Explicit rename never moves or overwrites a published cache directory. Rekey
  registry ownership with unchanged `InstallPath` values, removing the superseded
  current-registry-only cache-retention guard rather than adding ownership history.
- Permit rename-back and destination cache-only residue without deleting, adopting
  or mutating retained packages. Preserve live-name, clone and orphan-registry
  collision checks, including install-free renames. Never relabel orphan entries.
  Keep automatic migration's combined collision checks unchanged.
- Retain source-safe clone moves/sweeps, transaction/save/rollback error identity
  and recovery. Direct plugin uninstall and marketplace removal remain unchanged.
- Verify ordinary rename with selected and superseded complete packages, A→B→A
  with retained packages on both sides, cache-only name reuse, and real subsequent
  Install/Upgrade publication including same-SHA differing-byte collisions. Convert
  stale cache-move tests to stationary-path assertions while retaining clone,
  source-safety, collision and save-failure coverage. No provenance ledger, schema,
  copies, aliases or selective cache sweeps.

## Completion

The deliverable is a preservation PR with real filesystem and session-restore
proof. Do not claim safe selective reclamation, recovery of deleted files, or
changed uninstall behavior. Hand the accepted commit to the recovery coordinator
as its base; do not implement that next fix in this worktree.
