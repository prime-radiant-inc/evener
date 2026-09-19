# C-lane state note (SDK migration: D25e C4/C5 + D25d-1)

Retiring near the token budget. This is everything the next agent needs to pick up the lane: open
PR heads, what's done, what D25d-2 needs to read first, and the gate commands that keep coming up.

## Open PRs (all base `main`)

| PR | Title | Head (full 40-char) | Status |
|----|-------|----------------------|--------|
| #1899 | refactor(sdk): C4 follow-ups (typed timer port, lazy tripwire, host-agnostic wording, shared MutationCommit, commit+refs test) | `fd775c08654b0e603b7d16e9183340c02ade7955` | Open, CI/RoboRev not yet checked by me after the rebase-onto-main push. Rebased onto `origin/main` after #1886 merged; own diff verified identical shape before/after. |
| #1913 | refactor(sdk): C5 follow-ups (submission runner factory, shared types, dead method field, test title) | `a8e273ed90b1b3cc962cebc49dc5040a02c54437` | Open, same status as #1899 (rebased onto main after #1901 merged, own diff verified identical). |
| #1916 | feat(native): MutationOutboxStorage write path over expo-sqlite (D25d-1a) | `15dbdb5c0e7d220ff60aa9741b01994c8e927290` | Open, base main, standalone (not stacked on anything open). |
| #1917 | feat(native): MutationOutboxStorage read path over expo-sqlite (D25d-1b) | `71685b1e90363114d376a5529a3cd45c5b28c050` | Open, base main, **stacked on #1916** - its diff includes 1a's commit until 1a merges. Do not merge 1b before 1a. |

## Merged this session (for context, not action)

- #1886 (D25e C4: commit feed + stall tripwire in the package) → squash-merged `b62990e8088762e2d8851bf1534edbe218c7e5f9`.
- #1901 (D25e C5: submission lifecycle in the package) → squash-merged `bb910cf95752582f34c4e22e273f0bdfe16f4202`.

## D25d-1 status: both PRs drafted, tested, gated - nothing else owed

