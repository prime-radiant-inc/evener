// @vitest-environment node

import { expect, test } from "vitest";
import type { HistoryState, ThreadModel, TurnModel } from "./model";
import { isLatestRetryTurn } from "./turnContinuation";

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
    status: { type: "systemError" },
    turns: [turn("older"), turn("latest")],
    ...overrides,
  };
}

test("latest failure identity", () => {
  expect(isLatestRetryTurn(thread(), "latest")).toBe(true);
});

test("older failure identity", () => {
  expect(isLatestRetryTurn(thread(), "older")).toBe(false);
});

test("empty running turn", () => {
  // A running turn with no display row cannot establish that the source
  // failure is the latest canonical turn.
  expect(
    isLatestRetryTurn(
      thread({ turns: [turn("older")], runningTurnId: "latest", status: { type: "active" } }),
      "latest",
    ),
  ).toBe(false);
  // A different running identity prevents Retry even when that turn has no
  // display row either.
  expect(isLatestRetryTurn(thread({ runningTurnId: "new-running" }), "latest")).toBe(false);
  // The same running identity is still eligible when it is also the last
  // canonical turn.
  expect(isLatestRetryTurn(thread({ runningTurnId: "latest", status: { type: "active" } }), "latest")).toBe(true);
});

test("a running identity does not make an older failure the latest turn", () => {
  expect(isLatestRetryTurn(thread({ runningTurnId: "older" }), "older")).toBe(false);
});

test("invalidated history", () => {
  expect(isLatestRetryTurn(thread({ history: history({ invalidatedAtGeneration: 2 }) }), "latest")).toBe(false);
  expect(isLatestRetryTurn(thread({ history: history({ failed: "history read failed" }) }), "latest")).toBe(false);

  // A valid latest-window refresh replaces the invalidated history, and with
  // no different running identity the latest failure is retryable again.
  const refreshed = history({ appliedGeneration: 2, issuedGeneration: 2 });
  expect(isLatestRetryTurn(thread({ history: refreshed }), "latest")).toBe(true);
});

test("active status without a running identity is not enough", () => {
  expect(isLatestRetryTurn(thread({ status: { type: "active" } }), "latest")).toBe(false);
  // Once the live state is coherent the running identity re-establishes the
  // same answer.
  expect(isLatestRetryTurn(thread({ status: { type: "active" }, runningTurnId: "latest" }), "latest")).toBe(true);
});

test("absent history uses the canonical turns and live state", () => {
  expect(isLatestRetryTurn(thread(), "latest")).toBe(true);
  expect(isLatestRetryTurn(thread(), "older")).toBe(false);
  expect(isLatestRetryTurn(thread({ turns: [] }), "latest")).toBe(false);
});
