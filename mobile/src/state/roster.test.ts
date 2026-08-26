// RosterStore (Zustand) tests. Covers:
// - refresh(service) loads entries and clears loading
// - refresh sets error on failure
// - refresh error retains entries (last-good) plus sets error
// - setSearch filters locally by title and project
// - reset clears all state
// - grouping by attention: needsYou, running, recent
// - hasMore stored from the service result
// - event refresh: two tree/attention signals coalesce into one refresh
// - sessionsVisible === false does not schedule a refresh
// - generation change rejects a late refresh result
//
// Tests use an injected scheduler with schedule(key, effect) so no timer/sleep
// is needed. The fake subscribe seam captures the notification handler.

import { beforeEach, describe, expect, it } from "vitest";
import type { AnyNotification } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { RosterEntry, RosterService } from "../services/roster";
import { createRosterStore, type RosterScheduler } from "./roster";

// --- fake roster service ----------------------------------------------------

class FakeRosterService implements RosterService {
  listCalls = 0;
  threads: RosterEntry[] = [];
  hasMore = false;
  shouldReject: Error | null = null;

  async list(): Promise<{ threads: RosterEntry[]; hasMore: boolean }> {
    this.listCalls += 1;
    if (this.shouldReject !== null) throw this.shouldReject;
    return { threads: this.threads, hasMore: this.hasMore };
  }

  async refresh(): Promise<void> {
    await this.list();
  }
}

// --- deferred service for generation tests ----------------------------------

class DeferredRosterService implements RosterService {
  listCalls = 0;
  private deferred: {
    resolve: (v: { threads: RosterEntry[]; hasMore: boolean }) => void;
    reject: (e: Error) => void;
  } | null = null;

  async list(): Promise<{ threads: RosterEntry[]; hasMore: boolean }> {
    this.listCalls += 1;
    return new Promise((resolve, reject) => {
      this.deferred = { resolve, reject };
    });
  }

  async refresh(): Promise<void> {
    await this.list();
  }

  resolve(value: { threads: RosterEntry[]; hasMore: boolean }): void {
    this.deferred?.resolve(value);
    this.deferred = null;
  }

  reject(err: Error): void {
    this.deferred?.reject(err);
    this.deferred = null;
  }
}

// --- fake scheduler (coalesces by key) --------------------------------------

class FakeScheduler implements RosterScheduler {
  readonly scheduleCalls: { key: string; effect: () => void }[] = [];
  private readonly effects = new Map<string, () => void>();

  schedule(key: string, effect: () => void): void {
    this.scheduleCalls.push({ key, effect });
    this.effects.set(key, effect);
  }

  flush(key: string): void {
    const effect = this.effects.get(key);
    if (effect) {
      this.effects.delete(key);
      effect();
    }
  }
}

// --- helpers ----------------------------------------------------------------

function makeEntry(over: Partial<RosterEntry> = {}): RosterEntry {
  return {
    ref: "ref-1",
    title: "Test Session",
    project: "/tmp/project",
    status: "idle",
    updatedAt: 1_000_000,
    attention: "recent",
    ...over,
  };
}

function treeChanged(): AnyNotification {
  return { method: "evener/tree/changed", params: {} } as AnyNotification;
}

function attentionChanged(): AnyNotification {
  return {
    method: "evener/attention/changed",
    params: { changed: [], summary: { needsYou: 0, error: 0, working: 0 } },
  } as AnyNotification;
}

function statusChanged(): AnyNotification {
  return {
    method: "thread/status/changed",
    params: {
      threadId: "t1",
      ref: "ref-1",
      status: { type: "active" },
    },
  } as AnyNotification;
}

// --- tests ------------------------------------------------------------------

