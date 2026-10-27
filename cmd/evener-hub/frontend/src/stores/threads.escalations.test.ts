import "fake-indexeddb/auto";
import {
  type ConnectionState,
  type QueueState,
  type Thread,
  type ThreadCapabilities,
  type ThreadReadResponse,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  ConflictError,
  clearCachedSessions,
  MAX_RESOLVED_ESCALATIONS_PER_REF,
  resetThreadsStoreForTests,
  setProductionEntryCacheForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `foreground-escalations-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});
function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Omit<Thread["evener"], "queue"> & { queue: Partial<QueueState> };
};

function testThread(ref: string, overrides: TestThreadOverrides = {}): Thread {
  const { evener, ...threadOverrides } = overrides;
  const threadID = threadOverrides.id ?? `thr_${ref}`;
  return {
    id: threadID,
    sessionId: `sess_${ref}`,
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
      ref,
      instanceId: threadID,
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      ...evener,
      queue: { revision: 0, ...evener?.queue },
    },
    ...threadOverrides,
  };
}

function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
  };
}

describe("useThreadsStore.resolveEscalation", () => {
  function threadWithEscalation(ref: string, escalationId: string): ThreadReadResponse {
    return readResponse(ref, {
      evener: {
        ref,
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        pendingEscalations: [
          {
            ref,
            threadId: `thr_${ref}`,
            escalationId,
            mode: "workspace-write",
            tool: "shell",
            kind: "shell",
            deniedPath: "/etc/hosts",
          },
        ],
      },
    });
  }

  test("calls evener/sandbox/escalation/resolve with exact params", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => ({}));

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    const call = fake.calls.find((c) => c.method === "evener/sandbox/escalation/resolve");
    expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: true });
  });

  test("a successful resolve removes the escalation from the tracked model", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => ({}));

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", false);
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a rejected resolve propagates and leaves the model untouched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => {
      throw new Error("sandbox offline");
    });

    const before = threadsStore.getState().threads.get("ref_a");
    await expect(threadsStore.getState().resolveEscalation("ref_a", "esc_1", true)).rejects.toThrow("sandbox offline");
    expect(threadsStore.getState().threads.get("ref_a")).toBe(before); // same reference: untouched
  });

  test("maps a Conflict wire rejection (evenerErrorInfo === conflict) to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    // A stale/double/raced resolve is surfaced as appwire.Conflict() by the
    // daemon (server/appwire_runtime.go's handleAppSandboxEscalationResolve:
    // "Surface it as a conflict so the client can drop the card rather than
    // retry"). resolve must map it to ConflictError like every other mutating
    // action, so the escalation rail treats it as terminal, not retryable.
    fake.on("evener/sandbox/escalation/resolve", () => {
      throw new WireError("escalation is not pending (unknown or already resolved)", -32013, {
        evenerErrorInfo: "conflict",
      });
    });

    const rejection = threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    await expect(rejection).rejects.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toThrow("already resolved");
  });

  test("does not map a same-code, different-evenerErrorInfo WireError to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    // Same wire code (-32013) but a non-conflict evenerErrorInfo — the
    // discriminator is the evenerErrorInfo string, not the code alone.
    fake.on("evener/sandbox/escalation/resolve", () => {
      throw new WireError("something else", -32013, { evenerErrorInfo: "queuedDrainPartial" });
    });

    const rejection = threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toBeInstanceOf(WireError);
  });

  test("a resolve for an escalation absent from the model is a same-reference no-op", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => ({}));

    const before = threadsStore.getState().threads;
    await threadsStore.getState().resolveEscalation("ref_a", "esc_never_pending", true);
    expect(threadsStore.getState().threads).toBe(before);
  });

  // The resolving client is not sent the resolved broadcast, so its own clear
  // is the only one. The transcript repository republishes its model on every
  // later accepted frame, and that model must not still hold the card.
  test("a frame accepted after the resolve does not bring the escalation back", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active", activeFlags: [] } },
    } as never);

    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("active");
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  // A refresh read issued before the resolve can return after it, carrying the
  // escalation in its snapshot. The resolve is local, so the daemon's cut does
  // not order it against that snapshot.
  test("a read issued before the resolve and answered after it does not bring the escalation back", async () => {
    const fake = connectFakeClient();
    let answerRead: (response: ThreadReadResponse) => void = () => {};
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
      const requestGeneration = (params as { requestGeneration?: number }).requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        answerRead = (response) => resolve({ ...response, requestGeneration });
      });
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");

    const refresh = threadsStore.getState().refreshThread("ref_a");
    await vi.waitFor(() => expect(reads).toBe(2));
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    answerRead(threadWithEscalation("ref_a", "esc_1"));
    await refresh;

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  function readCarrying(ids: string[], requestGeneration: number | undefined): ThreadReadResponse {
    return {
      ...readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 0 },
          pendingEscalations: ids.map((escalationId) => ({
            ref: "ref_a",
            threadId: "thr_ref_a",
            escalationId,
            mode: "workspace-write" as const,
            tool: "shell",
            kind: "shell" as const,
            deniedPath: "/etc/hosts",
          })),
        },
      }),
      requestGeneration,
    };
  }

  const MANY_IDS = Array.from({ length: MAX_RESOLVED_ESCALATIONS_PER_REF * 3 }, (_, i) => `esc_${i}`);

  function pendingIds(): string[] | undefined {
    return threadsStore
      .getState()
      .threads.get("ref_a")
      ?.pendingEscalations.map((e) => e.escalationId);
  }

  // The record exists for reads still carrying an escalation the daemon has
  // since seen resolved, which can only be the few most recent. A long session
  // resolves without bound, so the oldest entries are forgotten.
  test("a long run of resolved escalations is bounded and the most recent stay filtered", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_seed");
      return readCarrying(MANY_IDS, (params as { requestGeneration?: number }).requestGeneration);
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    for (const id of MANY_IDS) await threadsStore.getState().resolveEscalation("ref_a", id, true);

    await threadsStore.getState().refreshThread("ref_a");

    expect(pendingIds()).toEqual(MANY_IDS.slice(0, MANY_IDS.length - MAX_RESOLVED_ESCALATIONS_PER_REF));
  });

  // Eviction waits for the read: one issued before the resolves can carry any
  // of them however many resolve before it is answered.
  test("a read in flight across more resolves than the bound still has every one filtered", async () => {
    const fake = connectFakeClient();
    let answerRead: (response: ThreadReadResponse) => void = () => {};
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_seed");
      const requestGeneration = (params as { requestGeneration?: number }).requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        answerRead = (response) => resolve({ ...response, requestGeneration });
      });
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");

    const refresh = threadsStore.getState().refreshThread("ref_a");
    await vi.waitFor(() => expect(reads).toBe(2));
    for (const id of MANY_IDS) await threadsStore.getState().resolveEscalation("ref_a", id, true);
    answerRead(readCarrying(MANY_IDS, undefined));
    await refresh;

    expect(pendingIds()).toEqual([]);
  });

  test("a superseded read answered after the resolve is still discarded", async () => {
    const fake = connectFakeClient();
    let answerRead: (response: ThreadReadResponse) => void = () => {};
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
      return new Promise<ThreadReadResponse>((resolve) => {
        answerRead = resolve;
      });
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    const publishedBefore = threadsStore.getState().hydrations.get("ref_a");

    const refresh = threadsStore.getState().refreshThread("ref_a");
    await vi.waitFor(() => expect(reads).toBe(2));
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    // No requestGeneration echo: the response answers an older request than the
    // one issued, so the reducer discards it.
    answerRead(threadWithEscalation("ref_a", "esc_1"));
    await refresh;

    expect(threadsStore.getState().hydrations.get("ref_a")).toBe(publishedBefore);
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a stale-cursor retry that replaces the repository does not bring a resolved escalation back", async () => {
    const fake = connectFakeClient();
    let answerRetry: (response: ThreadReadResponse) => void = () => {};
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
      const { heldSnapshot, requestGeneration } = params as { heldSnapshot?: unknown; requestGeneration?: number };
      if (heldSnapshot !== undefined)
        throw new WireError("stale", -32000, { evenerErrorInfo: "transcriptItemCursorStale" });
      return new Promise<ThreadReadResponse>((resolve) => {
        answerRetry = (response) => resolve({ ...response, requestGeneration });
      });
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");

    const refresh = threadsStore.getState().refreshThread("ref_a");
    await vi.waitFor(() => expect(reads).toBe(3));
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    answerRetry(threadWithEscalation("ref_a", "esc_1"));
    await refresh;

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a resolve before a stale-cursor retry that replaces the repository stays resolved", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
      const { heldSnapshot, requestGeneration } = params as { heldSnapshot?: unknown; requestGeneration?: number };
      if (heldSnapshot !== undefined)
        throw new WireError("stale", -32000, { evenerErrorInfo: "transcriptItemCursorStale" });
      return { ...threadWithEscalation("ref_a", "esc_1"), requestGeneration };
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    await threadsStore.getState().refreshThread("ref_a");

    expect(reads).toBe(3);
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test.each(["a status frame", "a goal response"] as const)(
    "a resolve acknowledged during a committed Clear stays resolved when %s arrives before the refresh",
    async (arrival) => {
      const fake = connectFakeClient();
      let answerRefresh: (response: ThreadReadResponse) => void = () => {};
      let reads = 0;
      fake.on("thread/read", (params) => {
        reads += 1;
        if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
        const requestGeneration = (params as { requestGeneration?: number }).requestGeneration;
        return new Promise<ThreadReadResponse>((resolve) => {
          answerRefresh = (response) => resolve({ ...response, requestGeneration });
        });
      });
      fake.on("evener/sandbox/escalation/resolve", () => ({}));
      await threadsStore.getState().ensureThread("ref_a");
      await settleEntryTranscriptForTests("ref_a");

      let commitClear: () => void = () => {};
      const heldClear = new Promise<void>((resolve) => {
        commitClear = resolve;
      });
      const originalClear = cache.clear.bind(cache);
      vi.spyOn(cache, "clear").mockImplementation(async () => {
        await heldClear;
        return originalClear();
      });
      const clearing = clearCachedSessions();
      await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
      commitClear();
      expect(await clearing).toEqual({ committed: true });
      await vi.waitFor(() => expect(reads).toBe(2));

      if (arrival === "a status frame") {
        fake.emitNotification({
          method: "thread/status/changed",
          params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active", activeFlags: [] } },
        } as never);
        expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("active");
      } else {
        fake.on("goal/set", () => ({ started: true }));
        await threadsStore.getState().setGoal("ref_a", "ship it");
        expect(threadsStore.getState().threads.get("ref_a")?.goal?.objective).toBe("ship it");
      }
      expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
      answerRefresh(threadWithEscalation("ref_a", "esc_1"));
    },
  );

  test("a watch read issued before the resolve and answered after it does not bring the escalation back", async () => {
    const fake = connectFakeClient();
    let answerWatchRead: (response: ThreadReadResponse) => void = () => {};
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
      const requestGeneration = (params as { requestGeneration?: number }).requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        answerWatchRead = (response) => resolve({ ...response, requestGeneration });
      });
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");

    const watch = threadsStore.getState().watchThread("ref_a");
    await vi.waitFor(() => expect(reads).toBe(2));
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    answerWatchRead(threadWithEscalation("ref_a", "esc_1"));
    await watch;

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a watch-only ref's read issued before the resolve does not bring the escalation back", async () => {
    const fake = connectFakeClient();
    let answerUpgrade: (response: ThreadReadResponse) => void = () => {};
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      if (reads === 1) return threadWithEscalation("ref_a", "esc_1");
      const requestGeneration = (params as { requestGeneration?: number }).requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        answerUpgrade = (response) => resolve({ ...response, requestGeneration });
      });
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().watchThread("ref_a");

    const upgrade = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await vi.waitFor(() => expect(reads).toBe(2));
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    answerUpgrade(threadWithEscalation("ref_a", "esc_1"));
    await upgrade;

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a watch upgrade after the resolve does not bring the escalation back", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", (params) => {
      reads += 1;
      const requestGeneration = (params as { requestGeneration?: number }).requestGeneration;
      return { ...threadWithEscalation("ref_a", "esc_1"), requestGeneration };
    });
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().watchThread("ref_a");
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });

    expect(reads).toBe(2);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("updates BOTH threads and watchedThreads when the same ref is tracked in each", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  // Dual-map atomicity (round-2 fix): the escalation clear in threads and the
  // clear in watchedThreads are one setState, so a synchronous subscriber
  // running between the two halves of the update — which the sequential
  // putThreadModel + putWatchedThreadModel pair this replaced made possible —
  // cannot observe threads updated while watchedThreads still holds the
  // escalation. The subscriber records every intermediate snapshot it sees;
  // after the resolve it must have seen exactly the before-state and the
  // after-state, never a mixed one.
  test("resolveEscalation clears both maps in one setState - no synchronous subscriber sees split state", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    const snapshots: { tracked: number; watched: number }[] = [];
    const unsubscribe = threadsStore.subscribe((state) => {
      snapshots.push({
        tracked: state.threads.get("ref_a")?.pendingEscalations.length ?? -1,
        watched: state.watchedThreads.get("ref_a")?.pendingEscalations.length ?? -1,
      });
    });
    try {
      await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    } finally {
      unsubscribe();
    }

    // Every observed snapshot is consistent: both maps agree on the
    // escalation count (or the model was absent from both, -1). A split
    // update would leave a { tracked: 0, watched: 1 } snapshot behind.
    for (const [i, snap] of snapshots.entries()) {
      expect(snap.tracked, `snapshot ${i} must not be split`).toBe(snap.watched);
    }
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a evener/sandbox/escalation/resolved notification clears the matching card from both tracked and watched models", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);

    // The real wire shape (appwire.SandboxEscalationResolved): {threadId, ref,
    // escalationId} — the daemon's broadcast to every OTHER subscribed client
    // when a pending escalation leaves the set (server/appwire_runtime.go's M7
    // fix). Unlike the local resolveEscalation action, this arrives for a
    // resolve some OTHER client made, so a client that only watches the session
    // still drops its now-stale card.
    fake.emitNotification({
      method: "evener/sandbox/escalation/resolved",
      params: { threadId: "thr_ref_a", ref: "ref_a", escalationId: "esc_1" },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });
});
