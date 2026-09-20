# D25d-1 lane state note (native MutationOutboxStorage, rounds 2-3)

Retiring past ~400k tokens. Worked in
`/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-d25c-pending-turns`, currently on
branch `claude/sdk-d25d-1b-native-outbox-storage-read`, clean, matching origin. This note is
everything the next agent needs to pick up D25d-2 or a further review round on #1916/#1917.

## Heads (round 3, both pushed and mergeable)

| PR | Title | Head (full 40-char) | Non-test lines |
|----|-------|----------------------|------|
| #1916 | feat(native): MutationOutboxStorage write path over expo-sqlite (D25d-1a) | `8d7732de2baa376378c94d627dbd8a5b895044da` | 382 (mobile-native/src/mutationOutboxStorage.ts, write methods only) |
| #1917 | feat(native): MutationOutboxStorage read path over expo-sqlite (D25d-1b) | `ee0013dc819e945dc878df6f3812b67ee680488f` | 445 total in the same file (1916's 382 + 1917's own ~63 read-path lines); stacked on #1916, do not merge before it |

Both `gh api .../pulls/{1916,1917} --jq .mergeable` = `true`, `mergeable_state` = `"behind"` (main has
moved since these branched; not a conflict, just not rebased onto latest main - the coordinator's
call whether to refresh before merging).

## What each PR still owes

Nothing outstanding from my side as of round 3. Every finding from three review rounds on #1916
(15 findings across rounds 1-3) and two rounds on #1917 (round 1 repeated 1a's findings; round 2 had
one #1917-owned finding, addressed) is fixed, tested, and gated green. Only residual, carried since
round 1 and intentionally not fixed: the web oracle's `#discardSupersededNoteRecovery` step (a later
accepted `notes/human/set` supersedes an earlier refused one in recovery) is absent from the native
adapter because the `MutationOutboxStorage` port itself does not declare it - noted in #1916's PR
body, only matters once native ships a shared-notes feature. I have not checked CI or requested a
round 4 review (no polling per the brief) - that's the coordinator's call.

## The oracle mapping settled on this round (insertNew vs replace)

Round 3's panel found one finding-class across three Mediums: the original `insert()` was always an
upsert (`ON CONFLICT DO UPDATE SET state, attempted` only), but the IndexedDB oracle
(`cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts`) uses two different primitives
depending on the call site:

- **`enqueueIntent`** uses IndexedDB's `add` - throws on a duplicate key. Native equivalent:
  `insertNew()`, a plain `INSERT` with no `ON CONFLICT` clause at all, so a colliding
  `clientMutationId` throws a SQLite constraint error. Because `enqueueIntent`'s whole body already
  runs inside `this.transaction(...)` (the savepoint helper), that throw rolls back the sequence
  allocation with it - no separate handling needed, it falls out of the existing wrapper.
- **Every transition** (`settleReceipt`'s optimistic write, `transferToRecovery`'s recovery write)
  uses IndexedDB's `put` - full-row replace regardless of what was there before. Native equivalent:
  `replace()`, an `INSERT ... ON CONFLICT (client_mutation_id) DO UPDATE SET <every column> =
  excluded.<every column>`. `settleApplied` doesn't insert at all (delete-only across all three
  tables), so it never needed this distinction.
- Both `insertNew`/`replace` share one `insertValues()` helper for the positional param array, so
  the column list/order lives in exactly one place.

**Important - measured, not just read:** the round-3 panel's phrasing for this made it sound like a
straightforward parameter change; in practice the two write paths (`enqueueIntent` vs
`settleReceipt`/`transferToRecovery`) needed genuinely different SQL (no `ON CONFLICT` at all vs a
full one), not a shared method with a flag. Keep them as two methods, not one method with a boolean.

## The accepted-record field list (settleReceipt's optimistic promotion) - as the oracle has it

`settleReceipt` used to promote with `{ ...source, state: "accepted" }`, leaking a recovery-sourced
receipt's `recoveryKind`/`recoveryReason`/`attempted` into the optimistic row. Fixed by building the
record field-by-field, **verified directly against the oracle's source** (not a panel's paraphrase -
see the discrepancy below):

```
version, clientMutationId, originClientId, targetRef, threadId, method, payload, attachments,
optimisticDisplay, intentSequence, createdAt, state: "accepted"
```