describe("RosterStore", () => {
  let service: FakeRosterService;

  beforeEach(() => {
    service = new FakeRosterService();
  });

  it("refresh(service) loads entries and sets loading then clears it", async () => {
    service.threads = [makeEntry({ ref: "r1", title: "Session 1" })];
    const store = createRosterStore();

    expect(store.getState().loading).toBe(false);
    expect(store.getState().entries).toEqual([]);

    const promise = store.getState().refresh(service);
    expect(store.getState().loading).toBe(true);

    await promise;

    expect(store.getState().loading).toBe(false);
    expect(store.getState().entries).toHaveLength(1);
    expect(store.getState().entries[0]?.ref).toBe("r1");
  });

  it("refresh(service) stores hasMore from the result", async () => {
    service.threads = [makeEntry()];
    service.hasMore = true;
    const store = createRosterStore();

    await store.getState().refresh(service);

    expect(store.getState().hasMore).toBe(true);
  });

  it("refresh(service) stores hasMore=false when service returns false", async () => {
    service.threads = [makeEntry()];
    service.hasMore = false;
    const store = createRosterStore();

    await store.getState().refresh(service);

    expect(store.getState().hasMore).toBe(false);
  });

  it("refresh(service) sets error on failure", async () => {
    service.shouldReject = new Error("network error");
    const store = createRosterStore();

    await store.getState().refresh(service);

    expect(store.getState().loading).toBe(false);
    expect(store.getState().error).toBe("network error");
    expect(store.getState().entries).toEqual([]);
  });

  it("refresh(service) retains entries (last-good) and sets error on failure", async () => {
    service.threads = [makeEntry({ ref: "r1", title: "Loaded" })];
    const store = createRosterStore();

    await store.getState().refresh(service);
    expect(store.getState().entries).toHaveLength(1);
    expect(store.getState().error).toBeNull();

    service.shouldReject = new Error("network error");
    await store.getState().refresh(service);

    expect(store.getState().entries).toHaveLength(1);
    expect(store.getState().entries[0]?.ref).toBe("r1");
    expect(store.getState().error).toBe("network error");
    expect(store.getState().loading).toBe(false);
  });

  it("refresh(service) clears a previous error on success", async () => {
    service.shouldReject = new Error("network error");
    const store = createRosterStore();
    await store.getState().refresh(service);
    expect(store.getState().error).not.toBeNull();

    service.shouldReject = null;
    service.threads = [makeEntry()];
    await store.getState().refresh(service);

    expect(store.getState().error).toBeNull();
    expect(store.getState().entries).toHaveLength(1);
  });

  it("setSearch filters entries locally by title", async () => {
    service.threads = [
      makeEntry({ ref: "r1", title: "Fix billing bug", project: "/a" }),
      makeEntry({ ref: "r2", title: "Refactor auth", project: "/b" }),
    ];
    const store = createRosterStore();
    await store.getState().refresh(service);
    expect(store.getState().entries).toHaveLength(2);

    store.getState().setSearch("billing");

    const visible = store.getState().visibleEntries;
    expect(visible).toHaveLength(1);
    expect(visible[0]?.ref).toBe("r1");
  });

  it("setSearch filters entries locally by project", async () => {
    service.threads = [
      makeEntry({ ref: "r1", title: "Fix billing bug", project: "/home/auth" }),
      makeEntry({
        ref: "r2",
        title: "Refactor auth",
        project: "/home/billing",
      }),
    ];
    const store = createRosterStore();
    await store.getState().refresh(service);

    store.getState().setSearch("billing");

    const visible = store.getState().visibleEntries;
    expect(visible).toHaveLength(2);
  });

  it("setSearch is case-insensitive", async () => {
    service.threads = [
      makeEntry({ ref: "r1", title: "Fix Billing Bug", project: "/a" }),
    ];
    const store = createRosterStore();
    await store.getState().refresh(service);

    store.getState().setSearch("billing");
    expect(store.getState().visibleEntries).toHaveLength(1);
  });

  it("setSearch with empty string shows all entries", async () => {
    service.threads = [
      makeEntry({ ref: "r1", title: "Alpha", project: "/a" }),
      makeEntry({ ref: "r2", title: "Beta", project: "/b" }),
    ];
    const store = createRosterStore();
    await store.getState().refresh(service);

    store.getState().setSearch("alpha");
    expect(store.getState().visibleEntries).toHaveLength(1);

    store.getState().setSearch("");
    expect(store.getState().visibleEntries).toHaveLength(2);
  });

  it("groupedEntries groups by attention", async () => {
    service.threads = [
      makeEntry({ ref: "needs1", title: "Needs 1", attention: "needsYou" }),
      makeEntry({ ref: "run1", title: "Running 1", attention: "running" }),
      makeEntry({ ref: "recent1", title: "Recent 1", attention: "recent" }),
      makeEntry({ ref: "needs2", title: "Needs 2", attention: "needsYou" }),
    ];
    const store = createRosterStore();
    await store.getState().refresh(service);

    const grouped = store.getState().groupedEntries;
    expect(grouped.needsYou).toHaveLength(2);
    expect(grouped.running).toHaveLength(1);
    expect(grouped.recent).toHaveLength(1);
    expect(grouped.needsYou[0]?.ref).toBe("needs1");
    expect(grouped.needsYou[1]?.ref).toBe("needs2");
  });

  it("groupedEntries respects search filter", async () => {
    service.threads = [
      makeEntry({ ref: "needs1", title: "Fix billing", attention: "needsYou" }),
      makeEntry({
        ref: "run1",
        title: "Run billing test",
        attention: "running",
      }),
      makeEntry({ ref: "recent1", title: "Other", attention: "recent" }),
    ];
    const store = createRosterStore();
    await store.getState().refresh(service);

    store.getState().setSearch("billing");
    const grouped = store.getState().groupedEntries;
    expect(grouped.needsYou).toHaveLength(1);
    expect(grouped.running).toHaveLength(1);
    expect(grouped.recent).toHaveLength(0);
  });

  it("reset clears all state", async () => {
    service.threads = [makeEntry()];
    const store = createRosterStore();
    await store.getState().refresh(service);
    store.getState().setSearch("test");

    store.getState().reset();

    expect(store.getState().entries).toEqual([]);
    expect(store.getState().loading).toBe(false);
    expect(store.getState().error).toBeNull();
    expect(store.getState().searchTerm).toBe("");
    expect(store.getState().hasMore).toBe(false);
  });

  it("pull-to-refresh triggers another refresh(service)", async () => {
    service.threads = [makeEntry({ ref: "r1" })];
    const store = createRosterStore();
    await store.getState().refresh(service);
    expect(service.listCalls).toBe(1);

    service.threads = [makeEntry({ ref: "r2" })];
    await store.getState().refresh(service);

    expect(service.listCalls).toBe(2);
    expect(store.getState().entries[0]?.ref).toBe("r2");
  });
});