The measurement phase (design note) predicted the full 13-method `MutationOutboxStorage` port
would exceed ~150 non-test lines in one PR (it came in at 302) and specified the exact split if so:
1a = write path (enqueueIntent, markAttempted, markUnknown, settleReceipt, settleApplied,
transferToRecovery), 1b = read path (getOutbox, getOptimistic, listOptimistic, getRecovery,
nextDispatchable, listTargetRefs, restoreProvenAbsent), 1b stacked on 1a. That's exactly what
happened - 1a landed at 254 non-test lines (schema + shared insert/get/delete plumbing + the 6
write methods; the plumbing has to live somewhere and 1a is where), 1b added 82 lines (the 7 read
methods + a new `list()` helper, reusing 1a's plumbing).

Both PRs:
- Use `node:sqlite`'s `DatabaseSync` in tests (a real SQLite engine, not a mock), mirroring
  `mobile-native/src/draftRepository.test.ts`'s own harness for the phone's existing draft storage.
  `node:sqlite` is a Node built-in (stable on this repo's Node v26) - nothing to `npm ci`.
- Cite `cmd/evener-hub/frontend/src/stores/mutationOutbox.test.ts`'s
  `describe("MutationOutboxIndexedDB", ...)` block as the oracle, with per-test comments naming
  which of its assertions each new test mirrors.
- Passed falsification (production file reverted/diff-reversed, correct subset of tests failed,
  restored clean).
- Gates run and green: `npx vitest run src/mutationOutboxStorage.test.ts` (15/15), `npm run check`
  in `mobile-native` (tsc clean), `make lint-package-imports` (PASS). **Never ran biome** -
  `mobile-native/` has no biome config (confirmed: no lint/format script in its `package.json`
  either) - this matches an existing memory fact, don't second-guess it.
- Match `mobile-native/src`'s tab-indentation style (converted from my initial 2-space draft with
  a `perl` one-liner; verified against `draftRepository.ts`'s actual bytes via `od -c`, not by eye -
  `cat -A` doesn't exist on macOS `cat`).

Residual/gap noted in #1916's body, not fixed: the web's `MutationOutboxIndexedDB` has a
`#discardSupersededNoteRecovery` step (a later accepted shared-note supersedes an earlier refused
one in recovery) that the `MutationOutboxStorage` port itself does NOT declare, so it's absent from
the native adapter. Only matters if/when native's queue gets a shared-notes feature; not blocking.

## D25d-2..4 plan (from `.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/d25d-design-note.md`,
approved by Jesse as written)

Read that file in full before starting D25d-2 - it has the current-vs-target flow, all four rows'
risk/oracle/visible-change writeups, and the package gaps found. Headline pointers:

- **D25d-2** (next): wire `MutationOutbox`, `MutationDispatcher`, `createPendingTurnsStore`, and
  `submitWithPendingTracking`/`createSubmissionRunner` into `mobile/src/state/conversation.ts`'s
  `send`/`steer`/`queue`/`interrupt`, replacing direct `service.send()` calls. Retires
  `ConversationMutationState`/`pendingMutation`/`pendingSend`. Estimate 100-150 non-test lines.
  **Visible change to state explicitly in the PR body per Jesse's ruling**: submitting while
  offline now durably queues instead of throwing immediately - this is the intended, ruled
  behaviour (Jesse's ruling 4), not a silent regression to flag for permission, but the PR body
  must say so plainly. Needs a small adapter between the package's `PendingTurnsDraftPort` and
  native's existing `DraftDocument`/`draftRepository.ts` (different shapes - see design note's
  "Draft port" gap entry).
- **D25d-3**: pending rows rendered from `pendingTurnEntries`, deduping against the hub's echo via
  `MobileConversation`'s existing `ThreadModel`/`ItemModel.clientMutationId` - **no new dedupe
  logic needed**, this already works because `MobileConversation extends ThreadModel` via the same
  package `hydrateThread` the web uses. Oracle: `pendingEntries.test.ts` (241 lines, package,
  unchanged). Watch the "never show refresh-to-see-latest" rule - native's subscription needs to
  fire on both the pending-turns store AND the thread store, same as web's `usePendingTurnEntries`.
- **D25d-4** (last, after 1-3 soak): reconnect/reload recovery via
  `MutationDispatcher.restoreProvenAbsent(targetRef, collectAuthoritativeMutationIds(response))`
  wired into native's `requestRehydrate`/reconnect path, plus starting `MutationOutbox` discovery
  on cold start. Highest risk of the four rows (double-post after an app kill) - do not parallelize
  with 2/3.

## Gates cheat-sheet (repo root unless noted)

- Package/web PR touching `appwire-client/typescript/state/mutation/*`: from
  `cmd/evener-hub/frontend`, `npx vitest run <touched files>`, `npm run typecheck`, `npm run lint`
  (biome ci over `src` + the package), `npx biome ci ../../../appwire-client/typescript`. If the PR
  adds/moves a package module or touches `index.ts`/`tsconfig.build.json`/the qualifier: also
  `make test-api-package` from repo root, plus the index.ts-re-export-deletion falsification
  (`AssertionError: shipped module unreachable from every published specifier: <module>`).
- `make lint-package-imports` from repo root: always, any PR touching the package or its consumers.
- Native (`mobile-native/`) PR: `npx vitest run <touched files>` from `mobile-native/`, `npm run
  check` (tsc) from `mobile-native/`. **Never run biome there** - no config, no lint script.
  `node:sqlite`'s `DatabaseSync` is the test double for anything backed by `expo-sqlite` (see
  `draftRepository.test.ts` for the established pattern); it's a Node built-in, not an npm install.
- A package change touching a port/exported type/`index.ts`: also `npm run check` in
  `mobile-native` (the phone imports the package by name via Metro's resolver in
  `mobile-native/metro.config.js`).
- Falsification recipe used throughout this session: for a new production file, `mv` it out of the
  tree and confirm the new test(s) fail to resolve the import, then `mv` back. For an edit to an
  existing file, `git diff <file> > patch`, `git apply -R patch` (or `git checkout -- <file>` when
  the file is otherwise unchanged since a known commit), run the ONE test, confirm the right subset
  fails, `git apply patch` (or re-apply from the commit) to restore, `git status --short` must show
  only the intended files.
- Rebasing a follow-up branch after its base PR merges: `git fetch origin main`, then
  `git rebase --onto origin/main <old-base-sha> <branch>`, then verify `git diff -M --stat
  origin/main...HEAD` shows the same files/line-counts as before the rebase (a real conflict would
  show up as a rebase stop, not silently).

## What I did NOT get to

- Have not checked CI/RoboRev status on #1899, #1913, #1916, #1917 since pushing (no `gh pr view`
  polling per the no-polling rule - that's the coordinator's or the next agent's call to make when
  ready).
- D25d-2 not started. Jesse approved the split (D25d-1/2/3/4) but D25d-2 itself needs its own
  measurement pass against `mobile/src/state/conversation.ts`'s current `send`/`steer`/`queue`/
  `interrupt` bodies before writing code - the design note's line estimate is a plan-time guess,
  same as D25d-1's was (which measured 302, not the ~130-220 the design note guessed).
