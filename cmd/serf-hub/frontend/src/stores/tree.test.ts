import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import {
  attentionChangedNotification,
  threadClosedNotification,
  threadStartedNotification,
} from "../protocol/testing/notifications";
import { connectionStore } from "./connection";
import { REFRESH_NOTIFICATIONS, resetTreeStoreForTests, type TreeNode, type TreeResponse, treeStore } from "./tree";

// A minimal, well-formed Response stand-in - only what refresh()/
// loadProjectDetail() actually touch (ok/status/statusText/json()).
function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(body),
  } as Response;
}

// The wire shape GET /api/tree sends when there's nothing to report - every
// array field explicitly null, exactly as Go's encoding/json renders a nil
// slice with no `omitempty` (see hubapi/types.go's TreeResponse/TreeProject).
const EMPTY_WIRE_TREE = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: null,
  live: null,
  needs_you: null,
  favorites: null,
  projects: null,
  archived_projects: null,
  test_runs: null,
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

const NORMALIZED_EMPTY_TREE = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  favorites: [],
  projects: [],
  archived_projects: [],
  test_runs: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetTreeStoreForTests();
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("refresh", () => {
  test("fetches GET /api/tree with same-origin credentials", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    await treeStore.getState().refresh();
    expect(fetchMock).toHaveBeenCalledWith("/api/tree", { credentials: "same-origin" });
  });

  test("normalizes every nullable top-level wire array to []", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    await treeStore.getState().refresh();
    const { tree, loading, error } = treeStore.getState();
    expect(loading).toBe(false);
    expect(error).toBeNull();
    expect(tree).toEqual(NORMALIZED_EMPTY_TREE);
  });

  test("normalizes nested nullable arrays: TreeProject.sessions and TreeNode.children", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        ...EMPTY_WIRE_TREE,
        projects: [
          {
            key: "p1",
            name: "Proj",
            sessions: [
              {
                row_id: "project:p1:local:a",
                ref: "local:a",
                host_id: "local",
                session_id: "a",
                title: "A",
                project: "Proj",
                state: "idle",
                kind: "session",
                live: true,
                children: null,
              },
            ],
          },
        ],
        archived_projects: [{ key: "p2", name: "Archived proj", sessions: null, session_count: 3 }],
      }),
    );
    await treeStore.getState().refresh();
    const { tree } = treeStore.getState();
    expect(tree?.projects[0]?.sessions[0]?.children).toEqual([]);
    expect(tree?.archived_projects[0]?.sessions).toEqual([]);
    expect(tree?.archived_projects[0]?.session_count).toBe(3);
  });

  // TreeProject.favorite (hubapi's Go field is `omitempty bool`, like
  // TreeNode's own favorite) is never a nullable array - unlike sessions/
  // children above, there's no explicit null to collapse, only "present and
  // true" vs. "absent" - both pass straight through normalizeProject
  // unchanged. Locks that this task's new field survives the wire round
  // trip, both ways.
  test("passes TreeProject.favorite through unchanged: present when the wire sends it, absent otherwise", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        ...EMPTY_WIRE_TREE,
        projects: [
          { key: "p1", name: "Favorited", sessions: null, favorite: true },
          { key: "p2", name: "Not favorited", sessions: null },
        ],
      }),
    );
    await treeStore.getState().refresh();
    const { tree } = treeStore.getState();
    expect(tree?.projects[0]?.favorite).toBe(true);
    expect(tree?.projects[1]?.favorite).toBeUndefined();
  });

  test("loading is true while the request is in flight and false once it settles", async () => {
    let resolveFetch!: (value: Response) => void;
    fetchMock.mockReturnValueOnce(
      new Promise<Response>((resolve) => {
        resolveFetch = resolve;
      }),
    );
    const pending = treeStore.getState().refresh();
    expect(treeStore.getState().loading).toBe(true);
    resolveFetch(jsonResponse(EMPTY_WIRE_TREE));
    await pending;
    expect(treeStore.getState().loading).toBe(false);
  });

  test("a non-ok response records an error and never throws", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "boom" }, 500));
    await expect(treeStore.getState().refresh()).resolves.toBe(false);
    const { tree, loading, error } = treeStore.getState();
    expect(tree).toBeNull();
    expect(loading).toBe(false);
    expect(error).not.toBeNull();
  });

  test("a rejected fetch (network failure) is caught, not thrown", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("network down"));
    await expect(treeStore.getState().refresh()).resolves.toBe(false);
    expect(treeStore.getState().error).toContain("network down");
  });

  test("a later successful refresh clears a previous error", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}, 500));
    await treeStore.getState().refresh();
    expect(treeStore.getState().error).not.toBeNull();

    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    await treeStore.getState().refresh();
    expect(treeStore.getState().error).toBeNull();
    expect(treeStore.getState().tree).toEqual(NORMALIZED_EMPTY_TREE);
  });

  test("reports refresh failure without replacing the current tree", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    expect(await treeStore.getState().refresh()).toBe(true);
    fetchMock.mockRejectedValueOnce(new TypeError("refresh down"));

    expect(await treeStore.getState().refresh()).toBe(false);
    expect(treeStore.getState().tree).toEqual(NORMALIZED_EMPTY_TREE);
    expect(treeStore.getState().error).toContain("refresh down");
  });

  test("only the newest refresh response can update the tree", async () => {
    let resolveOlder!: (response: Response) => void;
    let resolveNewer!: (response: Response) => void;
    fetchMock
      .mockReturnValueOnce(
        new Promise<Response>((resolve) => {
          resolveOlder = resolve;
        }),
      )
      .mockReturnValueOnce(
        new Promise<Response>((resolve) => {
          resolveNewer = resolve;
        }),
      );

    const older = treeStore.getState().refresh();
    const newer = treeStore.getState().refresh();
    resolveNewer(jsonResponse({ ...EMPTY_WIRE_TREE, generated_at: "newer" }));
    expect(await newer).toBe(true);
    resolveOlder(jsonResponse({ ...EMPTY_WIRE_TREE, generated_at: "older" }));
    expect(await older).toBe(false);

    expect(treeStore.getState().tree?.generated_at).toBe("newer");
    expect(treeStore.getState().loading).toBe(false);
  });
});

