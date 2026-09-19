== panel a8029be0-3f76-4acb-bad2-ce15bf1e2865 head a0ae5bbff outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=18622 verdict=1 chars=172
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=18623 verdict=1 chars=182
-- member 2 pi/deepseek-4.1-flash type=default status=done job=18624 verdict=1 chars=1162
-- member 3 codex/glm-5.3-vision type=default status=done job=18625 verdict=1 chars=207

######## member 0 (codex default)
No issues found.

Summary: Refactors the web pending-turn projection to use the shared mutation projection fence and rechecks fencing immediately before applying snapshots.

######## member 1 (codex default)
No issues found.
Summary: Moves the pending-turns projection fence and record replacement into the shared mutation package while preserving generation, epoch, and failure semantics.


######## member 2 (pi default)
## Summary

The three commits move the frontend's per-target durable-read "projection fence" out of `pendingTurnsStore.ts` and onto the shared package (`createMutationProjectionFence` / `replaceTargetRecords` / `MutationPersistencePort` from `@evener/appwire-client/state/mutation`), and fix a timing hole by applying the fence's target decision at `setState` time (via `accepted.apply()`) rather than reusing a decision frozen when the read resolved.

I verified the refactor preserves the original semantics: the epoch/generation bookkeeping (`epoch`, `refresh`, `advance`, `reset`) is byte-for-byte the old logic, the candidate/filter predicate is re-run by `apply()` with no further await before `replaceTargetRecords`, `recordSubmittedHere(snapshot)` still runs for any accepted (non-`false`) read even when all targets end up stale — matching the old order — and `reset()` still clears the generations and bumps the epoch used by `submitWithPendingTracking`. The package subpath export, `MutationAttachment` → `MutationAttachmentRef` compatibility, and the `tsconfig.json` path mapping all resolve correctly, so the new imports are valid.

No issues found.

######## member 3 (codex default)
No issues found.
Summary: The web projection now delegates durable-read fencing and record replacement to the shared mutation package, re-evaluating accepted targets immediately before applying the snapshot.
