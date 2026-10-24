// @vitest-environment node

import { expect, test } from "vitest";
import type { HistoryState, ThreadModel, TurnModel } from "./model";
import { type RetryEligibilityInput, retryEligibility } from "./retryEligibility";
import type { PendingTurnEntry } from "./state/mutation";

type RetryThread = Pick<ThreadModel, "turns" | "runningTurnId" | "history" | "status">;

function turn(id: string): TurnModel {
  return { id, status: "completed", items: [] };
}

function history(overrides: Partial<HistoryState> = {}): HistoryState {
  return {
    bootGeneration: "generation-1",
    epoch: 1,
    length: 2,
    appliedGeneration: 1,
    issuedGeneration: 1,
    deferredPages: [],
    turns: [],
    ...overrides,
  };
}

function thread(overrides: Partial<RetryThread> = {}): RetryThread {
  return {
    status: { type: "idle" },
    turns: [turn("older"), turn("latest")],
    ...overrides,
  };
}

function entry(overrides: Partial<PendingTurnEntry> = {}): PendingTurnEntry {
  return {
    id: "entry-1",
    ref: "ref",
    method: "send",
    text: "continue",
    imageCount: 0,
    skillNames: [],
    state: "submitting",
    source: "outbox",
    fromThisClient: true,
    ...overrides,
  };
}

function input(overrides: Partial<RetryEligibilityInput> = {}): RetryEligibilityInput {
  return {
    clickedTurnId: "latest",
    thread: thread(),
    clickedTurnFailed: true,
    pendingMutations: [],
    pendingMutationsReady: true,
    serviceReady: true,
    ...overrides,
  };
}

test("allowed when every fact holds", () => {
  expect(retryEligibility(input())).toEqual({ allowed: true, reason: "allowed" });
});

test("not the latest turn", () => {
  expect(retryEligibility(input({ clickedTurnId: "older" }))).toEqual({
    allowed: false,
    reason: "not-latest-turn",
  });
});

test("isLatestRetryTurn's live-state vetoes are the latest-turn answer", () => {
  // A different running turn hides Retry even when its own turn has no items.
  expect(retryEligibility(input({ thread: thread({ runningTurnId: "newer" }) }))).toEqual({
    allowed: false,
    reason: "not-latest-turn",
  });
  // Invalidated history cannot establish the tail until its owner refreshes.
  expect(retryEligibility(input({ thread: thread({ history: history({ invalidatedAtGeneration: 2 }) }) }))).toEqual({
    allowed: false,
    reason: "not-latest-turn",
  });
});

test("clicked turn no longer carries a failure", () => {
  expect(retryEligibility(input({ clickedTurnFailed: false }))).toEqual({
    allowed: false,
    reason: "clicked-turn-not-failed",
  });
});

test.each([
  ["an own unresolved send", entry({ method: "send" })],
  ["an own unresolved queue", entry({ method: "queue" })],
] as const)("%s owns the input", (_name, pending) => {
  expect(retryEligibility(input({ pendingMutations: [pending] }))).toEqual({
    allowed: false,
    reason: "pending-input-owned",
  });
});

test("ownPendingTurnInput's non-owning rows do not block", () => {
  // The shared predicate owns the canceled/foreign/other-method rules; this
  // decision must not restate them.
  for (const pending of [
    entry({ fromThisClient: false }),
    entry({ state: "canceled" }),
    entry({ method: "steer" }),
    entry({ method: "drain" }),
    entry({ method: "promote" }),
  ]) {
    expect(retryEligibility(input({ pendingMutations: [pending] }))).toEqual({
      allowed: true,
      reason: "allowed",
    });
  }
});

test("pending projection not ready", () => {
  expect(retryEligibility(input({ pendingMutations: null, pendingMutationsReady: false }))).toEqual({
    allowed: false,
    reason: "pending-not-ready",
  });
});

test("no live service", () => {
  expect(retryEligibility(input({ serviceReady: false }))).toEqual({
    allowed: false,
    reason: "no-live-service",
  });
});

test("no live service wins over pending input and an unread projection", () => {
  // Native's old !canSend answer hid the action outright, before asking the
  // pending owner. Keeping that answer is why service readiness precedes both
  // pending denials.
  expect(
    retryEligibility(
      input({
        serviceReady: false,
        pendingMutations: [entry()],
        pendingMutationsReady: false,
      }),
    ),
  ).toEqual({ allowed: false, reason: "no-live-service" });
});

test("precedence: not latest, failed, no service, pending, not ready", () => {
  // Each case sets every later fact to a denial too; the earlier reason wins.
  const allDenied = {
    clickedTurnId: "older",
    clickedTurnFailed: false,
    pendingMutations: [entry()],
    pendingMutationsReady: false,
    serviceReady: false,
  } as const;
  expect(retryEligibility(input(allDenied)).reason).toBe("not-latest-turn");
  expect(retryEligibility(input({ ...allDenied, clickedTurnId: "latest" })).reason).toBe("clicked-turn-not-failed");
  expect(retryEligibility(input({ ...allDenied, clickedTurnId: "latest", clickedTurnFailed: true })).reason).toBe(
    "no-live-service",
  );
  expect(
    retryEligibility(
      input({
        ...allDenied,
        clickedTurnId: "latest",
        clickedTurnFailed: true,
        serviceReady: true,
      }),
    ).reason,
  ).toBe("pending-input-owned");
  expect(
    retryEligibility(
      input({
        ...allDenied,
        clickedTurnId: "latest",
        clickedTurnFailed: true,
        serviceReady: true,
        pendingMutations: [],
      }),
    ).reason,
  ).toBe("pending-not-ready");
});
