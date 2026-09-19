# #1810 S2/S3 rounds 4-5 state note (written near 400k tokens, handing off)

Continuation of `1810-split-state-note.md` (S1/S2/S3 decomposition). That
note's "current heads" section is stale for S2 and S3 as of this note; this
file supersedes it for both. S1 #1889 is unchanged (merged to main as
`a81740a362d8c714953b59374cef86bb5dd6cdd4`).

## Current heads

- **S2 #1890** branch `claude/plugins-path-scrub-s2-remove-reorder`: head
  `bda74b98413ae09c43a8c2e6789f8ba60c3c77c8`. Round 4 and round 5 both
  landed and pushed; dispositions posted as PR comments. CI green as of
  round 5's push. Nothing blocking merge.
- **S3 #1897** branch `claude/plugins-path-scrub-s3-migration-close`: head
  `d3c099449e1d13aeaaf9c18b45d07aa9abee4ee8`. Rebased onto S2's round-5 tip;
  own diff vs S2 unchanged since round 3 (4 files, 318 insertions(+), 43
  deletions(-): `internal/plugins/install.go`, `marketplace_migration.go`,
  `marketplace_migration_test.go`, `marketplaces.go`). Nothing blocking
  merge. A fresh lane should merge S2 then S3 (S3 stacks on S2), same as
  `1810-split-state-note.md` already said.

