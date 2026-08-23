// RosterStore (Zustand) tests with a fake RosterService. Covers:
// - refresh(service) loads entries and clears loading
// - refresh sets error on failure
// - setSearch filters locally by title and project
// - reset clears all state
// - grouping by attention: needsYou, running, recent
// - pull-to-refresh calls refresh(service) again

import { beforeEach, describe, expect, it } from "vitest";
import type { RosterEntry, RosterService } from "../services/roster";
import { createRosterStore } from "./roster";

// --- fake roster service ----------------------------------------------------

class FakeRosterService implements RosterService {
  listCalls = 0;
  lastCursor: string | undefined = undefined;
  threads: RosterEntry[] = [];
  nextCursor: string | undefined = undefined;
  shouldReject: Error | null = null;

  async list(cursor?: string): Promise<{
    threads: RosterEntry[];
    nextCursor?: string;
  }> {
    this.listCalls += 1;
    this.lastCursor = cursor;
    if (this.shouldReject !== null) throw this.shouldReject;
    return { threads: this.threads, nextCursor: this.nextCursor };
  }

  async refresh(): Promise<void> {
    await this.list();
  }
}

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

  it("refresh(service) sets error on failure", async () => {
    service.shouldReject = new Error("network error");
    const store = createRosterStore();

    await store.getState().refresh(service);

    expect(store.getState().loading).toBe(false);
    expect(store.getState().error).toBe("network error");
    expect(store.getState().entries).toEqual([]);
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
    // Both match because both have "billing" in title or project
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