describe("reconcileProjectDelete", () => {
  test("filters deleted sessions while retaining skipped and unmentioned rows", () => {
    const deleted: TreeNode = {
      row_id: "deleted-row",
      ref: "local:deleted",
      host_id: "local",
      session_id: "deleted",
      title: "Deleted",
      project: "Proj",
      state: "ended",
      kind: "session",
      live: false,
      children: [],
    };
    const skipped = {
      ...deleted,
      row_id: "skipped-row",
      ref: "local:skipped",
      session_id: "skipped",
      title: "Skipped",
    };
    const unknownChild = {
      ...deleted,
      row_id: "unknown-child-row",
      ref: "remote:child",
      host_id: "remote",
      session_id: "child",
      title: "Unknown child",
    };
    const unknown = {
      ...deleted,
      row_id: "unknown-row",
      ref: "remote:new",
      host_id: "remote",
      session_id: "new",
      title: "Unknown",
      children: [unknownChild],
    };
    const p = { key: "p1", name: "Proj", sessions: [deleted, skipped, unknown] };
    const detail = { ...p, sessions: [deleted, skipped, unknown] };
    treeStore.setState({
      tree: { ...NORMALIZED_EMPTY_TREE, projects: [p], archived_projects: [p] },
      projectDetails: new Map([["p1", detail]]),
    });

    treeStore.getState().reconcileProjectDelete("p1", ["deleted"], ["local:skipped"]);

    const state = treeStore.getState();
    const expected = ["local:skipped", "remote:new"];
    expect(state.tree?.projects[0]?.sessions.map((n) => n.ref)).toEqual(expected);
    expect(state.tree?.projects[0]?.session_count).toBe(2);
    expect(state.tree?.projects[0]?.sessions[1]?.children.map((n) => n.ref)).toEqual(["remote:child"]);
    expect(state.tree?.archived_projects[0]?.sessions.map((n) => n.ref)).toEqual(expected);
    expect(state.projectDetails.get("p1")?.sessions.map((n) => n.ref)).toEqual(expected);
  });

  test("retains unmentioned remote rows when no sessions were skipped", () => {
    const deleted: TreeNode = {
      row_id: "deleted-row",
      ref: "local:deleted",
      host_id: "local",
      session_id: "deleted",
      title: "Deleted",
      project: "Proj",
      state: "ended",
      kind: "session",
      live: false,
      children: [],
    };
    const unknown = {
      ...deleted,
      row_id: "remote-row",
      ref: "remote:new",
      host_id: "remote",
      session_id: "new",
      title: "Remote",
    };
    const p = { key: "p1", name: "Proj", sessions: [deleted, unknown] };
    treeStore.setState({
      tree: { ...NORMALIZED_EMPTY_TREE, projects: [p] },
      projectDetails: new Map([["p1", { ...p }]]),
    });

    treeStore.getState().reconcileProjectDelete("p1", ["local:deleted"], []);

    expect(treeStore.getState().tree?.projects[0]?.sessions.map((n) => n.ref)).toEqual(["remote:new"]);
    expect(treeStore.getState().tree?.projects[0]?.session_count).toBe(1);
    expect(
      treeStore
        .getState()
        .projectDetails.get("p1")
        ?.sessions.map((n) => n.ref),
    ).toEqual(["remote:new"]);
  });

  test("removes a fully deleted project and its hydrated detail", () => {
    const deleted: TreeNode = {
      row_id: "deleted-row",
      ref: "local:deleted",
      host_id: "local",
      session_id: "deleted",
      title: "Deleted",
      project: "Proj",
      state: "ended",
      kind: "session",
      live: false,
      children: [],
    };
    const p = { key: "p1", name: "Proj", sessions: [deleted] };
    treeStore.setState({
      tree: { ...NORMALIZED_EMPTY_TREE, projects: [p] },
      projectDetails: new Map([["p1", { ...p }]]),
    });

    treeStore.getState().reconcileProjectDelete("p1", ["local:deleted"], []);

    expect(treeStore.getState().tree?.projects).toEqual([]);
    expect(treeStore.getState().projectDetails.has("p1")).toBe(false);
  });
});

