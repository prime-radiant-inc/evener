// @vitest-environment node

import { describe, expect, test } from "vitest";
import {
  createMutationProjectionFence,
  type MutationPersistencePort,
  type MutationPersistenceSnapshot,
  replaceTargetRecords,
} from "./projection";
import { outboxRecord, recoveryRecord } from "./testing";

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
    expect(result.apply()).toEqual(new Set(["ref-a"]));
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
    expect(result.apply()).toEqual(new Set(["ref-a", "ref-b"]));
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
  // that started before it but resolves after - for that ref only. A
  // snapshot that discarded ALL its targets whenever ANY of them went stale
  // would also pass this without the second, unaffected ref (ref-b).
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
    resolveRead?.({
      ...emptySnapshot(),
      outbox: [outboxRecord({ targetRef: "ref-a" }), outboxRecord({ clientMutationId: "cmid-2", targetRef: "ref-b" })],
    });
    const result = await stale;
    if (result === false) throw new Error("expected the refresh to be accepted for its other targets");
    const accepted = result.apply();
    expect(accepted.has("ref-a")).toBe(false);
    expect(accepted.has("ref-b")).toBe(true);
  });

  // The generation floor for a target can move between `refresh` resolving
  // (when candidates are first decided) and the caller getting around to
  // calling `apply()` - a live commit's `advance` is exactly that kind of
  // move. `apply()` must re-check at call time, not hand back a decision
  // frozen at resolution.
  test("advance landing after refresh resolves still out-ranks a stale apply", async () => {
    const fence = createMutationProjectionFence();
    const snapshot = { ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] };
    const result = await fence.refresh(fakePort(snapshot), "ref-a");
    if (result === false) throw new Error("expected the refresh to be accepted");
    // A live commit for ref-a lands after the read resolved but before the
    // caller applied it.
    fence.advance("ref-a");
    expect(result.apply().has("ref-a")).toBe(false);
  });

  // apply()'s per-target re-check must use exactly the same floor
  // refresh()'s own resolution-time decision does: an all-targets refresh
  // raises `allTargetsRefreshGeneration` the moment it starts, not when (or
  // whether) its read ever resolves.
  test("apply() excludes a target once an all-targets refresh starts after this refresh resolved", async () => {
    const fence = createMutationProjectionFence();
    const snapshot = { ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] };
    const result = await fence.refresh(fakePort(snapshot), "ref-a");
    if (result === false) throw new Error("expected the refresh to be accepted");
    // Starts a newer all-targets refresh but never resolves it - the same
    // "started, then failed or stayed pending" gap the fence must still
    // fence against.
    void fence.refresh({ read: () => new Promise(() => {}) });
    expect(result.apply().has("ref-a")).toBe(false);
  });

  // reset() clears and reuses generation numbers; apply()'s per-target
  // re-check must also honor the epoch this refresh was captured under,
  // or a pre-reset refresh can pass simply because a post-reset refresh
  // happens to reuse the same generation number.
  test("apply() excludes a target once reset() has moved on, even if generation numbers repeat", async () => {
    const fence = createMutationProjectionFence();
    const snapshot = { ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] };
    const result = await fence.refresh(fakePort(snapshot), "ref-a");
    if (result === false) throw new Error("expected the refresh to be accepted");
    fence.reset();
    const after = await fence.refresh(
      fakePort({ ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] }),
      "ref-a",
    );
    if (after === false) throw new Error("expected the post-reset refresh to be accepted");
    expect(result.apply().has("ref-a")).toBe(false);
    expect(after.apply().has("ref-a")).toBe(true);
  });

  test("a newer refresh that fails still raises the generation floor for an older in-flight refresh", async () => {
    const fence = createMutationProjectionFence();
    let resolveOlder: ((snapshot: MutationPersistenceSnapshot) => void) | undefined;
    const olderPort: MutationPersistencePort = {
      read: () =>
        new Promise((resolve) => {
          resolveOlder = resolve;
        }),
    };
    const older = fence.refresh(olderPort, "ref-a");
    const newer = fence.refresh({ read: () => Promise.reject(new Error("storage read failed")) }, "ref-a");
    expect(await newer).toBe(false);
    resolveOlder?.({ ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] });
    const result = await older;
    if (result === false) throw new Error("expected the older refresh to still resolve with a decision");
    expect(result.apply().has("ref-a")).toBe(false);
  });

  test("a commit landing after a failed newer refresh still out-ranks an older in-flight refresh", async () => {
    const fence = createMutationProjectionFence();
    let resolveOlder: ((snapshot: MutationPersistenceSnapshot) => void) | undefined;
    const olderPort: MutationPersistencePort = {
      read: () =>
        new Promise((resolve) => {
          resolveOlder = resolve;
        }),
    };
    const older = fence.refresh(olderPort, "ref-a");
    expect(await fence.refresh({ read: () => Promise.reject(new Error("storage read failed")) }, "ref-a")).toBe(false);
    // A commit is a request too, later than the older refresh's own -
    // exactly what raises the floor regardless of any failed refresh
    // in between.
    fence.advance("ref-a");
    resolveOlder?.({ ...emptySnapshot(), outbox: [outboxRecord({ targetRef: "ref-a" })] });
    const result = await older;
    if (result === false) throw new Error("expected the older refresh to still resolve with a decision");
    expect(result.apply().has("ref-a")).toBe(false);
  });

  test("epoch reports the fence's current epoch and moves on reset", () => {
    const fence = createMutationProjectionFence();
    const before = fence.epoch();
    fence.reset();
    expect(fence.epoch()).toBe(before + 1);
  });
});