Round 4 disposition: https://github.com/prime-radiant-inc/evener/pull/1890#issuecomment-5736731499
(pointer on #1897: https://github.com/prime-radiant-inc/evener/pull/1897#issuecomment-5736734932)
Round 5 disposition: https://github.com/prime-radiant-inc/evener/pull/1890#issuecomment-5737169796
(pointer on #1897: https://github.com/prime-radiant-inc/evener/pull/1897#issuecomment-5737171673)

Neither branch has uncommitted changes or open stashes as of this note.

## The typed-outcome contract, end to end

`RemoveMarketplace`'s applied-with-litter outcome (the unregister save
landed, the clone's own removal from disk failed) is now typed all the way
from the manager to the CLI/client, in this shape:

1. **Sentinel** (`internal/plugins/errors.go`, from S2 round 3/base):
   `ErrMarketplaceUnregisteredCloneRemains = errors.New("marketplace
   unregistered, but its clone could not be removed")`. Wrapped by
   `cloneRemovalFailed` (`internal/plugins/marketplaces.go`) with `%w` so a
   caller can `errors.Is` against it; the wrapping message carries no path
   ("see the hub's log for detail").

2. **Hub RPC boundary** (`cmd/evener-hub/app_plugins.go`,
   `hubPluginsController.RemoveMarketplace`, round 4 + round 5):
   ```go
   if errors.Is(err, plugins.ErrMarketplaceUnregisteredCloneRemains) {
       data := appwire.MarketplaceUnregisteredCloneRemainsData{
           EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
       }
       hubPluginsReconcileAfterCloneLitter()
       if applied, listErr := c.listMarketplaces(ctx); listErr != nil {
           data.AppliedUnavailable = true
       } else {
           data.Applied = applied
       }
       return appwire.MarketplaceListResponse{}, appwire.WireError{
           Code:    appwire.CodeInternalError,
           Message: err.Error(),
           Data:    data,
       }
   }
   ```
   Never routes through `marketplaceRefusalToWire` (that's for the plain
   refusals: unknown name, bad source, etc.) - this is a *different* wire
   error class, always returned as a typed `WireError`, whether or not the
   re-list to build `Data.Applied` itself succeeds.

3. **Wire types** (`appwire/errors.go` + `appwire/types.go`, round 4 + 5):
   - `ErrorMarketplaceUnregisteredCloneRemains ErrorInfo =
     "marketplaceUnregisteredCloneRemains"` (errors.go) - the
     `Data.evenerErrorInfo` discriminator, same rule as `ErrorKeybindingsPostRename`.
   - `MarketplaceUnregisteredCloneRemainsData` (types.go):
     ```go
     type MarketplaceUnregisteredCloneRemainsData struct {
         EvenerErrorInfo    ErrorInfo               `json:"evenerErrorInfo"`
         Applied            MarketplaceListResponse `json:"applied"`
         AppliedUnavailable bool                    `json:"appliedUnavailable,omitempty"`
     }
     ```
     `Applied` has no `omitempty` (a struct field - `omitempty` is a no-op on
     struct kinds in Go's `encoding/json` anyway) - it's always present.
     `Applied.Marketplaces` is `[]MarketplaceEntry` with no `omitempty` on
     that field either, but the VALUE differs by path: the hub's
     `listMarketplaces` always builds it via `make([]appwire.MarketplaceEntry,
     0, len(names))`, so on a successful re-list it serializes as `[]` or a
     populated array, **never** `null`. On `AppliedUnavailable: true`,
     `Applied` is left at the Go zero value, so `Applied.Marketplaces` is a
     nil slice and serializes as `null`. `AppliedUnavailable` itself is
     `omitempty` - absent in the normal (successful re-list) case, `true`
     only when the hub's own follow-up read failed.
   - Code on the wire: `appwire.CodeInternalError` (-32603), same code
     `ErrorKeybindingsPostRename`/`ErrorTranscriptDisplayPostApply` use for
     "applied, then a follow-up durable step failed."
   - Neither this Data type nor `MarketplaceUnregisteredCloneRemainsData` is
     part of codegen (`appwire-client/typescript/types.gen.ts`) - like
     `KeybindingsPostRenameData`/`TranscriptDisplayPostApplyData`, a
     `WireError.Data` payload is `any` on the Go side and consumed by hand
     on the TS side (see below), never generated.

4. **Package store** (`appwire-client/typescript/state/extensions/marketplaces.ts`,
   round 5): `removeMarketplace` catches its own rejection, extracts and
   applies `Data.Applied`, then rethrows - consume-then-rethrow, not
   swallow-and-succeed (deliberately different from
   `keybindingsStore.ts`'s post-rename handling, which swallows and reports
   success, because litter is something the user needs to see, not cosmetic
   settings state):
   ```ts
   function cloneLitterApplied(error: unknown): MarketplaceEntry[] | undefined {
     if (!(error instanceof WireError) || error.evenerErrorInfo !== "marketplaceUnregisteredCloneRemains")
       return undefined;
     const data = error.data as { applied?: { marketplaces?: MarketplaceEntry[] }; appliedUnavailable?: boolean };
     if (data.appliedUnavailable) return undefined;
     return data.applied?.marketplaces;
   }

   removeMarketplace: (name) =>
     mutate(() => client.request("evener/marketplace/remove", { name }), [name]).catch((error: unknown) => {
       const applied = cloneLitterApplied(error);
       if (applied !== undefined) {
         set((s) => ({ marketplaces: applied, browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [name]) }));
       }
       throw error;
     }),
   ```
   `cloneLitterApplied` returns `undefined` (no reconciliation) both for
   any other rejection AND for `appliedUnavailable: true` - there is nothing
   trustworthy to apply in that case, and applying an empty/null list would
   read as "every marketplace gone."
   **Known gap, not fixed**: this `set()` runs outside `listRevision`'s
   fencing protocol (see `listRevision.ts`'s state table) - a concurrent
   `fetchMarketplaces()` that resolves in the narrow window between the
   failed `remove` and this `catch` could theoretically be stomped by this
   reconciliation if it raced with a *newer* answer. Judged acceptable for
   round 5's scope (self-heals via the same `evener/marketplace/updated`
   broadcast that already exists, and is strictly better than pre-round-5
   behavior, which did nothing at all on this path). Flagging in the
   follow-ups below rather than fixing now.

5. **CLI boundary** (`cmd/evener/plugincmd.go`, `runPluginMarketplace`'s
   `remove` case, round 4): checks the same sentinel and reports
   ```go
   removed marketplace %q; its clone files could not be removed and remain
   on disk; no automated cleanup exists yet for marketplace clones, remove
   them by hand: %w
   ```
   wrapping `plugins.ErrMarketplaceUnregisteredCloneRemains`, still returns
   a non-nil error (non-zero exit via `main.go`'s generic dispatch error
   path) - "removed" is in the message so the registry change reads as
   landed, but the call is still an error (Jesse's ruling 9: never swallowed
   as success). Checked for an existing gc/clean command first (there is
   none - see follow-ups) rather than inventing one.

## The between-step seam idiom (reused, not invented)

`cmd/evener-hub/app_plugins.go` added `hubPluginsReconcileAfterCloneLitter`,
a no-op `var ... = func() {}` called immediately before the litter path's
re-list, purely as a test seam (a test points it at a real
`os.Chmod(registryFile, 0o000)` to force the reconcile read to fail with a
genuine permission-denied, without touching `RemoveMarketplace`'s own
already-successful unregister-then-failed-clone-cleanup). This is not a new
pattern - it matches existing precedent already in this package:
`credentialWriteBetween` (`app_auth.go:482`, used by `app_auth_test.go`) and
the navigation service's family of hooks (`navigation_service.go`:
`navigationBeforeSnapshotCommit`, `navigationFingerprintStringChunkContext`,
`navigationPublicationCommittedLocked`, `navigationRefreshTicketAttached`,
`navigationBeforePublicationDrainLock`, `navigationPendingCleared`). Reach
for this idiom again before inventing a different test-injection mechanism
for a synchronous, same-goroutine, two-internal-calls-in-a-row situation
that a package-level var swap (like `internal/plugins`'s
`marketplaceRemoveAll`) can't reach from outside the package, and that a
timing-based/concurrent-goroutine race would make flaky.

## What each PR still owes

- **S2 #1890**: nothing blocking. All 5 rounds' findings fixed (round 3's
  Medium was refuted with evidence, not fixed - see `1810-split-state-note.md`).
  5 rounds is the ceiling before decomposition (per BRIEF-COMMON); round 5's
  panel had exactly the two Mediums this note describes and both are fixed,
  so no split was needed.
- **S3 #1897**: nothing blocking. One thing worth knowing for whoever merges
  or reviews next: round 4's rebase onto S2's tip conflicted in
  `cmd/evener-hub/app_plugins_test.go` because S3's own commit
  `47332cd81` ("test(hub): RemoveMarketplace's clone-litter error is
  wire-safe") had added `TestPlugins_Marketplace_RemoveCloneLitter_NamesNoPathAndIsDistinguishable`,
  which asserted TODAY's (pre-round-4) unclassified behavior - including a
  "want it left unclassified (not folded into a refusal wire code)"
  assertion that became false the moment round 4 classified the sentinel.
  Resolved by keeping S2's own equivalent, already-correct test
  (`TestPlugins_Marketplace_RemoveCloneRemovalFailureReturnsALitterWireError`)
  and dropping S3's superseded one rather than rewriting it into a
  near-duplicate; `47332cd81`'s diff came out empty after that resolution
  and git dropped the commit entirely during the rebase (5 commits on S3
  instead of the original 6). `cmd/evener-hub/app_plugins_test.go` is
  currently byte-identical between S2 and S3's tips. This is exactly the
  collision `1810-split-state-note.md`'s "S3's remaining #6" already
  flagged in advance; treat it as resolved, not as new follow-up work.

## Follow-ups

`1810-split-state-note.md`'s follow-up list (ONE PR, opened from main after
S2+S3 merge) is **unchanged and still open** - S1's 8, S2's 6, S3's
remaining 6 (S3's #6 is now resolved per above; the rest of that list still
applies verbatim), plus the #1896 state and the "boom" assertion Low. Read
that file for the full list; this note only adds what's new from rounds 4-5:

1. **New, not yet filed as a GitHub issue**: no command sweeps a
   marketplace's own leftover clone directory
   (`<Root>/marketplaces/<name>`) after a failed `RemoveMarketplace`
   cleanup. Measured in round 4: `evener plugin gc`
   (`internal/plugins/gc.go`) only sweeps `cacheDir()` (materialized plugin
   dirs under `<Root>/cache/...`), never `marketplacesDir()`; `evener plugin
   doctor` only iterates *registered* marketplaces (the litter's registry
   entry is already gone by definition), so it can't see it either.
   Suggested direction: extend `Gc` to also sweep unreferenced
   `marketplacesDir()` entries, or add a `Doctor` check for a clone
   directory with no matching registry entry. This is the "run <the
   existing gc/clean command>" the CLI message (round 4) explicitly could
   not name.
2. **New, not yet filed**: the package store's `removeMarketplace`
   reconciliation (round 5) applies `Data.Applied` via a raw `set()` outside
   `listRevision`'s fencing protocol - see "Known gap, not fixed" above. Low
   priority (self-healing via the existing broadcast/refetch path); worth a
   look if `listRevision.ts`'s invariant ever gets tightened.
3. Everything in `1810-split-state-note.md`'s existing list (S1 #1-8, S2
   #1-6, S3 #2-5/#7, #1896 items 2-4, the "boom" assertion Low) is
   unaffected by rounds 4-5 and still applies as written there. In
   particular S3's remaining #7 (the `storeFileError(op, fileName string,
   err error) error` unification, both read and write sides) would also
   absorb this note's follow-up #1 above once it exists, the same way it
   already absorbs S1 #2/#3, S2 #1, S3 #2/#3.

## Go + frontend gates cheat-sheet (cmd/evener-hub, appwire, the package store)

Run Go commands from the repo root; run frontend commands from
`cmd/evener-hub/frontend`. Never the PATH `gofmt`, never `npx biome` from the
repo root (see BRIEF-COMMON).

```sh
# --- Go: cmd/evener-hub and appwire (both ROOT go.work module) ---
$(go env GOROOT)/bin/gofmt -l cmd/evener-hub/*.go appwire/*.go

go vet ./cmd/evener-hub/... ./appwire/...
go vet -tags evenerfuzz ./cmd/evener-hub/... ./appwire/...
GOOS=windows go vet -tags evenerfuzz ./cmd/evener-hub/... ./appwire/...

golangci-lint run ./cmd/evener-hub/... ./appwire/...

go test -count=1 ./cmd/evener-hub/... ./appwire/...
go test -count=1 -tags evenerfuzz ./cmd/evener-hub/... ./appwire/...
```

If also touching `internal/plugins` (the manager side of this same
contract), add the identical four-step vet/lint/test set scoped to
`./internal/plugins/...` - see `1810-split-state-note.md`'s own cheat-sheet
for that half; it hasn't changed.

```sh
# --- Frontend: the appwire-client/typescript package store, from
# cmd/evener-hub/frontend ---
npx vitest run ../../../appwire-client/typescript/state/extensions/<touched>.test.ts
npm run typecheck
npx biome ci ../../../appwire-client/typescript
npm run lint   # runs "biome ci src ../../../appwire-client/typescript"

# --- from repo root ---
make lint-package-imports
```

Biome's auto-formatter (`npx biome check --write <files>` from
`cmd/evener-hub/frontend`) is safe to run on files you just wrote/edited
yourself in `appwire-client/typescript` when `biome ci` flags pure
formatting (quote style, line wraps) with no logic change - confirm with a
`git diff` read before trusting it blindly per line, same discipline as any
other auto-fix.

Do NOT run `make test-web`, `make test-native`, `make test-web-browser`, `go
test -race`, `make lint`, or `secret-scan` locally - CI builds the merge ref
and runs the whole matrix (Jesse, 2026-09-17: "lean on the CI runner"; see
BRIEF-COMMON).

Not needed for this specific contract (no port/interface/exported-type
change reached mobile-native or index.ts/tsconfig.build.json/the qualifier):
`npm run check` in mobile-native, `make test-api-package`, the index.ts
falsification. Re-check BRIEF-COMMON's trigger conditions before skipping
those on a *different* change to this same package.
