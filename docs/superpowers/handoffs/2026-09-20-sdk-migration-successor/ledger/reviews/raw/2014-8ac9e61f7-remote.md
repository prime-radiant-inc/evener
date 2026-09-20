== panel 49d6bb30-190f-4085-abca-b2c819da0d39 head 8ac9e61f7 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=22316 verdict=1 chars=146
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=22317 verdict=1 chars=121
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=22318 verdict=0 chars=3221

######## member 0 (codex default)
No issues found.

Summary: Draft staleness now accounts for ready-generation changes, with coverage for same-revision reconnects and fenced saves.

######## member 1 (codex default)
No issues found.
Summary: Jesse, this change correctly fences drafts to their ready generation with regression coverage.


######## member 2 (pi default)
## Summary

The single commit fixes a draft-staleness gap in the keybindings store. A draft already carried a `generation` stamp (the ready generation that confirmed the revision it was composed against) and `rebaseDraft`/`editDraft`/`saveDraft` already maintained it, but `staleDraft` only compared revisions. On a hub/session change whose revision numbering restarted (e.g. a restart that happens to report the same revision), a draft from the previous session was therefore treated as current and could be saved over the new hub's config. The commit teaches `staleDraft` to also treat a non-null draft generation that differs from the current ready generation as stale, updates all three call sites, and adds two behavior tests plus scenario expectations for fenced saves.

The main path is correct: generations are monotonic (never reused), all three `staleDraft` callers are generation-active sites, and a restored draft (`generation: null`) correctly falls back to the revision comparison, getting stamped on its first apply.

---

**Severity: medium** — `appwire-client/typescript/keybindingsStore.ts:744` (with `draftFieldsFrom` at :442 and `staleDraft` at :341)

The generation fence is silently dropped on every `restoreDraft` reload. `restoreDraft` builds its draft via `draftFieldsFrom(checkpoint)`, whose `generation` parameter defaults to `null` (:442) and is never passed here. So the `draft` given to `staleDraft(draft, confirmed.revision, fence.generation)` at :744 always has `generation: null`, and the new generation branch (`draft.generation !== null && draft.generation !== currentGeneration`, :341) is dead on this path — only the revision comparison runs.

What goes wrong: several mid-session code paths rebuild the draft through `restoreDraft` after a local-storage refusal — `persistDraft`'s save-refusal branch (thrown from `editDraft`/`saveDraft`/`rebaseDraft`), `settleWrite`'s `removeIf`/`replaceClassified` refusal, `discardDraft`'s `discardClassified` refusal, and `settledWrite`'s `!replaced` branch. Consider a draft stamped generation 1 against revision 3 that is correctly flagged `draftConflict` after reconnecting to a new session (generation 3) that also reports revision 3. If any of those storage refusals then runs `restoreDraft(getState())`, the draft's in-memory generation is discarded, the conflict is recomputed as `staleDraft(null-generation draft, 3, 3)` → `false`, and a subsequent `editDraft`/`saveDraft` is allowed to PATCH the old session's draft with `expectedRevision: 3` — which the new hub accepts because its revision also happens to be 3. That is exactly the revision-collision overwrite the commit exists to prevent; it just needs a concurrent-writer/storage refusal to reopen. The existing tests don't catch it because `memoryDraftStorage` never refuses.

Suggested fix: don't let a reload erase an established conflict — e.g. preserve the previous in-memory draft's generation (or keep `draftConflict` true) when the reloaded record is the same draft, or reserve the stamp only when the reload is a genuinely new/foreign record, and cover it with a test whose storage port refuses the write and whose hub reports the same revision across a generation change.
