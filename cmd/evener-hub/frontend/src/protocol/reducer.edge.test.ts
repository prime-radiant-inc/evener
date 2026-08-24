// Edge cases for reducer.ts that close the remaining uncovered lines:
// - laterActivity: incoming is NaN, current is NaN, incoming > current
// - mergeStableDelegate: incoming projectionRevision > current, activity merge
// - upsertStableDelegate: new delegate, existing delegate merge, no-op merge
// - applyNotification "evener/delegate/updated": adds/updates delegates
// - applyNotification "evener/jobs/treeUpdated": stale revision

import { expect, test } from "vitest";
import type { ThreadModel, TurnModel } from "./model";
import { applyNotification, hydrateThread, prependOlderTurns } from "./reducer";
import type { AnyNotification, EvenerDelegateInfo, Thread, ThreadCapabilities } from "./types.gen";

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thr_t",
    sessionId: "sess_t",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
    },
    ...overrides,
  };
}

function testHydrate(overrides: Partial<Thread> = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread({ thread }, thread.evener.ref, 1000);
}

function delegateInfo(overrides: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo {
  return {
    delegateId: "dlg_1",
    ownerSessionId: "sess_owner",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    transcriptRef: "local:01dlg_1",
    type: "delegate",
    lifecycle: "stable",
    phase: "idle",
    status: "idle",
    outcome: "completed",
    terminal: true,
    resumable: true,
    needsAttention: false,
    projectionRevision: 1,
    ...overrides,
  };
}

function delegateNotification(ref: string, delegate: EvenerDelegateInfo): AnyNotification {
  return {
    method: "evener/delegate/updated",
    params: { ref, delegate },
  } as AnyNotification;
}

function jobsTreeNotification(ref: string, revision: number): AnyNotification {
  return {
    method: "evener/jobs/treeUpdated",
    params: { ref, revision },
  } as AnyNotification;
}

// --- laterActivity edge cases (exercised through delegate/updated) ---

test("delegate/updated adds a new delegate when none exist", () => {
  const model = testHydrate();
  const dlg = delegateInfo({ delegateId: "dlg_new", projectionRevision: 1 });
  const result = applyNotification(model, delegateNotification("ref_t", dlg), 2000);
  expect(result.delegates).toHaveLength(1);
  expect(result.delegates?.[0]).toMatchObject({ delegateId: "dlg_new" });
  expect(result.jobsUpdatedAt).toBe(2000);
  expect(result.lastFrameAt).toBe(2000);
});

test("delegate/updated merges an existing delegate with higher projectionRevision", () => {
  const model = testHydrate();
  // First: add a delegate
  const dlg1 = delegateInfo({ delegateId: "dlg_1", projectionRevision: 1, latestActivityAt: "2026-01-01T00:00:00Z" });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  // Then: update with higher revision
  const dlg2 = delegateInfo({ delegateId: "dlg_1", projectionRevision: 2, latestActivityAt: "2026-01-02T00:00:00Z" });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates).toHaveLength(1);
  expect(result.delegates?.[0]?.projectionRevision).toBe(2);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-02T00:00:00Z");
});

test("delegate/updated with same projectionRevision and later activity updates latestActivityAt", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  // Same revision, later activity
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-03T00:00:00Z");
});

test("delegate/updated with same projectionRevision and earlier activity keeps current activity", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  // Same revision, earlier activity
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-03T00:00:00Z");
});

test("delegate/updated with NaN incoming activity keeps current activity", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  // Incoming with unparseable activity
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "not-a-date",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated with NaN current and valid incoming uses incoming", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "not-a-date",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated with no incoming activity keeps current", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    // no latestActivityAt
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated with no current activity uses incoming", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    // no latestActivityAt
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated with identical delegate is a no-op (returns same delegates reference)", () => {
  const model = testHydrate();
  const dlg = delegateInfo({ delegateId: "dlg_1", projectionRevision: 1, latestActivityAt: "2026-01-01T00:00:00Z" });
  let result = applyNotification(model, delegateNotification("ref_t", dlg), 2000);
  const delegatesBefore = result.delegates;
  // Same delegate again
  result = applyNotification(result, delegateNotification("ref_t", dlg), 3000);
  expect(result.delegates).toBe(delegatesBefore);
});

test("delegate/updated for a different ref is a no-op", () => {
  const model = testHydrate();
  const dlg = delegateInfo({ delegateId: "dlg_1" });
  const result = applyNotification(model, delegateNotification("ref_other", dlg), 2000);
  expect(result.delegates).toEqual([]);
});

// --- evener/jobs/treeUpdated ---

test("jobs/treeUpdated sets jobsTreeRevision when it is null", () => {
  const model = testHydrate();
  const result = applyNotification(model, jobsTreeNotification("ref_t", 5), 2000);
  expect(result.jobsTreeRevision).toBe(5);
  expect(result.jobsUpdatedAt).toBe(2000);
});

test("jobs/treeUpdated with higher revision updates jobsTreeRevision", () => {
  const model = testHydrate();
  let result = applyNotification(model, jobsTreeNotification("ref_t", 5), 2000);
  result = applyNotification(result, jobsTreeNotification("ref_t", 10), 3000);
  expect(result.jobsTreeRevision).toBe(10);
});

test("jobs/treeUpdated with stale revision is a no-op", () => {
  const model = testHydrate();
  let result = applyNotification(model, jobsTreeNotification("ref_t", 10), 2000);
  const before = result.jobsTreeRevision;
  result = applyNotification(result, jobsTreeNotification("ref_t", 5), 3000);
  expect(result.jobsTreeRevision).toBe(before);
});

test("jobs/treeUpdated for a different ref is a no-op", () => {
  const model = testHydrate();
  const result = applyNotification(model, jobsTreeNotification("ref_other", 5), 2000);
  expect(result.jobsTreeRevision).toBeNull();
});

// --- prependOlderTurns edge case (line 307) ---

test("prependOlderTurns keeps an unmatched item in the turn", () => {
  // This exercises the items.push(item) path in mergeToolCallsByCallId
  // when an item from the older turn has no tool call/result pattern.
  // The response uses ThreadTurnsListResponse with .data as wire turns.
  const wireTurn = {
    id: "old_turn",
    status: "completed" as const,
    itemsView: "full",
    items: [{ id: "old_item", type: "message", text: "old message" }],
  };
  const currentTurn: TurnModel = { id: "current", status: "completed", items: [] };
  const model = testHydrate();
  const result = prependOlderTurns({ ...model, turns: [currentTurn] }, { data: [wireTurn], nextCursor: undefined });
  // The old turn with the unmatched item should be prepended
  expect(result.turns.length).toBe(2);
  expect(result.turns[0]?.id).toBe("old_turn");
});
