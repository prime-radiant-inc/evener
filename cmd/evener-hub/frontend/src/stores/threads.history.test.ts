// The web store's adoption of the shared reducer's versioned-history contract
// (task-15-report.md's "Contract for Tasks 16/17"; spec docs/superpowers/specs/
// 2026-09-25-transcript-read-model-design.md "Reads", "Live history
// notifications"). Covers the request-generation/heldSnapshot wiring on
// thread/read and the store obligations that ride it: a superseded
// generation never overwrites a newer one, a resync with a higher epoch
// re-reads and replaces, and a daemonless backfill page keeps a page newer
// than the one it raced.
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  ThreadCapabilities,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { resetThreadsStoreForTests, threadsStore } from "./threads";

const REF = "ref_h";
const THREAD_ID = "thr_h";

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

interface Snapshot {
  bootGeneration?: string;
  epoch?: number;
  incarnation?: string;
  length?: number;
}

function read(
  label: string,
  options: Snapshot & { requestGeneration?: number; authoritative?: boolean } = {},
): ThreadReadResponse {
  return {
    thread: {
      id: THREAD_ID,
      sessionId: "sess_h",
      preview: "",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/project",
      cliVersion: "1.0.0",
      source: "evener",
      name: label,
      turns: [
        {
          id: `turn_${label}`,
          itemsView: "full",
          status: "completed",
          version: 1,
          items: [
            {
              type: "agentMessage",
              id: `apptranscript-item-v2:turn_${label}:0:0`,
              transcriptKey: `apptranscript-item-v2:turn_${label}:0:0`,
              turnId: `turn_${label}`,
              position: { entry: 1, item: 0 },
              version: 1,
              text: label,
            },
          ],
        },
      ],
      evener: { ref: REF, capabilities: CAPABILITIES, queue: { revision: 0 } },
    },
    requestGeneration: options.requestGeneration,
    bootGeneration: options.bootGeneration ?? "1",
    epoch: options.epoch ?? 0,
    snapshot: { incarnation: options.incarnation ?? "inc_a", length: options.length ?? 100 },
    overlay: [],
    ...(options.authoritative === undefined ? {} : { authoritative: options.authoritative }),
  };
}

function page(
  id: string,
  options: Snapshot & { authoritative?: boolean; nextCursor?: string } = {},
): ThreadTurnsListResponse {
  return {
    data: [{ id, itemsView: "full", status: "completed", version: 1, items: [] }],
    nextCursor: options.nextCursor,
    bootGeneration: options.bootGeneration ?? "1",
    epoch: options.epoch ?? 0,
    snapshot: { incarnation: options.incarnation ?? "inc_a", length: options.length ?? 100 },
    ...(options.authoritative === undefined ? {} : { authoritative: options.authoritative }),
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function modelName(): string | undefined {
  return threadsStore.getState().threads.get(REF)?.name;
}

beforeEach(async () => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(async () => {
  resetThreadsStoreForTests();
});

describe("versioned history: request generations", () => {
  test("a slow response to an older request generation arriving after a newer one does not overwrite it", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => read("initial"));
    await threadsStore.getState().ensureThread(REF);
    expect(modelName()).toBe("initial");

    // Two overlapping re-reads for the same ref: refreshThread's targeted
    // path never dedups against an in-flight predecessor (refreshTrackedThread),
    // so both issue their own thread/read with their own bumped generation
    // before either resolves.
    const resolvers = new Map<number, (resp: ThreadReadResponse) => void>();
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolvers.set(params.requestGeneration ?? -1, resolve);
        }),
    );
    const first = threadsStore.getState().refreshThread(REF);
    const second = threadsStore.getState().refreshThread(REF);
    await Promise.resolve();
    await Promise.resolve();
    expect([...resolvers.keys()].sort()).toEqual([1, 2]);

    // Resolve the newer generation first, then the older, slow one.
    resolvers.get(2)?.(read("gen-2", { requestGeneration: 2, length: 200 }));
    await second;
    expect(modelName()).toBe("gen-2");

    resolvers.get(1)?.(read("gen-1", { requestGeneration: 1, length: 150 }));
    await first;
    expect(modelName()).toBe("gen-2");
  });
});

describe("versioned history: resync", () => {
  test("a resync push with a higher epoch re-reads and replaces", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => read("before"));
    await threadsStore.getState().ensureThread(REF);
    expect(modelName()).toBe("before");
    expect(threadsStore.getState().threads.get(REF)?.history?.epoch).toBe(0);

    fake.on("thread/read", () => read("after-resync", { epoch: 1, requestGeneration: 1 }));
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: THREAD_ID, ref: REF, bootGeneration: "1", epoch: 1 },
    } as AnyNotification);

    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(modelName()).toBe("after-resync");
    expect(threadsStore.getState().threads.get(REF)?.history?.epoch).toBe(1);
    expect(
      threadsStore
        .getState()
        .threads.get(REF)
        ?.turns.map((t) => t.id),
    ).toEqual(["turn_after-resync"]);
  });
});

