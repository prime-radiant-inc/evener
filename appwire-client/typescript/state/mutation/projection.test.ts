import { describe, expect, test } from "vitest";
import {
  createMutationProjectionFence,
  type MutationPersistencePort,
  type MutationPersistenceSnapshot,
  replaceTargetRecords,
} from "./projection";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "./records";

function outboxRecord(overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
  return {
    version: 1,
    clientMutationId: "cmid-1",
    targetRef: "ref-a",
    method: "turn/start",
    payload: {},
    attachments: [],
    optimisticDisplay: null,
    intentSequence: 0,
    createdAt: 0,
    state: "submitting",
    ...overrides,
  };
}

function recoveryRecord(overrides: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
  return { ...outboxRecord(), recoveryKind: "rejected", ...overrides };
}

function emptySnapshot(): MutationPersistenceSnapshot {
  return { outbox: [], optimistic: [], recovery: [] };
}

function fakePort(snapshot: MutationPersistenceSnapshot): MutationPersistencePort {
  return { read: () => Promise.resolve(snapshot) };
}

describe("replaceTargetRecords", () => {
  test("replaces a targeted ref's existing records with the new records for that ref", () => {
    const current = new Map([
      ["cmid-1", outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a" })],
      ["cmid-2", outboxRecord({ clientMutationId: "cmid-2", targetRef: "ref-b" })],
    ]);
    const next = replaceTargetRecords(current, new Set(["ref-a"]), [
      outboxRecord({ clientMutationId: "cmid-3", targetRef: "ref-a" }),
    ]);
    expect([...next.keys()].sort()).toEqual(["cmid-2", "cmid-3"]);
  });

  test("drops a targeted ref's existing records when no replacement names them", () => {
    const current = new Map([["cmid-1", outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a" })]]);
    const next = replaceTargetRecords(current, new Set(["ref-a"]), []);
    expect(next.size).toBe(0);
  });

  test("leaves an untargeted ref's records untouched", () => {
    const current = new Map([["cmid-1", outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a" })]]);
    const next = replaceTargetRecords(current, new Set(["ref-b"]), []);
    expect(next.get("cmid-1")).toEqual(current.get("cmid-1"));
  });
});

describe("createMutationProjectionFence", () => {
  test("accepts a fresh refresh for its own single target", async () => {
    const fence = createMutationProjectionFence();
    const snapshot = { ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] };
    const result = await fence.refresh(fakePort(snapshot), "ref-a");
    if (result === false) throw new Error("expected the refresh to be accepted");
    expect(result.targets).toEqual(new Set(["ref-a"]));
    expect(result.snapshot).toBe(snapshot);
  });

  test("an all-targets refresh accepts every ref its snapshot names", async () => {
    const fence = createMutationProjectionFence();
    const snapshot = {
      ...emptySnapshot(),
      outbox: [outboxRecord({ targetRef: "ref-a" })],
      recovery: [recoveryRecord({ targetRef: "ref-b" })],
    };
    const result = await fence.refresh(fakePort(snapshot));
    if (result === false) throw new Error("expected the refresh to be accepted");
    expect(result.targets).toEqual(new Set(["ref-a", "ref-b"]));
  });

  test("a port read failure is reported as an unaccepted refresh", async () => {
    const fence = createMutationProjectionFence();
    const port: MutationPersistencePort = {
      read: () => Promise.reject(new Error("storage read failed")),
    };
    expect(await fence.refresh(port, "ref-a")).toBe(false);
  });

  test("reset invalidates a refresh already awaiting its read", async () => {
    const fence = createMutationProjectionFence();
    let resolveRead: (() => void) | undefined;
    const port: MutationPersistencePort = {
      read: () =>
        new Promise((resolve) => {
          resolveRead = () => resolve(emptySnapshot());
        }),
    };
    const pending = fence.refresh(port, "ref-a");
    fence.reset();
    resolveRead?.();
    expect(await pending).toBe(false);
  });

  // Mirrors the web's own oracle (pendingTurnsStore.test.ts "an older
  // all-target projection cannot erase a newly committed send"): a live
  // commit's `advance` for one ref must out-rank a slower all-targets read
  // that started before it but resolves after.
  test("advance out-ranks a slower in-flight all-targets refresh for that one ref", async () => {
    const fence = createMutationProjectionFence();
    let resolveRead: ((snapshot: MutationPersistenceSnapshot) => void) | undefined;
    const port: MutationPersistencePort = {
      read: () =>
        new Promise((resolve) => {
          resolveRead = resolve;
        }),
    };
    const stale = fence.refresh(port);
    fence.advance("ref-a");
    resolveRead?.({ ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] });
    const result = await stale;
    if (result === false) throw new Error("expected the refresh to be accepted for its other targets");
    expect(result.targets.has("ref-a")).toBe(false);
  });

  test("epoch reports the fence's current epoch and moves on reset", () => {
    const fence = createMutationProjectionFence();
    const before = fence.epoch();
    fence.reset();
    expect(fence.epoch()).toBe(before + 1);
  });
});
