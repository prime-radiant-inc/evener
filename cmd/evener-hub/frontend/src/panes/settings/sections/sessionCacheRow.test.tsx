// Settings state observations run through the ordinary entry cache boundary.
import "fake-indexeddb/auto";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearProjectionWorkForTests, settleProjectionWorkForTests } from "../../../stores/projectionWork";
import { entryHistoryKey } from "../../../stores/sessionCacheSchema";
import { SessionEntryCache } from "../../../stores/sessionEntryCache";
import {
  deleteIndexedDatabase,
  holdNextWriteTransaction,
  neverSettlingFactory,
} from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setProductionEntryCacheForTests } from "../../../stores/threads";
import { resetSessionCacheRowStoreForTests, SessionCacheStatus, sessionCacheRowStore } from "./sessionCacheRow";

function isDisabled(el: Element): boolean {
  return (el as HTMLButtonElement).disabled;
}
const installedAdapters: SessionEntryCache[] = [];
function installCache(cache = new SessionEntryCache({ databaseName: "evener-session-cache" })): SessionEntryCache {
  installedAdapters.push(cache);
  setProductionEntryCacheForTests(cache);
  return cache;
}
async function seedRecord(adapter: SessionEntryCache, ref: string, clearEpoch: number): Promise<void> {
  const identity = { clientId: location.origin, hostId: "local", sessionId: ref, incarnation: "inc-1" };
  const historyKey = entryHistoryKey(identity);
  const now = Date.now();
  const outcome = await adapter.commitBatch({
    historyKey,
    clearEpoch,
    items: [],
    sessionMetadata: {
      historyKey,
      requestedRefs: [{ ref, clientId: location.origin }],
      resolvedIdentity: identity,
      savedAt: now,
      accessedAt: now,
      formatVersion: 1,
    },
  });
  expect(outcome.outcome).toBe("committed");
}
async function seedCacheWithRecords(count: number): Promise<void> {
  const cache = installCache();
  for (let i = 0; i < count; i++) await seedRecord(cache, `local:thr_${i}`, 0);
}
async function seedEmptyCache(): Promise<void> {
  installCache();
}
function installWedgedCacheAdapter(): void {
  installCache(new SessionEntryCache({ indexedDB: neverSettlingFactory(), databaseName: "evener-session-cache" }));
}
async function writeOneRecordThroughTheSeam(): Promise<void> {
  const cache = installedAdapters.at(-1);
  if (!cache) throw new Error("no entry cache installed");
  await seedRecord(cache, "local:refill", 1);
}
beforeEach(async () => {
  resetThreadsStoreForTests();
  resetSessionCacheRowStoreForTests();
  await deleteIndexedDatabase("evener-session-cache");
});
afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  for (const cache of installedAdapters.splice(0)) cache.close();
  vi.restoreAllMocks();
});