That's 11 fields plus `state`. **`composerText` is deliberately NOT in this list.** Two different
review panels, on two different PRs, both suggested a field list that *includes* `composerText`
(#1916 round 3 didn't mention it; #1917 round 2's member 1 explicitly listed it). I read
`mutationOutboxIndexedDB.ts`'s actual `settleReceipt` line by line before implementing and confirmed
it omits `composerText` from the accepted record it builds - and #1917's panel member 2, doing an
independent full-port review on the same head, found no issue with that omission. This is a case of
"measure before accepting a review finding": if a future round suggests adding `composerText` back
to this list, that suggestion is wrong per the oracle - point back to
`mutationOutboxIndexedDB.ts`'s `settleReceipt` (the `accepted` object literal) as ground truth, not
to what a panel member typed.

## The expo-sqlite savepoint/trigger test recipe

**Transaction wrapper** - `protected transaction<T>(name: string, body: () => T): T` in
`mutationOutboxStorage.ts`, used by every compound write (`enqueueIntent`, `settleReceipt`,
`settleApplied`, `transferToRecovery`, `restoreProvenAbsent`):

```ts
protected transaction<T>(name: string, body: () => T): T {
	this.db.execSync(`SAVEPOINT ${name}`);
	try {
		const result = body();
		this.db.execSync(`RELEASE ${name}`);
		return result;
	} catch (error) {
		this.db.execSync(`ROLLBACK TO ${name}; RELEASE ${name}`);
		throw error;
	}
}
```

This is not a new idiom - it's `mobile-native/src/draftRepository.ts`'s `write()` method's own
`SAVEPOINT draft_write` / `ROLLBACK TO draft_write; RELEASE draft_write` pattern, reused verbatim
over the same synchronous database port. Confirm this is still the established pattern before
inventing another one; the coordinator flagged this exact reuse mid-round-2.

**Failure injection** - a SQL trigger, the same device `draftRepository.test.ts` already uses:

```ts
database.exec(
	"CREATE TRIGGER reject_outbox_delete BEFORE DELETE ON mutation_outbox BEGIN SELECT RAISE(ABORT, 'handoff failed'); END",
);
await expect(storage.settleReceipt(id, "pending")).rejects.toThrow("handoff failed");
```

**The trap I hit twice this round and had to fix:** the trigger must fire on the LATER statement in
the compound operation, never the first. `settleReceipt`'s handoff writes the optimistic INSERT
*before* the outbox DELETE; my first draft of the atomicity test put the trigger on the optimistic
INSERT, which meant the DELETE never even ran - the test passed for the trivial reason that a
synchronous throw aborts the function early, proving nothing about rollback. Only when the trigger
targets the SECOND statement (the outbox DELETE) does a failure prove the FIRST statement (the
optimistic INSERT) gets rolled back too. Same trap for `restoreProvenAbsent`'s loop: the trigger has
to fail the *second* record's UPDATE (via `WHEN NEW.client_mutation_id = '<id>'`) to prove the first
record's already-applied UPDATE gets rolled back with it. Before trusting any new atomicity test,
check which statement in the compound op the trigger targets is not the LAST one written.

**Row-count helper** - `node:sqlite`'s `.run()` and `expo-sqlite`'s `runSync()` both return
`{ changes: number | bigint, lastInsertRowid }`; `changedRows()` normalizes the bigint case and is
used by `markAttempted`/`markUnknown`/`delete()` to decide their boolean result from the statement's
own affected-row count instead of a preceding `SELECT`.

**Mechanics**: `node:sqlite`'s `DatabaseSync` is a Node built-in (stable on this repo's Node v26,
currently v26.0.0 per `node --version` in this worktree) - nothing to `npm ci`. `database.exec(sql)`
accepts multiple `;`-separated statements in one call (used for the combined
`ROLLBACK TO x; RELEASE x`).

## Splitting a combined-file fix back onto two stacked PR branches

Both round 2 and round 3 required this because every fix touched the SAME file
(`mutationOutboxStorage.ts`) that both #1916 (write path) and #1917 (read path, stacked) modify.
Recipe that worked cleanly both times:

1. Make all fixes/tests in the current checkout (whichever stacked branch has both commits, e.g.
   `claude/sdk-d25d-1b-native-outbox-storage-read`), run every gate green there first.
2. `git add` + commit as a throwaway `wip: ...` commit.
3. `git branch d1916-roundN <old #1916 head>`, checkout it, `git cherry-pick <wip sha>`.
4. Resolve conflicts by keeping ONLY the write-path content (drop any read-path section entirely -
   it doesn't exist on this branch yet) - the conflict markers land almost exactly at the
   "shared plumbing" comment boundary between the write methods and the (not-yet-present) read
   methods every time; expect it there.
5. **Check `npm run check` (tsc), not just vitest**, after resolving - vitest's esbuild transform
   does not type-check, so a generic-bound widening that's needed for the fix (this round: `get`/
   `fromRow`'s bound had to widen from `MutationOutboxRecord<A>` to `MutationRecord<A>` because
   `settleReceipt` now calls `get<MutationOptimisticRecord<A>>`/`get<MutationRecoveryRecord<A>>`,
   which the narrower bound rejects) will pass vitest and silently fail `npm run check`.
