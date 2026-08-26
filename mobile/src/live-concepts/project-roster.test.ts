// Pure projection tests for projectLiveRoster. projectLiveRoster folds a
// RosterState (entries, loading, error, searchTerm, hasMore, sessionsVisible)
// into a display-safe LiveRosterView. No DOM, no network — given the same
// input it produces the same output. The raw thread ref must NOT appear in
// the enumerable display rows; it is carried separately in refsByKey for
// operational lookup.

import { describe, expect, it } from "vitest";
import type { RosterEntry } from "../services/roster";
import type { LiveRosterRow } from "./model";
import { createRosterProjector, projectLiveRoster } from "./project-roster";

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

interface RosterStateInput {
  entries: RosterEntry[];
  loading: boolean;
  error: string | null;
  searchTerm: string;
  hasMore: boolean;
  sessionsVisible: boolean;
}

// --- tests ------------------------------------------------------------------

describe("projectLiveRoster", () => {
  it("projects needsYou rows into the needsYou group", () => {
    const state: RosterStateInput = {
      entries: [
        makeEntry({ ref: "n1", title: "Needs", attention: "needsYou" }),
      ],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.groups.find((g) => g.id === "needsYou")?.rows).toHaveLength(1);
    expect(view.groups.find((g) => g.id === "running")).toBeUndefined();
    expect(view.groups.find((g) => g.id === "recent")).toBeUndefined();
  });

  it("projects running rows into the running group", () => {
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "r1", title: "Run", attention: "running" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.groups.find((g) => g.id === "running")?.rows).toHaveLength(1);
  });

  it("projects recent rows into the recent group", () => {
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "rec1", title: "Done", attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.groups.find((g) => g.id === "recent")?.rows).toHaveLength(1);
  });

  it("filters rows by query against title", () => {
    const state: RosterStateInput = {
      entries: [
        makeEntry({ ref: "r1", title: "Fix billing", attention: "recent" }),
        makeEntry({ ref: "r2", title: "Refactor auth", attention: "recent" }),
      ],
      loading: false,
      error: null,
      searchTerm: "billing",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    const recentRows = view.groups.find((g) => g.id === "recent")?.rows ?? [];
    expect(recentRows).toHaveLength(1);
    expect(recentRows[0]?.title).toBe("Fix billing");
  });

  it("filters rows by query against project", () => {
    const state: RosterStateInput = {
      entries: [
        makeEntry({
          ref: "r1",
          title: "Fix billing",
          project: "/home/auth",
          attention: "recent",
        }),
        makeEntry({
          ref: "r2",
          title: "Refactor auth",
          project: "/home/billing",
          attention: "recent",
        }),
      ],
      loading: false,
      error: null,
      searchTerm: "billing",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    const recentRows = view.groups.find((g) => g.id === "recent")?.rows ?? [];
    expect(recentRows).toHaveLength(2);
  });

  it("omits empty groups", () => {
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "r1", title: "Run", attention: "running" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.groups.map((g) => g.id)).toEqual(["running"]);
  });

  it("maps connectedWorkCount from the entry status", () => {
    const state: RosterStateInput = {
      entries: [
        makeEntry({
          ref: "r1",
          title: "Run",
          attention: "running",
          status: "active",
        }),
      ],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    const row = view.groups.find((g) => g.id === "running")?.rows[0];
    expect(row?.connectedWorkCount).toBe(1);
  });

  it("reports status loading", () => {
    const state: RosterStateInput = {
      entries: [],
      loading: true,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.status).toBe("loading");
  });

  it("reports status error", () => {
    const state: RosterStateInput = {
      entries: [],
      loading: false,
      error: "boom",
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.status).toBe("error");
    expect(view.error).toBe("boom");
  });

  it("reports status idle when not visible and not loading", () => {
    const state: RosterStateInput = {
      entries: [],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: false,
    };
    const { view } = projectLiveRoster(state);
    expect(view.status).toBe("idle");
  });

  it("reports status ready when entries loaded and visible", () => {
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "r1" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.status).toBe("ready");
  });

  it("reports empty state when ready with no rows", () => {
    const state: RosterStateInput = {
      entries: [],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.status).toBe("ready");
    expect(view.groups.every((g) => g.rows.length === 0)).toBe(true);
  });

  it("passes hasMore through to the view", () => {
    const state: RosterStateInput = {
      entries: [makeEntry()],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: true,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.hasMore).toBe(true);
  });

  it("passes the query through to the view", () => {
    const state: RosterStateInput = {
      entries: [],
      loading: false,
      error: null,
      searchTerm: "billing",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    expect(view.query).toBe("billing");
  });

  it("carries the raw ref in refsByKey, not in the enumerable row", () => {
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "ref-abc", title: "X", attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view, refsByKey } = projectLiveRoster(state);
    const row = view.groups.find((g) => g.id === "recent")?.rows[0] as
      | LiveRosterRow
      | undefined;
    expect(row).toBeDefined();
    expect(row).not.toHaveProperty("ref");
    expect(row).not.toHaveProperty("threadRef");
    const enumerableKeys = Object.keys(row as object);
    expect(enumerableKeys).not.toContain("ref");
    expect(refsByKey.get(row?.key ?? "")).toBe("ref-abc");
  });

  it("uses stable private keys across projections", () => {
    const entry = makeEntry({
      ref: "ref-1",
      title: "Stable",
      attention: "recent",
    });
    const state1: RosterStateInput = {
      entries: [entry],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const state2: RosterStateInput = {
      ...state1,
      searchTerm: "sta",
    };
    const { view: v1 } = projectLiveRoster(state1);
    const { view: v2 } = projectLiveRoster(state2);
    const key1 = v1.groups.find((g) => g.id === "recent")?.rows[0]?.key;
    const key2 = v2.groups.find((g) => g.id === "recent")?.rows[0]?.key;
    expect(key1).toBe(key2);
  });

  it("maps row display fields from the entry", () => {
    const state: RosterStateInput = {
      entries: [
        makeEntry({
          ref: "ref-x",
          title: "My Session",
          project: "/home/work",
          updatedAt: 2_000_000,
          attention: "needsYou",
          askPending: true,
        }),
      ],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projectLiveRoster(state);
    const row = view.groups.find((g) => g.id === "needsYou")?.rows[0];
    expect(row?.title).toBe("My Session");
    expect(row?.project).toBe("/home/work");
    expect(row?.tone).toBe("attention");
  });

  it("returns a ProjectedRoster with view and refsByKey", () => {
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "r1", attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const result = projectLiveRoster(state);
    expect(result.view).toBeDefined();
    expect(result.refsByKey).toBeInstanceOf(Map);
  });
});

// --- opaque key registry tests (fix round 1) ---------------------------------
//
// The raw thread ref must not appear in the row key or anywhere in the
// serialized view. Keys must be opaque, stable, and unique per ref — but
// not reversible. A createRosterProjector() closure owns the private key
// registry so the same ref maps to the same opaque key across projections.

describe("createRosterProjector — opaque keys", () => {
  it("row key does not contain the raw ref", () => {
    const projector = createRosterProjector();
    const state: RosterStateInput = {
      entries: [
        makeEntry({ ref: "ref-super-secret-123", attention: "recent" }),
      ],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projector.project(state);
    const row = view.groups.find((g) => g.id === "recent")?.rows[0];
    expect(row?.key).not.toContain("ref-super-secret-123");
  });

  it("row key does not contain a reversible prefix like row:", () => {
    const projector = createRosterProjector();
    const state: RosterStateInput = {
      entries: [makeEntry({ ref: "ref-abc", attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projector.project(state);
    const row = view.groups.find((g) => g.id === "recent")?.rows[0];
    expect(row?.key).not.toContain("row:");
    expect(row?.key).not.toContain("ref-abc");
  });

  it("same ref gets same opaque key across projections", () => {
    const projector = createRosterProjector();
    const entry = makeEntry({
      ref: "ref-stable",
      title: "Stable",
      attention: "recent",
    });
    const state1: RosterStateInput = {
      entries: [entry],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const state2: RosterStateInput = { ...state1, searchTerm: "sta" };

    const { view: v1 } = projector.project(state1);
    const { view: v2 } = projector.project(state2);
    const key1 = v1.groups.find((g) => g.id === "recent")?.rows[0]?.key;
    const key2 = v2.groups.find((g) => g.id === "recent")?.rows[0]?.key;
    expect(key1).toBeDefined();
    expect(key2).toBeDefined();
    expect(key1).toBe(key2);
  });

  it("different refs get different opaque keys", () => {
    const projector = createRosterProjector();
    const state: RosterStateInput = {
      entries: [
        makeEntry({ ref: "ref-a", attention: "recent" }),
        makeEntry({ ref: "ref-b", attention: "recent" }),
      ],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projector.project(state);
    const rows = view.groups.find((g) => g.id === "recent")?.rows ?? [];
    expect(rows[0]?.key).not.toBe(rows[1]?.key);
  });

  it("serialized view JSON does not contain any raw ref", () => {
    const projector = createRosterProjector();
    const ref = "ref-leak-check-xyz";
    const state: RosterStateInput = {
      entries: [makeEntry({ ref, title: "Check", attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projector.project(state);
    const serialized = JSON.stringify(view);
    expect(serialized).not.toContain(ref);
  });

  it("opaque keys are stable across independent projector instances for the same ref", () => {
    const entry = makeEntry({ ref: "ref-cross-instance", attention: "recent" });
    const state: RosterStateInput = {
      entries: [entry],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const projector1 = createRosterProjector();
    const projector2 = createRosterProjector();
    const { view: v1 } = projector1.project(state);
    const { view: v2 } = projector2.project(state);
    const key1 = v1.groups.find((g) => g.id === "recent")?.rows[0]?.key;
    const key2 = v2.groups.find((g) => g.id === "recent")?.rows[0]?.key;
    // Same ref MUST produce the same opaque key even across independent
    // projector instances — the key derivation must be deterministic from
    // the ref alone, not from instance-local random state.
    expect(key1).toBe(key2);
  });

  it("refsByKey maps the opaque key back to the raw ref", () => {
    const projector = createRosterProjector();
    const ref = "ref-lookup-test";
    const state: RosterStateInput = {
      entries: [makeEntry({ ref, attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view, refsByKey } = projector.project(state);
    const row = view.groups.find((g) => g.id === "recent")?.rows[0];
    expect(row).toBeDefined();
    expect(refsByKey.get(row?.key ?? "")).toBe(ref);
  });

  it("row key does not contain the ref even when ref has special characters", () => {
    const projector = createRosterProjector();
    const ref = "threads/abc-123_def@test";
    const state: RosterStateInput = {
      entries: [makeEntry({ ref, attention: "recent" })],
      loading: false,
      error: null,
      searchTerm: "",
      hasMore: false,
      sessionsVisible: true,
    };
    const { view } = projector.project(state);
    const row = view.groups.find((g) => g.id === "recent")?.rows[0];
    expect(row?.key).not.toContain(ref);
    // No substring of the ref should appear in the key
    expect(row?.key).not.toContain("abc-123");
    expect(row?.key).not.toContain("threads");
  });
});