describe("SessionCacheStatus", () => {
  it("renders empty truthfully when the store holds nothing", { timeout: 3_000 }, async () => {
    await seedEmptyCache();
    render(<SessionCacheStatus />);
    expect(await screen.findByText("empty")).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: /clear/i }))).toBe(true);
  });

  it("renders cached when records exist, and the Clear action works", { timeout: 3_000 }, async () => {
    await seedCacheWithRecords(2);
    render(<SessionCacheStatus />);
    expect(await screen.findByText("cached")).toBeTruthy();
    await act(async () => {
      screen.getByRole("button", { name: /clear/i }).click();
    });
    expect(await screen.findByText("cleared")).toBeTruthy();
  });

  it("renders unavailable on a failed open, with a retry, never as empty", { timeout: 3_000 }, async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      installWedgedCacheAdapter();
      render(<SessionCacheStatus />);
      // The pre-count state is unknown, and unknown renders as unavailable
      // (spec: "never shown as empty, so the privacy remedy cannot silently
      // claim to have worked") — asserted BEFORE the watchdog advances, so
      // the row's first paint already tells the truth.
      expect(screen.getByText("unavailable")).toBeTruthy();
      expect(screen.queryByText("empty")).toBeNull();
      // The wedged open's only failure is the adapter's storage watchdog
      // (10s): fake timers reach it at once — the wedged-open tests' own
      // discipline — instead of a real 10-second wait.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_500);
      });
      expect(screen.getByText("unavailable")).toBeTruthy();
      expect(screen.queryByText("empty")).toBeNull();
      expect(screen.getByRole("button", { name: /retry/i })).toBeTruthy();
    } finally {
      clearProjectionWorkForTests(); // the re-armed count never settles: forget it
      vi.useRealTimers();
    }
  });

  // Task 4: readable content stays usable and honest about staleness. A count
  // that becomes unreadable after content was cached must not keep claiming a
  // stale "cached" state, and must never fall back to "empty" (which would
  // claim the privacy remedy's store was certainly cleared).
  it("a count that becomes unavailable after cached content reports unavailable, not stale cached or empty", {
    timeout: 3_000,
  }, async () => {
    await seedCacheWithRecords(1);
    await sessionCacheRowStore.getState().refresh();
    expect(sessionCacheRowStore.getState().status).toBe("cached");

    const adapter = installedAdapters.at(-1);
    if (adapter === undefined) throw new Error("no cache adapter installed");
    vi.spyOn(adapter, "count").mockResolvedValue(undefined);
    await sessionCacheRowStore.getState().refresh();
    expect(sessionCacheRowStore.getState().status).toBe("unavailable");
  });

  it("a mounted cleared badge yields to the next same-tab write without a forced render", {
    timeout: 3_000,
  }, async () => {
    await seedCacheWithRecords(1);
    render(<SessionCacheStatus />);
    // The row starts unavailable with Clear disabled (its pre-count
    // state), so the click must wait for the first count to land — the
    // brief's own test-2 order.
    expect(await screen.findByText("cached")).toBeTruthy();
    await act(async () => {
      screen.getByRole("button", { name: /clear/i }).click();
    });
    expect(await screen.findByText("cleared")).toBeTruthy();
    await act(async () => {
      await writeOneRecordThroughTheSeam(); // the next record this tab writes
    });
    expect(await screen.findByText("cached")).toBeTruthy(); // the badge yielded
  });

  it("a clear missing its terminal event becomes unavailable and ignores late completion", {
    timeout: 3_000,
  }, async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await seedCacheWithRecords(1);
    await sessionCacheRowStore.getState().refresh();
    const hold = holdNextWriteTransaction([
      "transcriptItems",
      "transcriptTurns",
      "transcriptCoverage",
      "transcriptMeta",
    ]);
    const clearing = sessionCacheRowStore.getState().clear();
    try {
      await hold.reached;
      await vi.advanceTimersByTimeAsync(10_001);
      expect(sessionCacheRowStore.getState().clearing).toBe(false);
      expect(sessionCacheRowStore.getState().status).toBe("unavailable");
      await clearing;
      hold.release();
      await settleProjectionWorkForTests();
      expect(sessionCacheRowStore.getState().status).toBe("unavailable");
      // Retry can now recount using a fresh connection, without late success
      // falsely claiming that the uncertain action was observed to commit.
      await sessionCacheRowStore.getState().refresh();
      expect(sessionCacheRowStore.getState().status).toBe("empty");
    } finally {
      hold.release();
      await clearing;
      vi.useRealTimers();
    }
  });

  it("the mounted row retains unavailable and Retry after an uncertain clear", { timeout: 3_000 }, async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await seedCacheWithRecords(1);
    render(<SessionCacheStatus />);
    await act(async () => {
      await sessionCacheRowStore.getState().refresh();
    });
    const hold = holdNextWriteTransaction([
      "transcriptItems",
      "transcriptTurns",
      "transcriptCoverage",
      "transcriptMeta",
    ]);
    let clearing: Promise<void> | undefined;
    try {
      await act(async () => {
        clearing = sessionCacheRowStore.getState().clear();
        await hold.reached;
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_001);
      });
      await act(async () => {
        hold.release();
        await clearing;
        await settleProjectionWorkForTests();
      });
      expect(screen.getByText("unavailable")).toBeTruthy();
      expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
      await act(async () => {
        screen.getByRole("button", { name: "Retry" }).click();
        await settleProjectionWorkForTests();
        // Entry count is the real storage read, not legacy projection work.
        await vi.waitFor(() => expect(sessionCacheRowStore.getState().status).toBe("empty"));
      });
      expect(screen.getByText("empty")).toBeTruthy();
    } finally {
      hold.release();
      await clearing;
      vi.useRealTimers();
    }
  });

  it("a reset invalidates an in-flight count, so a late answer cannot overwrite the fresh state", {
    timeout: 3_000,
  }, async () => {
    // The full-suite isolation flake, pinned deterministically: a refresh
    // whose count settles only after the NEXT test's reset must not write
    // its stale answer over the state that reset just restored. The count is
    // deferred through the seam — the adapter's own count, mocked to a
    // manually-resolved promise — so the interleaving is scheduled, not
    // hoped for.
    const adapter = new SessionEntryCache({ databaseName: "evener-session-cache" });
    installedAdapters.push(adapter);
    setProductionEntryCacheForTests(adapter);
    let releaseCount: ((count: number) => void) | undefined;
    const deferred = new Promise<number>((resolve) => {
      releaseCount = resolve;
    });
    vi.spyOn(adapter, "count").mockImplementation(() => deferred);

    const { unmount } = render(<SessionCacheStatus />);
    // The mounted row's first refresh is parked on the deferred count, so the
    // pre-count state holds.
    expect(screen.getByText("unavailable")).toBeTruthy();
    unmount(); // this test ends
    resetSessionCacheRowStoreForTests(); // the next test's beforeEach runs
    await act(async () => {
      if (releaseCount === undefined) throw new Error("count resolver was not installed");
      releaseCount(1); // the stale answer lands at last: would flip the row to "cached"
    });
    expect(sessionCacheRowStore.getState().status).toBe("unavailable"); // it wrote nothing
  });
});
