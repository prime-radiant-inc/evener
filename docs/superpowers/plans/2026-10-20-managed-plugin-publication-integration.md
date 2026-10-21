# Managed plugin publication integration after preservation

Status: integration plan and spec amendment. Date: 2026-10-20. Issue:
[#4259](https://github.com/prime-radiant-inc/evener/issues/4259).

This document records how the approved Git-metadata separation is integrated
onto the preservation implementation already merged as PR #4795. It is not a new
product design. The operative design is
[2026-10-20-managed-plugin-publication.md](../specs/2026-10-20-managed-plugin-publication.md),
whose scope remains settled: managed installs keep their content paths, Git
metadata stays outside sandbox-readable managed content, and the per-spawn
managed-root `.git` walk stops only after a verified whole-store audit.
Preservation (validation before publication, immutable fresh revision paths,
retention through GC/Doctor, and automatic/explicit marketplace-rename
preservation) already landed and stays independent.

## Immutable inputs and holds

- Integration lane: `fix/plugin-metadata-after-preservation` at
  `3ce0e8bf2bfc54c158107009cf833d0b20e3f6ad` (clean before this plan).
- Preservation commit: `3ce0e8bf2b` (PR #4795, squash). Not modified.
- Frozen metadata candidate: `1659d37c5da1af5f73e3c4e1e060fa6d28ce69f9`,
  old base `9de8c6f0b2de2c24cca6dd0a2271030b95d62a53`. Read-only checkout at
  `wip/perf-plugin-scan-investigation`. Its branch, checkout, and PR #4785 are
  frozen inputs: never edit, reset, rebase, or copy an older overlapping file
  wholesale over newer landed code.
- No live store action, restart, deployment, push, PR edit, merge, or deletion
  of existing work by this stream. Parent owns delivery.

## Overlap table

The frozen delta is
`git diff 9de8c6f0b2de2c24cca6dd0a2271030b95d62a53 1659d37c5da1af5f73e3c4e1e060fa6d28ce69f9`
(45 files, +3,758/-275). The landed preservation commit changed 34 files. Files
touched by both branches, and the ruling for each:

| File | Frozen delta | Landed base | Reconciliation |
| --- | --- | --- | --- |
| `agent/plugin_revision_preservation_test.go` | older single-test journey (+233) | newer two-test journey covering automatic marketplace migration (+308) | Keep landed file; do not import the frozen duplicate. The metadata journey test is the separate new `agent/plugin_managed_content_test.go`. |
| `internal/plugins/revision_preservation_test.go` | three tests (+250) | four tests, including nested legacy staging (+267) | Keep landed file; do not import the frozen duplicate. |
| `cmd/evener-hub/main_background.go` | GC wording (+1/-1) | identical post-image | Already applied; no change. |
| `cmd/evener-hub/main_background_pluginroot_test.go` | startup GC retention test (+29) | identical post-image | Already applied; no change. |
| `cmd/evener/plugincmd.go` | GC usage wording (+1/-1) | identical post-image | Already applied; no change. |
| `cmd/evener/plugincmd_test.go` | CLI GC retention test (+34) | identical post-image | Already applied; no change. |
| `internal/plugins/gc.go`, `gc_test.go` | GC stops cache-revision sweep | identical post-image | Already applied; no change. |
| `internal/plugins/coverage_doctor_gc_fuzz_test.go` | drop nested-orphan GC fuzz cases | identical post-image | Already applied; no change. |
| `internal/plugins/package_union_fuzz_test.go` | rename retained-revision test target | identical post-image | Already applied; no change. |
| `internal/plugins/doctor_unix_test.go` | retained-revision assertion | identical post-image | Already applied; no change. |
| `internal/plugins/coverage_misc_fuzz_test.go` | weaker "GC succeeds" assertion | landed version proves bytes/identity/mode are preserved | Keep landed file; do not weaken it to the frozen text. |
| `internal/plugins/doctor.go` | rename orphan check to retained revision; OK level, no remediation | already identical finding semantics, plus newer manifest/Codex depth-independent walker and `Path` findings | Already applied; no change. The landed walker is the required newer implementation. |
| `internal/plugins/doctor_test.go` | rename and re-assert retained revision | already has the retained-revision assertions plus newer manifest/Codex cases | Already applied; no change. |
| `internal/plugins/install.go` | private `publishing` staging, audit before publication, immutable revision naming, validate before publish | landed owned staging container (`.staging-*`) with cleanup, validation order from PR #4795 | Keep landed staging ownership/cleanup and validation-before-publication. Move that owned staging container under the private `publishing` area, add the content audit, and use immutable revision publication. |
| `internal/plugins/install_test.go` | add "validated before publication" test | landed file already contains that test; only a stale comment differs | No test import. Adjust the staging comment to name the private publishing area (the merged staging container moves there). |
| `docs/plugins.md` | add Git-metadata section and GC wording | landed preservation wording (immutable revisions, rename retention, uninstall semantics) | Keep landed preservation text; add the metadata/publication/scan paragraphs and the revised GC row. |
| `docs/product/subsystems.md` | S17/S18/S19 metadata wording | landed preservation wording (S17 revision retention, S18 recovery, S19 MCP retention) | Keep landed rows; add only the managed-publication/migration/scan sentences to S17. Do not reintroduce the frozen rows' older preservation text. |
| `docs/sandboxing.md` | managed-root clean-state skip and archive wording | untouched | Apply frozen delta. |

Files the frozen delta adds or changes that the landed branch does not touch
are imported as-is: `internal/plugins/publication.go`,
`publication_crossdevice_{other,unix}.go`, `metadata_migration.go`,
`store_format.go`, `managed_*_test.go`, `metadata_migration*_test.go`,
`source_selection_test.go`, `source.go`, `git.go`, `locks.go`, `resolve.go`,
`resolve_unix_test.go`, `agent/plugin_managed_content_test.go`,
`agent/sandbox/{probe,resolve,rootguard,bwrap,seatbelt}.go`, its
`managed_content_*_test.go`, `envvars/userdirs/datavolume.go` and its test,
`docs/superpowers/specs/2026-10-20-managed-plugin-publication.md` (Task 1) and
`docs/sandboxing.md` (Task 3). `source.go`'s frozen post-image includes the repaired
source-selection rejection, file-identity/alias protection, and ambient
`GIT_DIR` handling; those repairs are part of this import, not optional.

## Preservation checks that must stay green

These landed behaviours must not regress; each has a named test:

- `TestPluginRevisionSurvivesUpgradeGcAndRestore` and
  `TestPluginRevisionSurvivesAutomaticMigrationAndRestore` in
  `agent/plugin_revision_preservation_test.go`.
- `TestPublishedRevisionSurvivesRevisit`, `...UnversionedInstall`,
  `...PublicationFailure`, and `...NestedLegacyStagingSurvives` in
  `internal/plugins/revision_preservation_test.go`.
- Marketplace/rename preservation suites:
  `internal/plugins/explicit_rename_preservation_test.go`,
  `migration_preservation_test.go`, `shared_cache_historical_ownership_test.go`,
  `store_changed_rename_rollback_test.go`, `marketplace_migration_test.go`,
  `marketplaces_test.go`.
- Doctor retention: `TestDoctor_RetainedCacheRevision`.
- GC retention: `TestGcPreservesPublishedRevisions`, hub startup and CLI GC
  retention tests (already identical to the frozen post-image).

## Global constraints

- Managed installs only. No new content namespace, no narrowing of cache or
  bundled grants. User skills and in-place user directory plugins keep existing
  behaviour.
- File tools keep their name-based `.git` component denial. The clean marker
  retires only the per-spawn backend walk; it never licenses reading `.git`
  through a file tool.
- Only entries named `.git` (any case) move, and only after the whole-store
  audit. Never move an ordinary symlink target, a `gitdir:` pointer target, or
  a marketplace/plugin namespace directory named `.git`. A store with any
  remaining `.git`, unsafe pointer, or read error stays `unverified`; the old
  scan keeps running and the next mutation retries.
- Updated hub/CLI writers are required before migration. Do not add old-writer
  support, fencing, or compatibility machinery.
- No live store, restart, deploy, push, PR, or merge. No native APFS claim
  without a case-insensitive normalizing host.

## Task 1: Import private publication and whole-store migration

Goal: managed publication never exposes Git metadata, publication is immutable,
and updated writers perform the bounded whole-store migration.

Files owned: `internal/plugins/publication.go`,
`internal/plugins/publication_crossdevice_other.go`,
`internal/plugins/publication_crossdevice_unix.go`,
`internal/plugins/metadata_migration.go`, `internal/plugins/store_format.go`,
`internal/plugins/source.go`, `internal/plugins/git.go`,
`internal/plugins/locks.go`, `internal/plugins/resolve.go`,
`internal/plugins/install.go`, `internal/plugins/install_test.go`,
`internal/plugins/source_selection_test.go`,
`internal/plugins/managed_publication_test.go`,
`internal/plugins/managed_namespace_test.go`,
`internal/plugins/metadata_migration_test.go`,
`internal/plugins/metadata_migration_alias_test.go`,
`internal/plugins/resolve_unix_test.go`, and
`envvars/userdirs/datavolume.go`, `envvars/userdirs/datavolume_test.go`.
Also owned: `docs/superpowers/specs/2026-10-20-managed-plugin-publication.md`
(imported verbatim from the frozen candidate).

Approach:

1. Add the new test files from the frozen delta first. Run
   `go test ./internal/plugins -run 'Test(CopyTreeSkipsGit|CopiedSource|Publication|PublishReclaims|MetadataArchive|PublishingGit|ManagedInstall|EmptySHA|AuditBundled|BundledStaging|ManagedPublicationJourney|ManagedMetadataMigration|TargetIsMixed|GitNamespace|InstallRejects|InstallAllows|InstallWithoutGit|AmbientRepository|ResolveForLaunch)' -count=1`
   and record the failure (missing implementation).
2. Import the new production files and the non-overlapping edits to `source.go`,
   `git.go`, `locks.go`, and `resolve.go` from the frozen delta.
3. Merge `install.go` by hand. Keep the landed `.staging-*` owned-container
   cleanup, but place the container under `<root>/publishing/...`, audit the
   prepared content, invalidate a `clean` marker before creating a marketplace
   or plugin namespace named `.git` (`if (isGitName(marketplace) ||
   isGitName(plugin)) && m.managedContentClean()`, write
   `managedContentUnverified` before `MkdirAll` of the namespace, remove
   staging and return the error if that write fails), and publish through
   `publishStaged(staging, final, installRename)` so a cross-filesystem
   publication still falls back to an audited temporary sibling. Keep the
   immutable `revision-<random>` naming when the SHA path is occupied or empty,
   and keep validation before publication from the landed branch. Extend the
   landed `TestPublishedRevisionSurvivesPublicationFailure`
   staging-leak assertion to check the private publishing directory as well as
   the cache directory, because the owned container moves.
4. Do not touch `gc.go`, `gc_test.go`, or the cmd files: they already match the
   frozen post-image. Do not weaken `coverage_misc_fuzz_test.go`: its landed
   assertions are stronger than the frozen text.
5. Run the task tests and the landed revision-preservation tests.

Acceptance:

- `go test ./internal/plugins ./envvars/userdirs -count=1` exits zero.
- Negative control: with a Go overlay restoring the base
  `internal/plugins/source.go`, `TestInstallRejectsGitMetadataSourceSelections`
  fails on the same cases it passes with the imported implementation.

## Task 2: Import managed-content sandbox policy and host probe

Goal: after a verified audit, both backends stop walking the managed content
roots while user skills keep their walk; before or without a clean audit,
behaviour is unchanged.

Files owned: `agent/sandbox/probe.go`, `agent/sandbox/resolve.go`,
`agent/sandbox/rootguard.go`, `agent/sandbox/bwrap.go`,
`agent/sandbox/seatbelt.go`, `agent/sandbox/managed_content_alias_test.go`,
`agent/sandbox/managed_content_clean_test.go`,
`agent/sandbox/managed_content_count_test.go`,
`agent/plugin_managed_content_test.go`.

Approach:

1. Add the three test files first; run their named tests and record the failure.
2. Import the frozen production changes. `rootguard.go` delegates
   `StripDataVolumeAlias` to `envvars/userdirs` (Task 1 adds it). `probe.go`
   samples `store-format.json` and the plugin content dirs. `resolve.go`
   records lexical, resolved, and on-disk spellings and exposes
   `ManagedWalkExempt`. Both backends consult it.
3. Add the independent traversal-count oracle. Introduce a package seam
   `var gitDirsUnderPolicy = gitDirsUnder` next to `gitDirsUnder` in
   `bwrap.go`, and make the policy call sites in `bwrap.go` and `seatbelt.go`
   use it. In `agent/sandbox/managed_content_count_test.go` (non-parallel;
   restore the seam with `t.Cleanup`), override the seam with a counter that
   delegates to the real function, then:
   - resolve a clean managed store whose `cache` and user `skills` roots each
     contain a real `.git`, and build the bwrap argv and Seatbelt policy twice;
   - assert the managed root is never passed to `gitDirsUnderPolicy`
     (count == 0 across both constructions) while the skills root is counted
     at least twice, which proves the flag is backed by an actual skipped
     traversal;
   - repeat with `EvenerManagedContentClean=false` and assert the managed root
     count is at least two, giving the before/after measurement.
4. Run the named journey test, the count test, and the sandbox package.

Acceptance:

- `go test ./agent/sandbox -run 'Test(BwrapCleanManagedRootSkipsWalkButUserSkillsStillWalk|BwrapUnverifiedManagedRootKeepsWalk|SeatbeltCleanManagedRootSkipsLinkedTargetWalk|SeatbeltManagedAliasSkipsLinkedTargetWalk|ProbeResolveSeatbeltWithASymlinkedManagedRoot|MissingManagedRootUsesProspectiveResolvedSpelling|ManagedWalkExempt|SpawnPolicyWalkCount)' -count=1 -v`
  and `go test ./agent -run '^TestRestoredSessionKeepsMigratedPluginRevisionAndCleanPolicy$' -count=1 -v`
  exit zero, with the repeated-construction zero-managed-traversal assertions
  observed next to the user-skills-still-walk assertions.
- Negative control: overlaying the base `agent/sandbox/bwrap.go` and
  `agent/sandbox/seatbelt.go` (which lack the `ManagedWalkExempt` check) makes
  the managed-clean skip tests fail, because the managed `.git` is masked
  again. Do not overlay the base `resolve.go`: it lacks `ManagedWalkExempt`
  and produces a compile error, not a behavioral failure.

## Task 3: Reconcile landed overlaps, docs, and final checks

Goal: preserve the merged preservation behaviour and document the integrated
boundary.

Files owned: `docs/plugins.md`, `docs/product/subsystems.md` (S17 row only),
`docs/sandboxing.md`, and this plan file.

Approach:

1. Do not modify `internal/plugins/doctor.go`, `doctor_test.go`, or
   `doctor_unix_test.go`: the landed versions already carry the frozen
   retained-revision finding on top of the newer walker.
2. Merge docs per the overlap table. Keep landed preservation contracts.
3. Run the full minimum check set from the brief and record exits:
   - `go test ./internal/plugins ./agent/sandbox ./envvars/userdirs -count=1`
   - `go test ./agent -run '^TestPluginRevisionSurvives(UpgradeGcAndRestore|AutomaticMigrationAndRestore)$' -count=1 -v`
   - `go test ./agent -run '^TestRestoredSessionKeepsMigratedPluginRevisionAndCleanPolicy$' -count=1 -v`
   - focused race runs for source rejection, ambient Git selection, ordinary
     target preservation, and failure-to-recovery.
   - `git diff --check` and `go vet` on the touched packages.
4. Rerun the Task 2 traversal-count oracle and report its counts, so the
   zero-traversal claim rests on counted calls, not a flag assertion.

Acceptance: all commands exit zero; platform skips are listed; the landed
preservation and marketplace-rename suites still pass.

## Task 4: Independent review and simplify

Goal: independent PAR on the immutable base/head diff, then simplify-code, then
rerun affected checks. Reviewers and implementers are leaf delegates
(`lunaroute/deepseek-4.1-flash`, delegation allowance zero). One writer at a
time.

Acceptance: review findings are dispositioned in the ledger; simplify changes
are rechecked; the branch is committed on named paths only and reported ready
for parent verification, not delivered.

## Rulings recorded before execution

- Ruling: keep the landed `agent/plugin_revision_preservation_test.go` and
  `internal/plugins/revision_preservation_test.go` instead of the frozen copies
  — the landed files are strict supersets and the brief requires preservation
  to stay landed — cost if wrong: the frozen-only journey variant is not run;
  the new `agent/plugin_managed_content_test.go` covers the metadata journey.
- Ruling: keep the landed stronger `coverage_misc_fuzz_test.go` assertions
  rather than the frozen weaker ones — the landed test proves byte/identity
  preservation, which the frozen text dropped — cost if wrong: none observed.
- Ruling: keep the landed doctor revision walker unchanged; it already carries
  the frozen retained-revision level/check change — the brief requires the newer
  Codex/manifest recognition — cost if wrong: a nested retained revision under
  legacy marketplace names could be reported differently.
