import "fake-indexeddb/auto";
import type { NavigationReadParams, NavigationReadResponse, NavigationSessionSummary } from "@evener/appwire-client";
import {
  type BackgroundLivePages,
  NAVIGATION_SECTION_LIMIT,
  selectLiveRows,
  selectNextSectionOffset,
} from "@evener/appwire-client/state/navigation";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { navigationInvalidatedNotification } from "@evener/appwire-client/testing/notifications";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { type BackgroundTranscriptsOwner, eligibleLiveRefs, startBackgroundTranscripts } from "./backgroundTranscripts";
import { connectionStore } from "./connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "./navigation/store";
import { SessionCacheIndexedDB, type SessionCacheWriteOutcome } from "./sessionCacheIndexedDB";
import {
  backgroundTranscriptSnapshotForTests,
  resetThreadsStoreForTests,
  setSessionCacheAdapterForTests,
  threadsStore,
} from "./threads";

const GENERATION = "generation_test";
const REPLACEMENT_GENERATION = "generation_replacement";
const PAGE_LIMIT = NAVIGATION_SECTION_LIMIT;

function liveRow(ref: string, fields: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "p",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...fields,
  };
}

interface PageSpec {
  rows: NavigationSessionSummary[];
  remaining: number;
  truncated?: boolean;
}
interface World {
  generation: string;
  revision: number;
  pages: Map<number, PageSpec | "fail">;
}

let world: World;
let sequence: number;
let fake: FakeClient;
let cache: SessionCacheIndexedDB;

// A held overview page: the next read of `holdOffset` blocks on `holdGate`
// until releaseHeldPage(), so a test can interleave an invalidation or a
// roster change with a sweep's terminal-page request.
let holdOffset: number | null = null;
let holdGate: Promise<void> = Promise.resolve();
let holdRelease: (() => void) | null = null;
let holdReached: (() => void) | null = null;
let holdReachedGate: Promise<void> = Promise.resolve();
// The read ordinal of `holdOffset` that blocks (1 = the next read). A later
// ordinal bounds an owner whose own publications otherwise re-sweep forever.
let holdAtRead = 1;
let holdSeen = 0;

function holdNextPage(offset: number): void {
  holdPageAt(offset, 1);
}
function holdPageAt(offset: number, nth: number): void {
  holdOffset = offset;
  holdAtRead = Math.max(1, nth);
  holdSeen = 0;
  holdGate = new Promise<void>((resolve) => {
    holdRelease = resolve;
  });
  holdReachedGate = new Promise<void>((resolve) => {
    holdReached = resolve;
  });
}
function waitForHeldPage(): Promise<void> {
  return holdReachedGate;
}
function releaseHeldPage(): void {
  holdOffset = null;
  holdRelease?.();
  holdRelease = null;
}

function totalRows(): number {
  let total = 0;
  for (const page of world.pages.values()) if (page !== "fail") total += page.rows.length;
  return total;
}