describe("loadProjectDetail", () => {
  test("fetches GET /api/tree/project?key=<url-encoded key> with same-origin credentials", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        key: "p/1",
        name: "Proj",
        sessions: [
          {
            row_id: "r1",
            ref: "local:a",
            host_id: "local",
            session_id: "a",
            title: "A",
            project: "Proj",
            state: "idle",
            kind: "session",
            live: true,
          },
        ],
      }),
    );
    await treeStore.getState().loadProjectDetail("p/1");
    expect(fetchMock).toHaveBeenCalledWith("/api/tree/project?key=p%2F1", { credentials: "same-origin" });
    const detail = treeStore.getState().projectDetails.get("p/1");
    expect(detail?.sessions).toHaveLength(1);
    expect(detail?.sessions[0]?.title).toBe("A");
  });

  test("normalizes a null sessions array on the detail response too", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ key: "p1", name: "Proj", sessions: null }));
    await treeStore.getState().loadProjectDetail("p1");
    expect(treeStore.getState().projectDetails.get("p1")?.sessions).toEqual([]);
  });

  test("a failed load leaves projectDetails unchanged and never throws (retriable)", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "not found" }, 404));
    await expect(treeStore.getState().loadProjectDetail("missing")).resolves.toBeUndefined();
    expect(treeStore.getState().projectDetails.has("missing")).toBe(false);
  });

  test("a late detail response cannot resurrect a reconciled project", async () => {
    let resolveDetail!: (response: Response) => void;
    fetchMock.mockReturnValueOnce(
      new Promise<Response>((resolve) => {
        resolveDetail = resolve;
      }),
    );
    const loading = treeStore.getState().loadProjectDetail("p1");

    treeStore.setState({ tree: { ...NORMALIZED_EMPTY_TREE, projects: [{ key: "p1", name: "Proj", sessions: [] }] } });
    treeStore.getState().reconcileProjectDelete("p1", [], []);
    resolveDetail(jsonResponse({ key: "p1", name: "Proj", sessions: [{ ref: "local:deleted" }] }));

    await loading;
    expect(treeStore.getState().projectDetails.has("p1")).toBe(false);
    expect(treeStore.getState().tree?.projects).toEqual([]);
  });
});