// --- event refresh tests ----------------------------------------------------

describe("RosterStore — event refresh", () => {
  it("coalesces two tree/attention signals into one refresh", async () => {
    const scheduler = new FakeScheduler();
    let notify: (n: AnyNotification) => void = () => {};
    const subscribe = (h: (n: AnyNotification) => void): (() => void) => {
      notify = h;
      return () => {};
    };

    const service = new FakeRosterService();
    service.threads = [makeEntry({ ref: "r1" })];

    const store = createRosterStore({ scheduler, subscribe });
    store.getState().setSessionsVisible(true);
    await store.getState().refresh(service);
    expect(service.listCalls).toBe(1);

    notify?.(treeChanged());
    notify?.(attentionChanged());

    expect(scheduler.scheduleCalls).toHaveLength(2);
    const key0 = scheduler.scheduleCalls[0]?.key;
    const key1 = scheduler.scheduleCalls[1]?.key;
    expect(key0).toBe(key1);

    if (key0 !== undefined) scheduler.flush(key0);
    await Promise.resolve();

    expect(service.listCalls).toBe(2);
  });

  it("does not schedule a refresh when sessionsVisible is false", () => {
    const scheduler = new FakeScheduler();
    let notify: (n: AnyNotification) => void = () => {};
    const subscribe = (h: (n: AnyNotification) => void): (() => void) => {
      notify = h;
      return () => {};
    };

    const store = createRosterStore({ scheduler, subscribe });
    // sessionsVisible defaults to false
    expect(store.getState().sessionsVisible).toBe(false);

    notify?.(treeChanged());

    expect(scheduler.scheduleCalls).toHaveLength(0);
  });

  it("schedules a refresh for thread/status/changed", async () => {
    const scheduler = new FakeScheduler();
    let notify: (n: AnyNotification) => void = () => {};
    const subscribe = (h: (n: AnyNotification) => void): (() => void) => {
      notify = h;
      return () => {};
    };

    const service = new FakeRosterService();
    const store = createRosterStore({ scheduler, subscribe });
    store.getState().setSessionsVisible(true);
    await store.getState().refresh(service);

    notify?.(statusChanged());

    expect(scheduler.scheduleCalls).toHaveLength(1);
  });

  it("uses a generation-scoped scheduler key", async () => {
    const scheduler = new FakeScheduler();
    let notify: (n: AnyNotification) => void = () => {};
    const subscribe = (h: (n: AnyNotification) => void): (() => void) => {
      notify = h;
      return () => {};
    };

    const service = new FakeRosterService();
    const store = createRosterStore({ scheduler, subscribe });
    store.getState().setSessionsVisible(true);
    await store.getState().refresh(service);

    notify?.(treeChanged());
    const keyBefore = scheduler.scheduleCalls[0]?.key;
    expect(keyBefore).toContain(String(store.getState().generation));
  });

  it("rejects a late refresh result after generation change", async () => {
    const service = new DeferredRosterService();
    const store = createRosterStore();

    expect(store.getState().entries).toEqual([]);
    expect(store.getState().generation).toBe(0);

    const promise = store.getState().refresh(service);
    expect(store.getState().loading).toBe(true);

    store.getState().bumpGeneration();
    expect(store.getState().generation).toBe(1);

    service.resolve({
      threads: [makeEntry({ ref: "late" })],
      hasMore: false,
    });
    await promise;

    expect(store.getState().entries).toEqual([]);
    expect(store.getState().loading).toBe(false);
  });

  it("accepts a refresh result when generation is unchanged", async () => {
    const service = new DeferredRosterService();
    const store = createRosterStore();

    const promise = store.getState().refresh(service);
    expect(store.getState().loading).toBe(true);

    service.resolve({
      threads: [makeEntry({ ref: "ok" })],
      hasMore: false,
    });
    await promise;

    expect(store.getState().entries).toHaveLength(1);
    expect(store.getState().entries[0]?.ref).toBe("ok");
    expect(store.getState().loading).toBe(false);
  });
});