6. Rerun the full gate list, `git add`, `git commit --amend` with a real message (never leave `wip:`
   in the pushed history), `git push origin d1916-roundN:<real branch name> --force-with-lease=<real
   branch name>:<old head>`.
7. `git branch -f b-new <old #1917 head>`, checkout it, `git rebase --onto d1916-roundN <old #1916
   head>`. Resolve the read-path conflict the same way (keep the read-path section, drop nothing -
   this side is additive only). Gate, commit, push with `--force-with-lease` against #1917's old
   head.
8. Clean up: checkout the real tracking branch, `git reset --hard <new head>`, delete the temporary
   local branches. Confirm `git status` is clean and matches origin before reporting done.

Watch for one subtlety: a `git apply -R <patch>` used for falsification, run WHILE mid-cherry-pick/
mid-rebase with unresolved conflicts still in the index, can end up staging the REVERTED content if
you `git add` afterward without checking `git status` first - this happened once this round
(committed the falsified/reverted file by accident, caught by `grep`-ing for the fix's own symbol
name being absent from the committed blob, fixed with `git commit --amend`). Always grep the
committed file for a symbol the fix introduces before pushing, not just re-run the tests.

## D25d-2..4 pointers into `d25d-design-note.md` - adjustment from round 3

Read `d25d-design-note.md` in full before starting D25d-2; it has the current-vs-target flow and all
four rows' oracle/risk/visible-change writeups, still accurate. One addition from round 3's fix,
worth folding into D25d-2's own measurement pass rather than re-deriving it there:

- **D25d-2 (submit through dispatcher + pending-turns store)**: `enqueueIntent` is no longer
  idempotent on a repeated `clientMutationId` - it now throws (round 3's fix, mirroring the oracle's
  `add`). Any retry logic D25d-2 wires up (a dispatcher retry, a resubmit-after-reconnect path) MUST
  generate a fresh id per `enqueueIntent` call and never re-submit the same `clientMutationId` to
  `enqueueIntent` a second time expecting it to be a no-op - that path now throws instead of
  quietly refreshing two columns. This wasn't true before round 3 and isn't mentioned in the design
  note's D25d-2 writeup; check the dispatcher's retry path against this before assuming
  `enqueueIntent` can be called twice for the same intent.
- **D25d-3 (pending rows UI)**: unaffected by anything this round.
- **D25d-4 (reconnect/reload recovery)**: `restoreProvenAbsent` is now savepoint-wrapped (round 3's
  #1917-owned fix), so D25d-4's reconnect path can call it without a separate atomicity concern of
  its own - a failure partway through no longer leaves some `blockedUnknown` records reopened and
  others not. Slightly reduces (doesn't eliminate) the design note's stated "highest risk of the
  four rows" for D25d-4; the double-post exposure it describes is about the DISPATCH layer above
  this storage adapter, not this fix, so the note's recommendation to land D25d-4 last and let it
  soak still stands.

No line-count or PR-split adjustments to the design note's estimates - D25d-1's actual sizes (382 +
63 = 445 total, vs the note's original 130-220 guess) are already documented in the merged
`c-lane-state-note.md`, not repeated here.

## Mobile-native gates cheat-sheet (this file's own scope)

- `npx vitest run src/mutationOutboxStorage.test.ts` from `mobile-native/`.
- `npm run check` (tsc) from `mobile-native/` - **always run this, not just vitest**, whenever a
  change touches a generic bound, an exported type, or anything vitest's esbuild transform wouldn't
  catch.
- `make lint-package-imports` from the repo root - always, any PR touching the package or its
  consumers.
- Never run `npx biome` over `mobile-native/` - no config, no lint script there (confirmed again
  this round; don't second-guess this memory fact).
- `node_modules` in THIS worktree (`sdk-d25c-pending-turns`) is currently a real directory, not a
  symlink - checked with `ls -ld node_modules` before assuming otherwise. Other worktrees may differ;
  always check `[ -L node_modules ]` fresh rather than trusting a memory fact about a specific
  worktree.
- Falsification recipe used throughout: `git diff <parent-commit> -- <file> > patch`, `git apply -R
  patch`, run the ONE test file, confirm the exact expected subset fails (read the failure messages,
  don't just count red/green), `git apply patch`, rerun clean, `git status --porcelain` empty.

Stopping here per the coordinator's instruction.