describe("versioned history: backfill pages", () => {
  test("a daemonless backfill page keeps newer pages", async () => {
    const fake = connectFakeClient();
    // The initial latest-window read's snapshot length (100) is what a
    // later page's own snapshot length is judged against - held.length
    // advances only from latest-window reads, never from pages (spec's
    // "Backfill accumulation" follow-up: a page's length is the transcript
    // length at read time, not a promise everything through it was
    // delivered, so advancing held to it could make a later heldSnapshot
    // claim completeness through changes the client never actually saw),
    // so a page below it is out of order no matter how many other pages
    // have merged since.
    fake.on("thread/read", () => ({ ...read("initial"), olderCursor: "cursor_1" }));
    await threadsStore.getState().ensureThread(REF);

    fake.on("thread/turns/list", () => page("turn_new", { length: 120, nextCursor: "cursor_2" }));
    await threadsStore.getState().loadOlderTurns(REF);
    expect(
      threadsStore
        .getState()
        .threads.get(REF)
        ?.turns.map((t) => t.id),
    ).toContain("turn_new");
    expect(threadsStore.getState().threads.get(REF)?.history?.length).toBe(100);

    // A page that arrives after it, from a snapshot shorter than the one the
    // client already holds (the initial read's length 100), is stale - it
    // must be discarded, not merged over the newer page above.
    fake.on("thread/turns/list", () => page("turn_stale", { length: 90 }));
    await threadsStore.getState().loadOlderTurns(REF);

    const turnIds = threadsStore
      .getState()
      .threads.get(REF)
      ?.turns.map((t) => t.id);
    expect(turnIds).toContain("turn_new");
    expect(turnIds).not.toContain("turn_stale");
    expect(threadsStore.getState().threads.get(REF)?.history?.length).toBe(100);
  });
});

describe("versioned history: below-floor held snapshot", () => {
  test("a stale-cursor rejection retries thread/read without the held snapshot", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => read("initial"));
    await threadsStore.getState().ensureThread(REF);
    expect(modelName()).toBe("initial");

    fake.on("thread/read", (params) => {
      // The held snapshot named a length the kept update log no longer
      // reaches: the hub answers transcriptItemCursorStale rather than
      // silently merging a bare window (spec's "Below-floor update-log
      // requests" follow-up). The retry must ask again with no held
      // snapshot at all.
      if (params.heldSnapshot !== undefined) {
        throw new WireError("stale", -32000, { evenerErrorInfo: "transcriptItemCursorStale" });
      }
      return read("recovered", { length: 300, requestGeneration: params.requestGeneration });
    });
    await threadsStore.getState().refreshThread(REF);

    expect(modelName()).toBe("recovered");
    expect(threadsStore.getState().threads.get(REF)?.history?.length).toBe(300);
    const readCalls = fake.calls.filter((c) => c.method === "thread/read");
    expect(readCalls.length).toBeGreaterThanOrEqual(2);
    const last = readCalls[readCalls.length - 1]?.params as { heldSnapshot?: unknown };
    expect(last.heldSnapshot).toBeUndefined();
  });

  // roborev finding on this PR: the retry must fully replace held history,
  // not merge into it through applyReadResponse (same incarnation/epoch,
  // length >= held's, so readDisposition would call it a "merge" and never
  // touch a held item outside the fresh window) - that would leave exactly
  // the items TranscriptItemCursorStale exists to protect (one outside the
  // latest window whose completion landed after the held length) stale
  // forever.
  test("a stale-cursor retry discards an older held turn outside the recovered window, not merges it", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({ ...read("initial"), olderCursor: "cursor_1" }));
    await threadsStore.getState().ensureThread(REF);

    fake.on("thread/turns/list", () => page("turn_old", { length: 100 }));
    await threadsStore.getState().loadOlderTurns(REF);
    expect(
      threadsStore
        .getState()
        .threads.get(REF)
        ?.turns.map((t) => t.id),
    ).toContain("turn_old");

    fake.on("thread/read", (params) => {
      if (params.heldSnapshot !== undefined) {
        throw new WireError("stale", -32000, { evenerErrorInfo: "transcriptItemCursorStale" });
      }
      return read("recovered", { length: 300, requestGeneration: params.requestGeneration });
    });
    await threadsStore.getState().refreshThread(REF);

    const turnIds = threadsStore
      .getState()
      .threads.get(REF)
      ?.turns.map((t) => t.id);
    expect(turnIds).not.toContain("turn_old");
    expect(turnIds).toEqual(["turn_recovered"]);
  });
});