async function navigationRead(params: NavigationReadParams): Promise<NavigationReadResponse> {
  if (params.resource === "manifest") {
    return wireSnapshot(
      params,
      {
        generation_id: world.generation,
        revision: world.revision,
        sources: [],
        attentionSummary: { needsYou: 0, error: 0, working: 0 },
        sections: { live: { count: totalRows() }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
      },
      `"manifest-${world.revision}"`,
      world.revision,
      world.generation,
    );
  }
  if (params.resource === "section") {
    const offset = params.offset ?? 0;
    if (holdOffset === offset) {
      holdSeen += 1;
      if (holdSeen >= holdAtRead) {
        holdReached?.();
        await holdGate;
      }
    }
    const spec = world.pages.get(offset);
    if (spec === "fail") throw new Error(`live page ${offset} unavailable`);
    const rows = params.section === "live" ? (spec?.rows ?? []) : [];
    const remaining = spec?.remaining ?? 0;
    return wireSnapshot(
      params,
      { sessions: rows, remaining, truncated: spec?.truncated ?? remaining > 0 },
      `"live-${offset}-${world.revision}"`,
      world.revision,
      world.generation,
    );
  }
  return wireSnapshot(
    params,
    { sessions: [], remaining: 0, truncated: false },
    `"${params.resource}"`,
    1,
    world.generation,
  );
}

function connectFake(capabilityVersion = 1): FakeClient {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", navigationRead);
  client.on("thread/read", (params) => ({
    ...currentRead(params.ref ?? "local:root"),
    requestGeneration: (params as { requestGeneration?: number }).requestGeneration ?? 1,
  }));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  initNavigation(client, {
    version: capabilityVersion,
    generationId: world.generation,
    sequence: 0,
    readVersions: [3],
  });
  return client;
}

function invalidate(revision: number): void {
  world.revision = revision;
  sequence += 1;
  fake.emitNotification(
    navigationInvalidatedNotification({
      generationId: world.generation,
      sequence,
      targets: [{ kind: "section", section: "live", revision }],
    }),
  );
}

function threadReadRefs(): Set<string> {
  return new Set(
    fake.calls
      .filter((call) => call.method === "thread/read")
      .map((call) => (call.params as { ref?: string }).ref ?? ""),
  );
}
function threadUnsubRefs(): string[] {
  return fake.calls
    .filter((call) => call.method === "thread/unsubscribe")
    .map((call) => (call.params as { ref?: string }).ref ?? "");
}

function sectionReadCount(): number {
  return fake.calls.filter(
    (call) => call.method === "evener/navigation/read" && (call.params as NavigationReadParams).resource === "section",
  ).length;
}

// The number of wire reads of a single Live section page.
function pageReadCount(offset: number): number {
  return fake.calls.filter(
    (call) =>
      call.method === "evener/navigation/read" &&
      (call.params as NavigationReadParams).resource === "section" &&
      (call.params as NavigationReadParams).offset === offset,
  ).length;
}

// Under injected fake timers a background-thread release can arm a drained
// cache-write deadline, so disposal must be allowed to advance time to settle
// (the same harness detail the Task7a probes recorded; not product behavior).
async function disposeOwner(owner: BackgroundTranscriptsOwner): Promise<void> {
  const disposed = owner.dispose();
  await vi.advanceTimersByTimeAsync(8_000);
  await disposed;
}

// Waits for an OBSERVED boundary, failing loudly if it never arrives: a
// FakeClient round trip settles within microtasks, so a bounded drain is
// enough, but silently returning on expiry would let a stalled owner pass.
async function awaitObserved(done: () => boolean, what: string, maxTurns = 400): Promise<void> {
  for (let turn = 0; turn < maxTurns; turn += 1) {
    if (done()) return;
    await Promise.resolve();
  }
  throw new Error(`observed boundary not reached: ${what}`);
}

// A single terminal page whose read fails.
function failPage(): Map<number, PageSpec | "fail"> {
  return new Map([[0, "fail"]]);
}

// True when any loaded Live section page currently materializes `ref`.
function livePageHas(ref: string): boolean {
  for (const resource of navigationStore.getState().resources.values()) {
    if (resource.key.kind !== "section" || resource.key.section !== "live") continue;
    const sessions = (resource.data as { sessions?: NavigationSessionSummary[] } | null)?.sessions ?? [];
    if (sessions.some((row) => row.ref === ref)) return true;
  }
  return false;
}

// A thread read carrying one recorded, positioned turn, so the hydrated model
// has versioned history and its release drains a real cache write.
function versionedRead(ref: string, requestGeneration: number) {
  const base = currentRead(ref);
  base.snapshot = { incarnation: "inc-1", length: 1 };
  base.thread.turns = [
    {
      id: "turn_1",
      itemsView: "full",
      status: "completed",
      version: 1,
      items: [
        {
          type: "agentMessage",
          id: "turn_1-item-0",
          transcriptKey: "apptranscript-item-v2:turn_1:0:0",
          turnId: "turn_1",
          position: { entry: 2, item: 0 },
          version: 1,
          text: "recorded",
        },
      ],
    },
  ];
  return { ...base, requestGeneration };
}

// A single-terminal-page roster: page 0 holds `rows`, and `remaining` says
// whether a later page exists. The owner advances by row count, so a page
// that is not terminal must have at least one row.
function onePage(rows: NavigationSessionSummary[], remaining = 0): Map<number, PageSpec | "fail"> {
  return new Map([[0, { rows, remaining }]]);
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
  cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), databaseName: `bg-roster-${crypto.randomUUID()}` });
  setSessionCacheAdapterForTests(cache);
  world = { generation: GENERATION, revision: 1, pages: new Map() };
  sequence = 0;
  holdOffset = null;
  holdAtRead = 1;
  holdSeen = 0;
  holdGate = Promise.resolve();
  holdRelease = null;
  holdReached = null;
  holdReachedGate = Promise.resolve();
});