describe("loadProjectPage", () => {
  test("fetches an offset page and merges it into the requested tier", async () => {
    const current: TreeNode[] = Array.from({ length: 50 }, (_, i) => ({
      row_id: `r${i}`,
      ref: `local:${i}`,
      host_id: "local",
      session_id: `${i}`,
      title: `Current ${i}`,
      project: "Proj",
      state: "ended",
      kind: "session",
      tier: "current",
      live: false,
      children: [],
    }));
    const recent: TreeNode = { ...current[0]!, row_id: "recent", ref: "local:recent", tier: "recent", title: "Recent" };
    const tree: TreeResponse = {
      generated_at: "2026-01-01T00:00:00Z",
      sources: [],
      live: [],
      needs_you: [],
      favorites: [],
      projects: [{ key: "p1", name: "Proj", sessions: [...current, recent], more_current: 1 }],
      archived_projects: [],
      test_runs: [],
      attentionSummary: { needsYou: 0, error: 0, working: 0 },
    };
    treeStore.setState({ tree });
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        key: "p1",
        tier: "current",
        offset: 50,
        sessions: [{ ...current[0]!, row_id: "r50", ref: "local:50", session_id: "50", title: "Current 50" }],
        remaining: 0,
      }),
    );

    await treeStore.getState().loadProjectPage("p1", "current", 50, 50);

    expect(fetchMock).toHaveBeenCalledWith("/api/tree/project?key=p1&tier=current&offset=50&limit=50", {
      credentials: "same-origin",
    });
    const project = treeStore.getState().tree?.projects[0];
    expect(project?.sessions.filter((n) => n.tier === "current")).toHaveLength(51);
    expect(project?.sessions[50]?.title).toBe("Current 50");
    expect(project?.sessions[51]?.title).toBe("Recent");
    expect(project?.more_current).toBe(0);
  });

  test("a late page response cannot overwrite a reconciled project", async () => {
    let resolvePage!: (response: Response) => void;
    fetchMock.mockReturnValueOnce(
      new Promise<Response>((resolve) => {
        resolvePage = resolve;
      }),
    );
    const deleted: TreeNode = {
      row_id: "deleted-row",
      ref: "local:deleted",
      host_id: "local",
      session_id: "deleted",
      title: "Deleted",
      project: "Proj",
      state: "ended",
      kind: "session",
      live: false,
      children: [],
    };
    const remote = {
      ...deleted,
      row_id: "remote-row",
      ref: "remote:new",
      host_id: "remote",
      session_id: "new",
      title: "Remote",
    };
    const p = { key: "p1", name: "Proj", sessions: [deleted, remote] };
    treeStore.setState({ tree: { ...NORMALIZED_EMPTY_TREE, projects: [p] }, projectDetails: new Map([["p1", p]]) });
    const loading = treeStore.getState().loadProjectPage("p1", "current", 50, 50);

    treeStore.getState().reconcileProjectDelete("p1", ["local:deleted"], []);
    resolvePage(
      jsonResponse({
        key: "p1",
        tier: "current",
        offset: 50,
        sessions: [{ ...remote, row_id: "late-row", ref: "local:late", session_id: "late", title: "Late" }],
        remaining: 0,
      }),
    );

    await loading;
    expect(treeStore.getState().tree?.projects[0]?.sessions.map((n) => n.ref)).toEqual(["remote:new"]);
    expect(
      treeStore
        .getState()
        .projectDetails.get("p1")
        ?.sessions.map((n) => n.ref),
    ).toEqual(["remote:new"]);
  });
});

describe("REFRESH_NOTIFICATIONS", () => {
  test("lists exactly the wired notifications, including the hub's dedicated tree push", () => {
    expect(REFRESH_NOTIFICATIONS).toEqual([
      "thread/started",
      "thread/closed",
      "serf/attention/changed",
      "serf/tree/changed",
    ]);
  });
});

describe("notification-triggered refetch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  test("thread/started schedules a refetch, debounced 250ms", async () => {
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    const fake = connectFakeClient();
    await treeStore.getState().refresh(); // initial load; also wires notification handling
    fetchMock.mockClear();

    fake.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(249);
    expect(fetchMock).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith("/api/tree", { credentials: "same-origin" });
  });

  test("thread/closed schedules a debounced refetch", async () => {
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    const fake = connectFakeClient();
    await treeStore.getState().refresh();
    fetchMock.mockClear();

    fake.emitNotification(threadClosedNotification());
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test("serf/attention/changed schedules a debounced refetch", async () => {
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    const fake = connectFakeClient();
    await treeStore.getState().refresh();
    fetchMock.mockClear();

    fake.emitNotification(attentionChangedNotification());
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test("an irrelevant notification does not trigger a refetch", async () => {
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    const fake = connectFakeClient();
    await treeStore.getState().refresh();
    fetchMock.mockClear();

    fake.emitNotification({
      method: "turn/started",
      params: { threadId: "t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    });
    await vi.advanceTimersByTimeAsync(1000);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  test("a burst of qualifying notifications coalesces into exactly one refetch", async () => {
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    const fake = connectFakeClient();
    await treeStore.getState().refresh();
    fetchMock.mockClear();

    fake.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(100);
    fake.emitNotification(threadClosedNotification());
    await vi.advanceTimersByTimeAsync(100);
    fake.emitNotification(attentionChangedNotification());
    await vi.advanceTimersByTimeAsync(100); // 300ms elapsed total, but each notification reset the window

    expect(fetchMock).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(150); // 250ms since the last notification
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test("wiring attaches even when the client connects AFTER the first refresh() call", async () => {
    // Mirrors the real mount order: a sibling component's mount effect can
    // call refresh() before AppShell's own effect has connected the client
    // (child effects run before parent effects in the same commit).
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    await treeStore.getState().refresh(); // client is still null here
    fetchMock.mockClear();

    const fake = connectFakeClient(); // connects later
    fake.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test("reconnecting to a new client does not double-fire a refetch per notification", async () => {
    fetchMock.mockResolvedValue(jsonResponse(EMPTY_WIRE_TREE));
    const first = connectFakeClient();
    await treeStore.getState().refresh();
    fetchMock.mockClear();

    const second = new FakeClient("ready");
    connectionStore.getState().connect(second);

    second.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    fetchMock.mockClear();
    first.emitNotification(threadStartedNotification()); // stale client, still attached, but that's fine - not a regression to guard beyond "no crash"
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
