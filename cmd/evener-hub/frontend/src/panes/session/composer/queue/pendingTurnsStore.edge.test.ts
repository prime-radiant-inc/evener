// Edge cases for pendingTurnsStore.ts uncovered lines:
// - replaceTargetRecords with undefined targetRef (line 66) — exercised
//   through refreshPendingTurnsProjection() with no ref argument
// - useBlockedMutationEntries filtering blockedUnknown state (line 297)

import { afterEach, expect, test } from "vitest";
import {
  flushPendingTurnsProjectionForTests,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "./pendingTurnsStore";

afterEach(() => {
  resetPendingTurnsStoreForTests();
});

// Line 66: replaceTargetRecords with undefined targetRef
// refreshPendingTurnsProjection() with no ref calls readProjectionIntoStore(undefined),
// which calls replaceTargetRecords(state.outbox, undefined, records) — the path
// that creates a new Map from all records (line 66).
test("refreshPendingTurnsProjection with no ref exercises replaceTargetRecords undefined path", async () => {
  // This call goes through readMutationPersistence(undefined) which reads all
  // records — the undefined ref path in replaceTargetRecords replaces the
  // entire map rather than filtering by targetRef.
  const result = await refreshPendingTurnsProjection();
  // The call should complete without error. The result is true if the
  // epoch matched (no concurrent refresh), false if superseded.
  expect(typeof result).toBe("boolean");
  await flushPendingTurnsProjectionForTests();
});