afterEach(() => {
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
  setSessionCacheAdapterForTests(undefined);
  cache.close();
});

describe("eligibleLiveRefs", () => {
  test("walks children recursively and keeps only opaque Live session refs", () => {
    expect([
      ...eligibleLiveRefs([
        liveRow("local:idle", { children: [liveRow("remote-a:nested", { host_id: "remote-a" })] }),
        liveRow("remote-b:same-id", { host_id: "remote-b" }),
        liveRow("local:idle"),
        liveRow("local:delegate", { kind: "subagent" }),
        liveRow("local:ended", { live: false }),
      ]),
    ]).toEqual(["local:idle", "remote-a:nested", "remote-b:same-id"]);
  });
});

describe("startBackgroundTranscripts", () => {
  test("owns every Live page without moving the visible rail boundary", async () => {
    // Three full pages at the section limit. Page 0 carries a remote session;
    // the terminal page carries an idle remote session. (The v3 navigation
    // codec keeps session summaries flat - Go navigation_schema.go "Session
    // summaries are flat" / TS codec.ts:741 reject a session's non-empty owned
    // children slot - so nested-row recursion is proven by the eligibleLiveRefs
    // unit test above; the real-wire roster is flat.)
    const pageRows = (offset: number): NavigationSessionSummary[] => {
      if (offset === 0) {
        const rows = [liveRow("remote-a:nested", { host_id: "remote-a" }), liveRow("local:root")];
        for (let index = 2; index < PAGE_LIMIT; index += 1) rows.push(liveRow(`local:p0-${index}`));
        return rows;
      }
      if (offset === PAGE_LIMIT) {
        return Array.from({ length: PAGE_LIMIT }, (_unused, index) => liveRow(`local:p1-${index}`));
      }
      const rows = [liveRow("remote-a:last-page-idle", { host_id: "remote-a" })];
      for (let index = 1; index < PAGE_LIMIT; index += 1) rows.push(liveRow(`local:p2-${index}`));
      return rows;
    };
    world.pages = new Map([
      [0, { rows: pageRows(0), remaining: PAGE_LIMIT * 2 }],
      [PAGE_LIMIT, { rows: pageRows(PAGE_LIMIT), remaining: PAGE_LIMIT }],
      [PAGE_LIMIT * 2, { rows: pageRows(PAGE_LIMIT * 2), remaining: 0 }],
    ]);

    fake = connectFake();
    await navigationStore.getState().loadSection("live", 0);
    await navigationStore.getState().loadSection("live", PAGE_LIMIT);

    const visible = selectLiveRows(navigationStore.getState()).map((row) => row.ref);
    const nextOffset = selectNextSectionOffset("live", navigationStore.getState());
    expect(visible.length).toBe(PAGE_LIMIT * 2);
    expect(nextOffset).toBe(PAGE_LIMIT * 2);

    fake.calls.length = 0;
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();

    const reads = threadReadRefs();
    expect(reads.has("remote-a:last-page-idle")).toBe(true);
    expect(reads.has("remote-a:nested")).toBe(true);
    expect(threadUnsubRefs()).toEqual([]);

    expect(selectLiveRows(navigationStore.getState()).map((row) => row.ref)).toEqual(visible);
    expect(selectNextSectionOffset("live", navigationStore.getState())).toBe(nextOffset);

    await owner.dispose();
  });

  test("does not release a held ref until a coherent sweep returns to the terminal page", async () => {
    // page 0 (1 row, remaining 1) advances to page 1.
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:b")], remaining: 0 }],
    ]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();
    expect(threadReadRefs()).toEqual(new Set(["local:a", "local:b"]));

    // Remove b, but hold the terminal page's read so the sweep cannot prove
    // the roster complete yet.
    holdNextPage(1);
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [], remaining: 0 }],
    ]);
    invalidate(2);
    // The sweep is blocked on the held terminal page and cannot have decided
    // yet, so no release may have been issued.
    await waitForHeldPage();
    expect(threadUnsubRefs()).toEqual([]);

    releaseHeldPage();
    await owner.settled();
    expect(threadUnsubRefs()).toEqual(["local:b"]);
    expect(threadReadRefs().has("local:a")).toBe(true);

    await owner.dispose();
  });

  test("an invalidation during the terminal-page request cannot release absent refs", async () => {
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:b")], remaining: 0 }],
    ]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();

    // The roster does not change (a and b stay Live), but the terminal page is
    // invalidated mid-read.
    holdNextPage(1);
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:b")], remaining: 0 }],
    ]);
    invalidate(2);
    await waitForHeldPage();
    invalidate(3);
    releaseHeldPage();
    await owner.settled();

    expect(threadUnsubRefs()).toEqual([]);
    expect(threadReadRefs().has("local:a")).toBe(true);
    expect(threadReadRefs().has("local:b")).toBe(true);

    await owner.dispose();
  });

  test("a failed page retains every existing owner until a coherent sweep retires only the removed ref", async () => {
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:b")], remaining: 0 }],
    ]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();
    expect(threadReadRefs()).toEqual(new Set(["local:a", "local:b"]));

    // Reorder page 0 (b stays, a is gone) and fail the terminal page.
    world.pages = new Map<number, PageSpec | "fail">([
      [0, { rows: [liveRow("local:b")], remaining: 1 }],
      [1, "fail"],
    ]);
    invalidate(2);
    await owner.settled();
    expect(threadUnsubRefs()).toEqual([]);

    // A coherent replacement sweep (terminal page recovers, c arrives) retires
    // only a.
    world.pages = new Map([
      [0, { rows: [liveRow("local:b")], remaining: 1 }],
      [1, { rows: [liveRow("local:c")], remaining: 0 }],
    ]);
    invalidate(3);
    await owner.settled();
    expect(threadUnsubRefs()).toEqual(["local:a"]);
    expect(threadReadRefs().has("local:b")).toBe(true);
    expect(threadReadRefs().has("local:c")).toBe(true);

    await owner.dispose();
  });

  test("a non-advancing truncated page ends the sweep without releasing owners", async () => {
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:b")], remaining: 0 }],
    ]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();

    // A truncated page that returns no rows yet claims more data must not spin
    // or be trusted as complete.
    world.pages = new Map([[0, { rows: [], remaining: 5 }]]);
    invalidate(2);
    await owner.settled();
    expect(threadUnsubRefs()).toEqual([]);
    const sectionReads = fake.calls.filter(
      (call) =>
        call.method === "evener/navigation/read" && (call.params as NavigationReadParams).resource === "section",
    ).length;
    expect(sectionReads).toBeLessThan(20);

    await owner.dispose();
  });

  test("duplicate refs across pages are owned once and released once", async () => {
    // a appears on both pages. The owner must acquire it once (and release it
    // once). Duplicating a *within* one page is a wire error (duplicate logical
    // identity), so the duplication is across pages. page 0 has two rows, so
    // the owner advances to offset 2.
    world.pages = new Map([
      [0, { rows: [liveRow("local:a"), liveRow("local:b")], remaining: 1 }],
      [2, { rows: [liveRow("local:a")], remaining: 0 }],
    ]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();
    expect(threadReadRefs()).toEqual(new Set(["local:a", "local:b"]));

    world.pages = onePage([liveRow("local:b")]);
    invalidate(2);
    await owner.settled();
    expect(threadUnsubRefs()).toEqual(["local:a"]);

    await owner.dispose();
  });

  test("a generation replacement re-discovers the roster without dropping retained demand", async () => {
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:b")], remaining: 0 }],
    ]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();
    expect(threadReadRefs()).toEqual(new Set(["local:a", "local:b"]));
    fake.calls.length = 0;

    // Reconnect: a fresh generation with a changed roster.
    world.generation = REPLACEMENT_GENERATION;
    world.revision = 1;
    world.pages = new Map([
      [0, { rows: [liveRow("local:a")], remaining: 1 }],
      [1, { rows: [liveRow("local:c")], remaining: 0 }],
    ]);
    fake.emitStateChange("reconnecting");
    fake.emitReady({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v6",
      sourceId: "fake",
      features: {} as never,
      navigation: { version: 1, generationId: REPLACEMENT_GENERATION, sequence: 0, readVersions: [3] },
    });
    await owner.settled();

    expect(threadReadRefs().has("local:c")).toBe(true);
    expect(threadReadRefs().has("local:a")).toBe(true);
    expect(threadUnsubRefs()).toEqual(["local:b"]);

    await owner.dispose();
  });

  test("a reappearance during a held lease release cancels retirement", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    // Open the cache connection: a background model's release only drains a
    // cache write while the adapter is open.
    await cache.count();
    // Give every read a recorded turn so the resident model's release drains a
    // real versioned cache write (an unversioned model skips the write gate).
    fake.on("thread/read", (params) =>
      versionedRead(params.ref ?? "local:a", (params as { requestGeneration?: number }).requestGeneration ?? 1),
    );
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();
    expect(threadReadRefs()).toEqual(new Set(["local:a"]));
    // A resident model makes release drain a final cache write we can hold.
    expect(backgroundTranscriptSnapshotForTests().refs.get("local:a")?.resident).toBe(true);
    expect(threadsStore.getState().threads.get("local:a")?.history?.incarnation).toBeDefined();

    let settleWrite: ((outcome: SessionCacheWriteOutcome) => void) | undefined;
    const heldWrite = new Promise<SessionCacheWriteOutcome>((resolve) => {
      settleWrite = resolve;
    });
    const putSpy = vi.spyOn(cache, "put").mockReturnValue(heldWrite);
    try {
      // a leaves Live; the coherent sweep starts a's final release, which
      // blocks on the held cache write.
      world.pages = onePage([liveRow("local:b")]);
      invalidate(2);
      await awaitObserved(() => threadReadRefs().has("local:b"), "b background read");
      expect(putSpy).toHaveBeenCalled();
      expect(threadUnsubRefs()).toEqual([]);

      // a reappears while the release is still in flight: a fresh coherent
      // sweep must re-claim it before the held write settles.
      fake.calls.length = 0;
      world.pages = onePage([liveRow("local:a"), liveRow("local:b")]);
      invalidate(3);
      // The reappearance sweep must observe a back in the roster before the
      // held write settles. The model is still resident, so re-claiming adopts
      // it without a second read.
      await awaitObserved(() => livePageHas("local:a"), "reappearance roster");
      expect(livePageHas("local:a")).toBe(true);
    } finally {
      settleWrite?.({ outcome: "written" });
      putSpy.mockRestore();
    }

    await owner.settled();
    // The old release observed the re-claimed owner generation and dropped
    // nothing: no unsubscribe, the requested subscription survived, and the
    // re-claimed model still holds its literal content exactly once.
    expect(threadUnsubRefs()).toEqual([]);
    const survived = threadsStore.getState().threads.get("local:a");
    expect(survived).toBeDefined();
    expect(JSON.stringify(survived?.history)).toContain("recorded");
    await owner.dispose();
  });

  test("a reappearance after a release re-acquires the retired ref", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();
    expect(threadReadRefs()).toEqual(new Set(["local:a"]));

    // a leaves Live: the coherent sweep retires its lease.
    world.pages = onePage([liveRow("local:b")]);
    invalidate(2);
    await owner.settled();
    expect(threadUnsubRefs()).toEqual(["local:a"]);

    // a reappears: the owner must re-claim it with a fresh background read
    // rather than leave it retired. (Mid-release cancellation of an
    // already-started final release is the thread lease's own
    // ownership-generation contract.)
    fake.calls.length = 0;
    world.pages = onePage([liveRow("local:a"), liveRow("local:b")]);
    invalidate(3);
    await owner.settled();
    expect(threadReadRefs().has("local:a")).toBe(true);
    expect(threadUnsubRefs()).toEqual([]);

    await owner.dispose();
  });

  test("an unsupported navigation capability owns nothing and settles", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake(2);
    expect(navigationStore.getState().mode).toBe("error");

    const owner = startBackgroundTranscripts(navigationStore);
    await owner.settled();

    const sectionReads = fake.calls.filter(
      (call) =>
        call.method === "evener/navigation/read" && (call.params as NavigationReadParams).resource === "section",
    );
    expect(sectionReads).toEqual([]);
    expect(threadReadRefs()).toEqual(new Set());
    await owner.dispose();
  });

  // --- review-fix regression tests (I1/I2/I3) ---------------------------

  test("a superseded empty sweep does not retire a ref a newer held revision still lists", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();

    // Call-through wrapper around the real background page lease: a real rev3
    // invalidation lands after the rev2 empty read resolves but before that
    // stale page returns to the owner, and the rev3 transport read is held.
    let armed = false;
    let injected = false;
    let signalPostInjectionLoad: (() => void) | null = null;
    const postInjectionLoadEntered = new Promise<void>((resolve) => {
      signalPostInjectionLoad = resolve;
    });
    const originalAcquire = navigationStore.acquireBackgroundLivePages;
    const realAcquire = originalAcquire.bind(navigationStore);
    const patched = navigationStore as unknown as { acquireBackgroundLivePages: () => BackgroundLivePages };
    patched.acquireBackgroundLivePages = () => {
      const real = realAcquire();
      return {
        load: async (offset, limit) => {
          if (injected) signalPostInjectionLoad?.();
          const page = await real.load(offset, limit);
          if (armed && !injected && offset === 0) {
            injected = true;
            world.pages = onePage([liveRow("local:a")]);
            holdNextPage(0);
            invalidate(3);
          }
          return page;
        },
        refresh: (offset, limit) => real.refresh(offset, limit),
        release: () => real.release(),
      };
    };

    const owner = startBackgroundTranscripts(navigationStore);
    try {
      await owner.settled();
      expect(threadReadRefs()).toEqual(new Set(["local:a"]));

      world.pages = onePage([]);
      armed = true;
      invalidate(2);

      // Boundary: the held rev3 transport read is in flight, and the owner's
      // post-injection sweep has begun (so the stale rev2 sweep already made
      // its decision).
      await waitForHeldPage();
      await postInjectionLoadEntered;
      expect(threadUnsubRefs()).not.toContain("local:a");
    } finally {
      patched.acquireBackgroundLivePages = originalAcquire;
      releaseHeldPage();
      await owner.dispose();
    }
  });

  test("control: a truly coherent empty terminal sweep retires a ref normally", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    try {
      await owner.settled();
      expect(threadReadRefs()).toEqual(new Set(["local:a"]));

      world.pages = onePage([]);
      invalidate(2);
      await owner.settled();

      expect(threadUnsubRefs()).toEqual(["local:a"]);
    } finally {
      await owner.dispose();
    }
  });

  test("a successful same-invalidation page recovery restarts discovery", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    try {
      await owner.settled();
      expect(threadReadRefs()).toEqual(new Set(["local:a"]));

      // rev2 invalidates live while the page fails: incomplete, retain a. The
      // invalidation sequence advances once.
      world.pages = failPage();
      invalidate(2);
      await owner.settled();
      expect(threadUnsubRefs()).toEqual([]);

      // Fault removed. A foreground read republishes a fresh page under the
      // SAME invalidation (no new sequence) carrying a new Live ref.
      world.pages = onePage([liveRow("local:a"), liveRow("remote:recovered-idle", { host_id: "remote" })]);
      await navigationStore.getState().loadSection("live", 0);
      expect(navigationStore.getState().lastSequence).toBe(1);
      expect(livePageHas("remote:recovered-idle")).toBe(true);

      await owner.settled();
      expect(threadReadRefs().has("remote:recovered-idle")).toBe(true);
      expect(threadUnsubRefs()).toEqual([]);
    } finally {
      await owner.dispose();
    }
  });

  test("control: a fresh invalidation after the recovery also discovers the new ref", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    try {
      await owner.settled();

      world.pages = failPage();
      invalidate(2);
      await owner.settled();
      expect(threadUnsubRefs()).toEqual([]);

      world.pages = onePage([liveRow("local:a"), liveRow("remote:recovered-idle", { host_id: "remote" })]);
      await navigationStore.getState().loadSection("live", 0);
      expect(livePageHas("remote:recovered-idle")).toBe(true);

      invalidate(3);
      await owner.settled();
      expect(threadReadRefs().has("remote:recovered-idle")).toBe(true);
    } finally {
      await owner.dispose();
    }
  });

  test("a truncated terminal page cannot prove the roster complete", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    try {
      await owner.settled();
      expect(threadReadRefs()).toEqual(new Set(["local:a"]));

      // Empty, no remaining data, yet still claims truncation: accepted on the
      // wire, but not a trustworthy terminal proof.
      world.pages = new Map([[0, { rows: [], remaining: 0, truncated: true }]]);
      invalidate(2);
      await owner.settled();

      expect(threadUnsubRefs()).not.toContain("local:a");
    } finally {
      await owner.dispose();
    }
  });

  test("control: a genuinely terminal empty page retires a ref normally", async () => {
    world.pages = onePage([liveRow("local:a")]);
    fake = connectFake();
    const owner = startBackgroundTranscripts(navigationStore);
    try {
      await owner.settled();
      expect(threadReadRefs()).toEqual(new Set(["local:a"]));

      world.pages = new Map([[0, { rows: [], remaining: 0, truncated: false }]]);
      invalidate(2);
      await owner.settled();

      expect(threadUnsubRefs()).toEqual(["local:a"]);
    } finally {
      await owner.dispose();
    }
  });
  test("bounded backoff keeps retrying past the attempt cap and recovers a later Live ref", async () => {
    vi.useFakeTimers();
    try {
      const RETRY_CAP_MS = 5_000;
      world.pages = onePage([liveRow("local:a")]);
      fake = connectFake();
      const owner = startBackgroundTranscripts(navigationStore);
      await owner.settled();
      expect(threadReadRefs()).toEqual(new Set(["local:a"]));

      // The terminal page keeps failing. Existing owners are retained, and
      // settled() must not wait on the future retry timer.
      world.pages = failPage();
      invalidate(2);
      await owner.settled();
      expect(threadUnsubRefs()).toEqual([]);
      expect(threadReadRefs().has("local:a")).toBe(true);

      // Persist the fault well past six backoff attempts: the owner must keep
      // pacing retries at the capped cadence (not strand discovery, not spin).
      const readsAtFault = sectionReadCount();
      await vi.advanceTimersByTimeAsync(RETRY_CAP_MS * 10);
      const retriesDuringFault = sectionReadCount() - readsAtFault;
      expect(retriesDuringFault).toBeGreaterThanOrEqual(7);
      expect(retriesDuringFault).toBeLessThanOrEqual(20);
      expect(threadUnsubRefs()).toEqual([]);

      // The host recovers with no external loadSection and no new invalidation:
      // the next paced retry must acquire the newly Live ref.
      world.pages = onePage([liveRow("local:a"), liveRow("remote:recovered-idle", { host_id: "remote" })]);
      await vi.advanceTimersByTimeAsync(RETRY_CAP_MS);
      await owner.settled();
      expect(threadReadRefs().has("remote:recovered-idle")).toBe(true);
      expect(threadUnsubRefs()).toEqual([]);

      // dispose cancels the pending retry callback: no later demand.
      const disposed = owner.dispose();
      await vi.advanceTimersByTimeAsync(RETRY_CAP_MS);
      await disposed;
      const readsAtDispose = sectionReadCount();
      await vi.advanceTimersByTimeAsync(RETRY_CAP_MS * 3);
      expect(sectionReadCount()).toBe(readsAtDispose);
      expect(threadReadRefs().has("remote:recovered-idle")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  // --- Task7c: paced, useful incomplete Live discovery --------------------

  test("an incomplete sweep does not spin on its own fresh hidden page, and a later coherent shrink retires it", async () => {
    vi.useFakeTimers();
    try {
      world.pages = onePage([liveRow("local:a")]);
      fake = connectFake();
      const owner = startBackgroundTranscripts(navigationStore);
      try {
        await owner.settled();
        expect(threadReadRefs()).toEqual(new Set(["local:a"]));

        // rev2: page0 advances, a fresh hidden page1 (no foreground demand)
        // succeeds, page2 fails. The sweep's own publication/retirement of
        // page1 must not schedule an immediate re-sweep.
        fake.calls.length = 0;
        world.pages = new Map<number, PageSpec | "fail">([
          [0, { rows: [liveRow("local:a")], remaining: 2 }],
          [1, { rows: [liveRow("local:b")], remaining: 1 }],
          [2, "fail"],
        ]);
        // Bounded exit: either the third repeated page1 read (the defect's
        // spin) or the owner settling without one. Never drain unboundedly.
        holdPageAt(1, 3);
        invalidate(2);
        let spun = false;
        const heldBoundary = waitForHeldPage().then(() => {
          spun = true;
        });
        try {
          await Promise.race([heldBoundary, owner.settled()]);
          // The contract is "no self-sustaining loop at zero elapsed time", not
          // an exact read count: the owner must settle without reaching the
          // third immediate page-1 read.
          expect(spun).toBe(false);
          expect(pageReadCount(1)).toBeLessThan(3);
          // A fresh addition from the partial sweep is still permitted.
          expect(threadReadRefs().has("local:b")).toBe(true);
          expect(threadUnsubRefs()).toEqual([]);
        } finally {
          releaseHeldPage();
        }

        // A later coherent complete sweep shrinks the roster: the freshly added
        // hidden ref b is retired only under a current coherent terminal proof.
        world.pages = onePage([liveRow("local:a")]);
        invalidate(3);
        await owner.settled();
        // Only the freshly discovered hidden ref retires; the retained ref
        // never does.
        expect(threadUnsubRefs()).toEqual(["local:b"]);
      } finally {
        await disposeOwner(owner);
      }
    } finally {
      vi.useRealTimers();
    }
  });

  test("control: a page-zero failure with no fresh hidden page stays quiet", async () => {
    vi.useFakeTimers();
    try {
      world.pages = onePage([liveRow("local:a")]);
      fake = connectFake();
      const owner = startBackgroundTranscripts(navigationStore);
      try {
        await owner.settled();
        fake.calls.length = 0;
        world.pages = failPage();
        invalidate(2);
        await owner.settled();
        expect(pageReadCount(0)).toBe(1);
        expect(pageReadCount(1)).toBe(0);
        expect(threadUnsubRefs()).toEqual([]);
      } finally {
        await disposeOwner(owner);
      }
    } finally {
      vi.useRealTimers();
    }
  });

  test("a paced retry over an accepted truncated page reaches recovered host authority", async () => {
    vi.useFakeTimers();
    try {
      world.pages = onePage([liveRow("local:a")]);
      fake = connectFake();
      const owner = startBackgroundTranscripts(navigationStore);
      try {
        await owner.settled();
        expect(threadReadRefs()).toEqual(new Set(["local:a"]));

        // rev2 publishes an accepted truncated empty page: the ref is retained
        // and a retry is paced, but the retry must still reach host authority.
        world.pages = new Map([[0, { rows: [], remaining: 0, truncated: true }]]);
        invalidate(2);
        await owner.settled();
        expect(threadUnsubRefs()).toEqual([]);
        expect(threadReadRefs().has("local:a")).toBe(true);

        // The host recovers with no notification and no external read.
        const readsBefore = sectionReadCount();
        world.pages = onePage([liveRow("local:a"), liveRow("remote:recovered", { host_id: "remote" })]);
        await vi.advanceTimersByTimeAsync(8_000);
        await owner.settled();

        expect(sectionReadCount()).toBeGreaterThan(readsBefore);
        expect(threadReadRefs().has("remote:recovered")).toBe(true);
        expect(threadUnsubRefs()).toEqual([]);
      } finally {
        await disposeOwner(owner);
      }
    } finally {
      vi.useRealTimers();
    }
  });

  test("control: an unchanged truncated page retains its ref with bounded paced reads", async () => {
    vi.useFakeTimers();
    try {
      world.pages = onePage([liveRow("local:a")]);
      fake = connectFake();
      const owner = startBackgroundTranscripts(navigationStore);
      try {
        await owner.settled();
        world.pages = new Map([[0, { rows: [], remaining: 0, truncated: true }]]);
        invalidate(2);
        await owner.settled();
        expect(threadUnsubRefs()).toEqual([]);

        const readsBefore = sectionReadCount();
        await vi.advanceTimersByTimeAsync(8_000);
        await owner.settled();

        expect(threadUnsubRefs()).toEqual([]);
        expect(threadReadRefs().has("local:a")).toBe(true);
        const readsAfter = sectionReadCount();
        expect(readsAfter - readsBefore).toBeGreaterThanOrEqual(0);
        expect(readsAfter - readsBefore).toBeLessThanOrEqual(16);
      } finally {
        await disposeOwner(owner);
      }
    } finally {
      vi.useRealTimers();
    }
  });

  test("control: dispose cancels a retry pending over an incomplete page", async () => {
    vi.useFakeTimers();
    try {
      world.pages = onePage([liveRow("local:a")]);
      fake = connectFake();
      const owner = startBackgroundTranscripts(navigationStore);
      await owner.settled();
      world.pages = new Map([[0, { rows: [], remaining: 0, truncated: true }]]);
      invalidate(2);
      await owner.settled();

      const readsBefore = sectionReadCount();
      const disposed = owner.dispose();
      await vi.advanceTimersByTimeAsync(8_000);
      await disposed;
      expect(sectionReadCount()).toBe(readsBefore);
      await vi.advanceTimersByTimeAsync(8_000);
      expect(sectionReadCount()).toBe(readsBefore);
    } finally {
      vi.useRealTimers();
    }
  });
});
