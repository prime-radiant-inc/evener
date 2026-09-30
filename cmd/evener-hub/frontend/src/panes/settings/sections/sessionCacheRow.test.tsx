// The settings storage row for the session cache (web session-history cache
// plan, Task 11; spec, "The clear-cached-sessions setting"): the row's four
// states — empty, cached, cleared, unavailable — and the per-render count that
// ends a committed clear's badge.
//
// Note: this codebase has no jest-dom matcher setup (vite.config.ts's own
// setupFiles is ./src/testSetup.ts), so assertions are house-style —
// findByText/getByText for presence (they throw when absent), queryByText +
// toBeNull for absence, and the button's disabled DOM property — matching
// hubResidents.test.tsx's isDisabled convention in this same folder.
//
// Test bed: this file re-creates locally what threads.sessionCache.test.ts
// keeps in its own bed (importing another test file is not allowed):
// fake-indexeddb's global factory, a seeded SessionCacheIndexedDB installed
// on the singleton seam, a wedged-open adapter for "unavailable", and a
// deleteDatabase between tests so no record one test seeded survives.
import "fake-indexeddb/auto";
import type { CachedSessionRecord } from "@evener/appwire-client";
import { act, cleanup, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearProjectionWorkForTests } from "../../../stores/projectionWork";
import { SessionCacheIndexedDB } from "../../../stores/sessionCacheIndexedDB";
import { neverSettlingRequest } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setSessionCacheAdapterForTests } from "../../../stores/threads";
import { resetSessionCacheRowStoreForTests, SessionCacheRow } from "./sessionCacheRow";

// Checks the HTML disabled property — no jest-dom in this project.
// Mirrors hubResidents.test.tsx's isDisabled helper.
function isDisabled(el: Element): boolean {
  return (el as HTMLButtonElement).disabled;
}

// The record literal every seed in this file agrees on —
// threads.sessionCache.test.ts's seededRecord shape, re-created locally with
// only the fields the record type requires. The row counts records store
// rows, so the history's content is irrelevant here; only its presence makes
// the row a record the adapter's decode step accepts.
function seededRecord(ref: string): CachedSessionRecord {
  return {
    ref,
    threadId: "thr_1",
    name: "cached session",
    modelProvider: "anthropic/claude-sonnet-4-5",
    model: "anthropic/claude-sonnet-4-5",
    savedAt: Date.now(),
    history: {
      bootGeneration: "1",
      epoch: 0,
      length: 0,
      appliedGeneration: 0,
      issuedGeneration: 0,
      turns: [],
    },
  };
}

// Every adapter a test installed on the singleton seam, closed in afterEach
// so the next beforeEach's deleteCacheDatabase never fires "blocked" — the
// beds discipline threads.sessionCache.test.ts itself documents.
const installedAdapters: SessionCacheIndexedDB[] = [];

// Mirrors threads.sessionCache.test.ts's deleteCacheDatabase, so no record
// one test seeded survives into the next one's count.
async function deleteCacheDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("evener-session-cache");
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error("session cache database deletion blocked")), {
      once: true,
    });
  });
}

// Installs a fresh adapter on the singleton seam — the row counts through
// it — and writes `count` records into the store the row will read. The
// seeds must land before the render: the count runs per render, so a record
// arriving after the last render would never be seen.
async function seedCacheWithRecords(count: number): Promise<void> {
  const adapter = new SessionCacheIndexedDB();
  installedAdapters.push(adapter);
  setSessionCacheAdapterForTests(adapter);
  for (let i = 0; i < count; i += 1) {
    const outcome = await adapter.put(seededRecord(`local:thr_${i}`), 0, Date.now());
    expect(outcome.outcome).toBe("written");
  }
}

// The same bed with nothing seeded: the honest empty database the row counts.
async function seedEmptyCache(): Promise<void> {
  const adapter = new SessionCacheIndexedDB();
  installedAdapters.push(adapter);
  setSessionCacheAdapterForTests(adapter);
}

// The wedged-open adapter: fake-indexeddb's IDBFactory with open() mocked to
// the tree's neverSettlingRequest — the connection-coordinator shape where
// open() returns and then no event ever arrives (threads.sessionCache.test.ts's
// neverSettlingFactory mirror).
function installWedgedCacheAdapter(): void {
  const factory = new IDBFactory();
  vi.spyOn(factory, "open").mockImplementation(() => neverSettlingRequest());
  const adapter = new SessionCacheIndexedDB({ indexedDB: factory });
  installedAdapters.push(adapter);
  setSessionCacheAdapterForTests(adapter);
}

// The next record this tab writes: a direct put through a
// SessionCacheIndexedDB on the same global fake database — the row's count
// reads that database through the singleton seam. The committed Clear raised
// the durable epoch from 0 to 1, so the refill is scheduled at exactly that
// epoch — the same value the tab's own write seam captures after its
// same-tab clear; scheduling below it aborts by design.
async function writeOneRecordThroughTheSeam(): Promise<void> {
  const sibling = new SessionCacheIndexedDB();
  try {
    const outcome = await sibling.put(seededRecord("local:refill"), 1, Date.now());
    expect(outcome.outcome).toBe("written");
  } finally {
    sibling.close();
  }
}

beforeEach(async () => {
  // resetThreadsStoreForTests returns threads.ts's module state to its
  // initial values — the tab's clear-epoch view above all — and closes the
  // singleton adapter. Without it, this file's earlier tests' committed
  // clears would leave the refill's scheduled epoch dependent on test order.
  resetThreadsStoreForTests();
  resetSessionCacheRowStoreForTests();
  await deleteCacheDatabase();
});

afterEach(() => {
  cleanup();
  setSessionCacheAdapterForTests(undefined);
  for (const adapter of installedAdapters.splice(0)) adapter.close();
  vi.restoreAllMocks();
});

describe("SessionCacheRow", () => {
  it("renders empty truthfully when the store holds nothing", { timeout: 3_000 }, async () => {
    await seedEmptyCache();
    render(<SessionCacheRow />);
    expect(await screen.findByText("empty")).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: /clear/i }))).toBe(true);
  });

  it("renders cached when records exist, and the Clear action works", { timeout: 3_000 }, async () => {
    await seedCacheWithRecords(2);
    render(<SessionCacheRow />);
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
      render(<SessionCacheRow />);
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

  it("cleared exits on the next re-render: the per-render count recomputes the state", { timeout: 3_000 }, async () => {
    await seedCacheWithRecords(1);
    const { rerender } = render(<SessionCacheRow />);
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
    await act(async () => {
      rerender(<SessionCacheRow />); // the settings pane rendering the row again
    });
    expect(await screen.findByText("cached")).toBeTruthy(); // the badge yielded
  });
});
