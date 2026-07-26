import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { resetTreeStoreForTests } from "../../stores/tree";
import { Toast } from "../../widgets";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { Rail } from "./Rail";

// See shell/DockHost.test.tsx's identical comment: Node 26 shadows jsdom's
// real window.localStorage with its own (non-functional under vitest)
// global, so every test file that touches localStorage needs this same
// small in-memory stand-in. Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(async () => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
  // Registers the real "session" pane type (side-effect import), mirroring
  // shell/DockHost.test.tsx's identical precedent - in production this
  // already happened before Rail ever mounts (AppShell.tsx imports
  // "../panes/session" at module scope), so activating a session row here
  // exercises the same workspaceStore.openPane() path against a real,
  // registered pane type rather than a hand-rolled fixture.
  await import("../../panes/session");
});

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => (body === undefined ? Promise.reject(new Error("no body")) : Promise.resolve(body)),
  } as Response;
}

function wireNode(overrides: Record<string, unknown> = {}) {
  return {
    row_id: "row1",
    ref: "local:row1",
    host_id: "local",
    session_id: "row1",
    title: "Session",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: null,
    ...overrides,
  };
}

function wireProject(overrides: Record<string, unknown> = {}) {
  return { key: "p", name: "Proj", sessions: [], ...overrides };
}

const NEEDS_YOU_NODE = wireNode({
  row_id: "needsyou:local:ny1",
  ref: "local:ny1",
  title: "Needs you session",
  state: "awaiting",
});
const LIVE_NODE = wireNode({ row_id: "live:local:live1", ref: "local:live1", title: "Live session", state: "active" });
const PINNED_NODE = wireNode({
  row_id: "pinned:local:pin1",
  ref: "local:pin1",
  title: "Pinned session",
  favorite: true,
});
const PROJECT_SESSION = wireNode({
  row_id: "project:proj1:local:s1",
  ref: "local:s1",
  title: "Session one",
  favorite: false,
  rename: true,
  tier: "current",
});
const PROJECT = wireProject({
  key: "proj1",
  name: "prime-radiant",
  working_dir: "/home/user/prime-radiant",
  default_expanded: true,
  sessions: [PROJECT_SESSION],
});
const ARCHIVED_PROJECT = wireProject({
  key: "archproj",
  name: "old-project",
  working_dir: "/home/user/old-project",
  is_archived: true,
  session_count: 1,
  sessions: null,
});
const TESTRUN_SESSION = wireNode({ row_id: "project:testrun1:local:tr1", ref: "local:tr1", title: "Test run session" });
const TESTRUN_PROJECT = wireProject({ key: "testrun1", name: "test-run-1", sessions: [TESTRUN_SESSION] });

const SAMPLE_WIRE_TREE = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [LIVE_NODE],
  needs_you: [NEEDS_YOU_NODE],
  favorites: [PINNED_NODE],
  projects: [PROJECT],
  archived_projects: [ARCHIVED_PROJECT],
  test_runs: [TESTRUN_PROJECT],
  attentionSummary: { needsYou: 1, error: 0, working: 0 },
};

// An ACTIVE project carrying one archived-tier session. The wire ships every
// tier in one `sessions` array (web_api_tree.go's projectSessions), which is
// exactly the case the rail has to divert.
const ARCHIVED_SESSION = wireNode({
  row_id: "project:proj1:local:s-old",
  ref: "local:s-old",
  title: "Old archived session",
  tier: "archived",
});
const WIRE_TREE_WITH_ARCHIVED_SESSION = {
  ...SAMPLE_WIRE_TREE,
  projects: [wireProject({ ...PROJECT, sessions: [PROJECT_SESSION, ARCHIVED_SESSION] })],
};

// A project session with one finished subagent, which the rail folds away
// (railNodes' splitChildren) behind a per-parent disclosure.
const WIRE_TREE_WITH_INACTIVE_SUBAGENT = {
  ...SAMPLE_WIRE_TREE,
  projects: [
    wireProject({
      ...PROJECT,
      sessions: [
        {
          ...PROJECT_SESSION,
          children: [
            wireNode({
              row_id: "project:proj1:local:sub1",
              ref: "local:sub1",
              title: "Finished helper",
              kind: "subagent",
              state: "ended",
            }),
          ],
        },
      ],
    }),
  ],
};

const EMPTY_WIRE_TREE = {
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

const ARCHIVED_PROJECT_DETAIL = {
  key: "archproj",
  name: "old-project",
  sessions: [wireNode({ row_id: "r-old1", ref: "local:old1", title: "Old session" })],
};

let fetchMock: ReturnType<typeof vi.fn<(url: string, init?: RequestInit) => Promise<Response>>>;

// The nearest ancestor role=treeitem for a row's visible label text -
// scopes a within() query to one specific row when several rows share the
// same control (every row has a chevron/menu button with the same role).
function rowFor(labelText: string): HTMLElement {
  const row = screen.getByText(labelText).closest('[role="treeitem"]');
  if (!(row instanceof HTMLElement)) throw new Error(`no treeitem ancestor found for "${labelText}"`);
  return row;
}

let treeResponseBody: unknown;
let postResponses: Record<string, { status: number; body: unknown }>;
let postCalls: { path: string; body: unknown }[];
let projectDetailResponses: Record<string, unknown>;

function defaultPostResponses(): Record<string, { status: number; body: unknown }> {
  return {
    "/api/favorite": { status: 200, body: { ok: true } },
    "/api/archive": { status: 200, body: { ok: true } },
    "/api/project/delete": { status: 200, body: { deleted: ["local:old1"], skipped: [] } },
    rename: { status: 204, body: undefined },
  };
}

beforeEach(() => {
  resetTreeStoreForTests();
  resetWorkspaceStoreForTests();
  localStorage.clear();

  treeResponseBody = SAMPLE_WIRE_TREE;
  postResponses = defaultPostResponses();
  postCalls = [];
  projectDetailResponses = { archproj: ARCHIVED_PROJECT_DETAIL };

  fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (method === "GET" && url === "/api/tree") return jsonResponse(treeResponseBody);
    if (method === "GET" && url.startsWith("/api/tree/project?key=")) {
      const key = decodeURIComponent(url.slice("/api/tree/project?key=".length));
      const body = projectDetailResponses[key];
      return body ? jsonResponse(body) : jsonResponse({ error: "not found" }, 404);
    }
    if (method === "POST") {
      const body = JSON.parse(init!.body as string) as unknown;
      postCalls.push({ path: url, body });
      if (url.startsWith("/api/sessions/") && url.endsWith("/rename")) {
        const scripted = postResponses.rename!;
        return jsonResponse(scripted.body, scripted.status);
      }
      const scripted = postResponses[url];
      if (!scripted) throw new Error(`unscripted POST ${url}`);
      return jsonResponse(scripted.body, scripted.status);
    }
    throw new Error(`unhandled fetch: ${method} ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function renderRail() {
  return render(
    <>
      <Rail />
      <Toast />
    </>,
  );
}

function treeGetCallCount(): number {
  return fetchMock.mock.calls.filter((call) => call[0] === "/api/tree" && (call[1]?.method ?? "GET") === "GET").length;
}

describe("initial load", () => {
  test("fetches the tree on mount", async () => {
    renderRail();
    await screen.findByText("Live session");
    expect(treeGetCallCount()).toBe(1);
  });

  test("shows a loading skeleton before the first response resolves", async () => {
    let resolveFetch!: (value: Response) => void;
    fetchMock.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    renderRail();
    expect(screen.getByRole("status", { name: /loading/i })).toBeTruthy();
    resolveFetch(jsonResponse(SAMPLE_WIRE_TREE));
    await screen.findByText("Live session");
  });

  test("shows an error state with a retry action when the fetch fails, and retry re-fetches successfully", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "boom" }, 500));
    renderRail();
    await screen.findByText(/couldn.t load sessions/i);

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /retry/i }));
    await screen.findByText("Live session");
  });

  test("shows an empty state when every tier is empty", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    renderRail();
    await screen.findByText(/no sessions yet/i);
  });
});

describe("sections", () => {
  test("renders each non-empty tier as its own heading, in tier order", async () => {
    renderRail();
    await screen.findByText("Live session");
    const headingNames = screen.getAllByRole("heading").map((h) => h.textContent);
    const order = ["Live", "Pinned", "Projects", "Test runs"];
    const positions = order.map((name) => headingNames.indexOf(name));
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
    expect(positions.every((p) => p >= 0)).toBe(true);
  });

  test("omits a tier's heading entirely when it has no rows", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...SAMPLE_WIRE_TREE, live: [], test_runs: [] }));
    renderRail();
    await screen.findByText("Pinned session");
    expect(screen.queryByText("Live")).toBeNull();
    expect(screen.queryByText("Test runs")).toBeNull();
  });

  test("the Archived disclosure is present but collapsed by default", async () => {
    renderRail();
    await screen.findByText("Live session");
    const disclosure = screen.getByRole("button", { name: /archived/i });
    expect(disclosure.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("old-project")).toBeNull();
  });

  test("omits the Archived disclosure entirely when there is nothing archived at all", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...SAMPLE_WIRE_TREE, archived_projects: [] }));
    renderRail();
    await screen.findByText("Live session");
    expect(screen.queryByRole("button", { name: /archived/i })).toBeNull();
  });

  // parity-m3-sidebar-tree.md §8: one disclosure at the very bottom, holding
  // everything archived hub-wide. It is the last thing in the rail because it
  // is the least likely thing you came here for.
  describe("Archived sessions section", () => {
    test("sits below every other section, including Test runs", async () => {
      renderRail();
      await screen.findByText("Live session");
      const disclosure = screen.getByRole("button", { name: /archived/i });
      const testRuns = screen.getByRole("heading", { name: "Test runs" });
      // DOCUMENT_POSITION_FOLLOWING: the disclosure comes after Test runs.
      expect(testRuns.compareDocumentPosition(disclosure) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    test("says how many sessions it stands for", async () => {
      renderRail();
      await screen.findByText("Live session");
      expect(screen.getByRole("button", { name: /archived sessions \(1\)/i })).toBeTruthy();
    });

    test("counts an active project's archived sessions alongside whole archived projects", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(WIRE_TREE_WITH_ARCHIVED_SESSION));
      renderRail();
      await screen.findByText("Live session");
      expect(screen.getByRole("button", { name: /archived sessions \(2\)/i })).toBeTruthy();
    });

    test("an active project never lists its archived sessions inline, even expanded", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(WIRE_TREE_WITH_ARCHIVED_SESSION));
      renderRail();
      await screen.findByText("Session one"); // PROJECT is default_expanded
      expect(screen.queryByText("Old archived session")).toBeNull();
    });

    test("reveals an active project's archived sessions under its own sub-branch", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(WIRE_TREE_WITH_ARCHIVED_SESSION));
      renderRail();
      await screen.findByText("Session one");
      const user = userEvent.setup();
      const disclosure = screen.getByRole("button", { name: /archived sessions/i });
      await user.click(disclosure);
      // The project deliberately appears TWICE once this is open - once under
      // Projects for its live sessions, once here for its archived ones - so
      // this has to ask the archived section specifically.
      const archivedSection = disclosure.closest("section");
      if (!archivedSection) throw new Error("archived disclosure has no owning section");
      const subBranch = within(archivedSection).getByRole("treeitem", { name: /prime-radiant/ });
      await user.click(within(subBranch).getByTestId("rail-chevron"));
      expect(await within(archivedSection).findByText("Old archived session")).toBeTruthy();
      // The diverted session is the ONLY thing behind the sub-branch: the
      // project's live session stays where it was.
      expect(within(archivedSection).queryByText("Session one")).toBeNull();
    });

    test("appears for an archived session even when no whole project is archived", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ ...WIRE_TREE_WITH_ARCHIVED_SESSION, archived_projects: [] }));
      renderRail();
      await screen.findByText("Session one");
      expect(screen.getByRole("button", { name: /archived sessions \(1\)/i })).toBeTruthy();
    });
  });

  // vbh8/§2.2: the auto-grouped "Needs you" RailSection is gone - attention
  // surfaces inline instead (the session's own Cadence dot, plus a derived
  // descendant-count Badge - see RailRow.test.tsx). Before this fix, a
  // needs-you session listed twice: once here, once under its project. The
  // tree still carries a populated needs_you tier on the wire (the store
  // tiers themselves are untouched - only this RailSection is removed), so
  // this seeds one to prove the removal, not just an empty-tier no-op.
  test("drops the auto-grouped 'Needs you' section; a needs-you session renders only once, under its project (vbh8)", async () => {
    const sharedRef = "local:shared1";
    const sharedInProject = wireNode({
      row_id: "project:sharedproj:local:shared1",
      ref: sharedRef,
      title: "Shared session",
      state: "awaiting",
    });
    const sharedInNeedsYou = wireNode({
      row_id: "needsyou:local:shared1",
      ref: sharedRef,
      title: "Shared session",
      state: "awaiting",
    });
    const sharedProject = wireProject({
      key: "sharedproj",
      name: "shared-project",
      default_expanded: true,
      sessions: [sharedInProject],
    });
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        ...EMPTY_WIRE_TREE,
        needs_you: [sharedInNeedsYou],
        favorites: [PINNED_NODE],
        projects: [sharedProject],
        attentionSummary: { needsYou: 1, error: 0, working: 0 },
      }),
    );
    renderRail();
    await screen.findByText("Shared session");

    expect(screen.getAllByText("Shared session")).toHaveLength(1); // no duplicate listing
    expect(screen.queryByRole("heading", { name: "Needs you" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Pinned" })).toBeTruthy(); // other sections unaffected
  });
});

describe("in-sidebar chrome (c8gt)", () => {
  // Seed an empty tree so /new session/i is unambiguous: SAMPLE_WIRE_TREE's
  // project rows render their own "New session in {project}" IconButtons,
  // which also match that name. The header/footer chrome renders regardless
  // of tree contents (it lives outside the tree-body conditional), so an
  // empty tree still exercises it fully while leaving exactly one New-session
  // control in the DOM - the chrome's own.
  test("renders in-sidebar chrome: search opens palette, + New session, settings", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    renderRail();
    await screen.findByText(/no sessions yet/i);
    const search = screen.getByTestId("rail-search");
    expect(search.getAttribute("data-search-trigger")).not.toBeNull(); // wired to the palette handler
    // Icon-only, so its accessible name is the whole affordance: an
    // unlabelled ⌕ glyph would leave screen readers and tooltip-less
    // discovery with nothing at all.
    expect(search.getAttribute("aria-label")).toBe("Search");
    expect(screen.getByRole("button", { name: /new session/i })).toBeTruthy();
    expect(screen.getByTestId("rail-settings")).toBeTruthy();
  });

  test("search rides in the brand row rather than claiming a header row of its own", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    renderRail();
    await screen.findByText(/no sessions yet/i);
    // The rail is 280px wide and every header row costs the session tree
    // vertical space, so the palette opener shares the brand row's spare
    // width instead of stacking under it.
    const brandRow = screen.getByTestId("rail-brand");
    expect(brandRow.contains(screen.getByTestId("rail-search"))).toBe(true);
  });

  // The tooltip, not the accessible name, is where the ⌕ glyph's meaning
  // actually gets explained: the aria-label has to stay short enough to be a
  // usable control name, so "what does this search?" has nowhere else to go.
  // It said only "Search" for a while because a centre-anchored bubble with no
  // collision handling clipped a longer label against the rail's right edge -
  // widgets/tooltip shifts and portals now, and both the 280px docked rail and
  // the 430px mobile drawer were re-measured showing the full string.
  test("hovering search reveals a tooltip that says what the palette searches", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
      renderRail();
      await vi.waitFor(() => expect(screen.getByText(/no sessions yet/i)).toBeTruthy());
      fireEvent.mouseEnter(screen.getByTestId("rail-search"));
      // Tooltip's own 300ms show delay (widgets/tooltip/index.tsx).
      act(() => {
        vi.advanceTimersByTime(400);
      });
      expect(screen.getByRole("tooltip").textContent).toBe("Search sessions and commands");
    } finally {
      vi.useRealTimers();
    }
  });

  test("renders the SAME full chrome with no host props (drawer-hosted parity)", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(EMPTY_WIRE_TREE));
    render(
      <>
        <Rail />
        <Toast />
      </>,
    );
    await screen.findByText(/no sessions yet/i);
    // Collapsed mode + hostedInSheet are gone (2026-07-24): every Rail,
    // including the one inside the mobile TreeDrawer, carries the full
    // search/new/settings chrome.
    expect(screen.getByTestId("rail-search")).toBeTruthy();
    expect(screen.getByTestId("rail-settings")).toBeTruthy();
  });
});

describe("row activation", () => {
  test("activating a session row opens its pane via workspaceStore.openPane", async () => {
    renderRail();
    await screen.findByText("Live session");
    await userEvent.setup().click(screen.getByText("Live session"));
    const panes = workspaceStore.getState().panes;
    expect(panes).toHaveLength(1);
    expect(panes[0]).toMatchObject({ type: "session", params: { ref: "local:live1" } });
  });

  test("Enter on a focused row activates it the same as a click", async () => {
    renderRail();
    await screen.findByText("Live session");
    const row = rowFor("Live session");
    row.focus();
    fireEvent.keyDown(row, { key: "Enter" });
    expect(workspaceStore.getState().panes).toHaveLength(1);
  });
});

// A subagent opens BESIDE the session that spawned it, never as the main pane.
// See docs/web-ui/specs/2026-07-26-subagent-opens-beside-main.md - placement
// used to be decided purely by "is main free?", so a subagent took main
// whenever it happened to be, and (slot being assign-once and persisted) then
// stayed there forever with its own parent opening to its right.
describe("opening a nested session", () => {
  async function openSubagent() {
    treeResponseBody = WIRE_TREE_WITH_INACTIVE_SUBAGENT;
    renderRail();
    await screen.findByText("Session one");
    const user = userEvent.setup();
    await user.click(within(rowFor("Session one")).getByTestId("rail-chevron"));
    await user.click(within(rowFor("Inactive subagent (1)")).getByTestId("rail-chevron"));
    await user.click(screen.getByText("Finished helper"));
    return user;
  }

  const paneFor = (ref: string) =>
    workspaceStore.getState().panes.find((p) => (p.params as { ref: string }).ref === ref);

  test("the subagent lands in the secondary slot, not main", async () => {
    await openSubagent();
    expect(paneFor("local:sub1")?.slot).toBe("secondary");
  });

  test("its top-level parent is opened into the empty main slot", async () => {
    await openSubagent();
    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:s1" });
  });

  test("the subagent takes focus - it is what you clicked", async () => {
    await openSubagent();
    expect(workspaceStore.getState().focusedPaneId).toBe(paneFor("local:sub1")?.id);
  });

  test("a parent already in main is reused, not opened twice", async () => {
    treeResponseBody = WIRE_TREE_WITH_INACTIVE_SUBAGENT;
    renderRail();
    await screen.findByText("Session one");
    const user = userEvent.setup();
    await user.click(screen.getByText("Session one")); // parent -> main
    const mainId = workspaceStore.getState().mainPane()?.id;
    await user.click(within(rowFor("Session one")).getByTestId("rail-chevron"));
    await user.click(within(rowFor("Inactive subagent (1)")).getByTestId("rail-chevron"));
    await user.click(screen.getByText("Finished helper"));

    expect(workspaceStore.getState().mainPane()?.id).toBe(mainId);
    expect(
      workspaceStore.getState().panes.filter((p) => (p.params as { ref: string }).ref === "local:s1"),
    ).toHaveLength(1);
  });

  // Replacing whatever you were reading would be worse than the bug being
  // fixed: the parent is opened only into an EMPTY main slot.
  test("an unrelated session already in main is left alone", async () => {
    treeResponseBody = WIRE_TREE_WITH_INACTIVE_SUBAGENT;
    renderRail();
    await screen.findByText("Live session");
    const user = userEvent.setup();
    await user.click(screen.getByText("Live session")); // unrelated -> main
    await user.click(within(rowFor("Session one")).getByTestId("rail-chevron"));
    await user.click(within(rowFor("Inactive subagent (1)")).getByTestId("rail-chevron"));
    await user.click(screen.getByText("Finished helper"));

    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:live1" });
    expect(paneFor("local:sub1")?.slot).toBe("secondary");
    expect(paneFor("local:s1")).toBeUndefined(); // parent not force-opened over your work
  });

  // A layout saved before this fix can have a subagent stamped slot:"main"
  // permanently - slot is assign-once and persisted. Activating it repairs it
  // rather than requiring every saved arrangement to be discarded.
  test("a subagent already stuck in the main slot is moved out of it", async () => {
    treeResponseBody = WIRE_TREE_WITH_INACTIVE_SUBAGENT;
    // Simulate the stale layout: the subagent holds main before the rail mounts.
    workspaceStore.getState().openPane("session", { ref: "local:sub1" });
    expect(workspaceStore.getState().mainPane()?.params).toMatchObject({ ref: "local:sub1" });

    renderRail();
    await screen.findByText("Session one");
    const user = userEvent.setup();
    await user.click(within(rowFor("Session one")).getByTestId("rail-chevron"));
    await user.click(within(rowFor("Inactive subagent (1)")).getByTestId("rail-chevron"));
    await user.click(screen.getByText("Finished helper"));

    expect(paneFor("local:sub1")?.slot).toBe("secondary");
    expect(workspaceStore.getState().mainPane()?.params).not.toMatchObject({ ref: "local:sub1" });
  });
});

// A cluster row is a repeated-title FOLD, not a session: its ref is a
// synthetic "cluster:<hex>" (a SHA of project + title, hubcore/tree.go) that
// names no session, so opening a pane for it loads a transcript that never
// arrives. Confirmed in a real browser before this fix.
describe("activating a cluster row", () => {
  const CLUSTER_REF = "cluster:abc12345";
  const WIRE_TREE_WITH_CLUSTER = {
    ...SAMPLE_WIRE_TREE,
    projects: [
      wireProject({
        ...PROJECT,
        sessions: [
          wireNode({
            row_id: "project:proj1:cluster:abc12345",
            ref: CLUSTER_REF,
            title: "Repeated title",
            kind: "cluster",
            cluster_count: 3,
            state: "ended",
            children: [
              wireNode({ row_id: "m1", ref: "local:m1", title: "Member one", state: "ended" }),
              wireNode({ row_id: "m2", ref: "local:m2", title: "Member two", state: "ended" }),
            ],
          }),
        ],
      }),
    ],
  };

  test("opens no pane at all", async () => {
    treeResponseBody = WIRE_TREE_WITH_CLUSTER;
    renderRail();
    await screen.findByText("Repeated title");
    await userEvent.setup().click(screen.getByText("Repeated title"));
    expect(workspaceStore.getState().panes).toHaveLength(0);
  });

  test("toggles the fold open, the way its own chevron does", async () => {
    treeResponseBody = WIRE_TREE_WITH_CLUSTER;
    renderRail();
    await screen.findByText("Repeated title");
    expect(screen.queryByText("Member one")).toBeNull();
    await userEvent.setup().click(screen.getByText("Repeated title"));
    expect(await screen.findByText("Member one")).toBeTruthy();
  });

  // Its members are ordinary top-level sessions and still open normally.
  test("a cluster member still opens its own pane", async () => {
    treeResponseBody = WIRE_TREE_WITH_CLUSTER;
    renderRail();
    await screen.findByText("Repeated title");
    const user = userEvent.setup();
    await user.click(screen.getByText("Repeated title"));
    await user.click(await screen.findByText("Member one"));
    expect(workspaceStore.getState().panes).toHaveLength(1);
    expect(workspaceStore.getState().panes[0]).toMatchObject({ params: { ref: "local:m1" } });
  });
});

describe("keyboard roving through renderRow", () => {
  // Proves the Tree widget's own roving-tabindex/arrow-key mechanism still
  // works correctly with RailRow's content inside each row - a stray
  // tabIndex or a click handler that steals focus on the row content
  // (rather than the chevron/menu's own deliberately-scoped ones) would
  // break this. prime-radiant (default_expanded: true) with its one child
  // session is exactly "a branch with a visible child" - ArrowDown from
  // the branch should descend into it, per widgets/tree's own documented
  // Right/ArrowDown behavior.
  test("ArrowDown on an expanded project row moves focus into its child session row", async () => {
    renderRail();
    await screen.findByText("Session one");

    const projectRow = rowFor("prime-radiant");
    act(() => projectRow.focus());
    fireEvent.keyDown(projectRow, { key: "ArrowDown" });

    expect(document.activeElement).toBe(rowFor("Session one"));
  });
});

// parity-m3-sidebar-tree.md §10.1: the htmx UI remembered every disclosure
// across reloads and this one forgot all of them, so a rail arranged the way
// you wanted it reverted on every refresh.
describe("expand state persists across a remount", () => {
  // Remount = what a browser reload does to this component. The tree store is
  // reset alongside it so the second mount refetches, exactly as a reload
  // would; only localStorage survives, which is the whole point.
  async function remount() {
    cleanup();
    resetTreeStoreForTests();
    renderRail();
  }

  test("a project you collapsed is still collapsed after remounting", async () => {
    renderRail();
    await screen.findByText("Session one"); // default_expanded: true
    await userEvent.setup().click(within(rowFor("prime-radiant")).getByTestId("rail-chevron"));
    expect(screen.queryByText("Session one")).toBeNull();

    await remount();
    await screen.findByText("prime-radiant");
    expect(screen.queryByText("Session one")).toBeNull();
  });

  test("the Archived section remembers being open", async () => {
    renderRail();
    await screen.findByText("Live session");
    await userEvent.setup().click(screen.getByRole("button", { name: /archived sessions/i }));
    expect(screen.getByText("old-project")).toBeTruthy();

    await remount();
    expect(await screen.findByText("old-project")).toBeTruthy();
  });

  test("an inactive-subagent fold remembers being open", async () => {
    treeResponseBody = WIRE_TREE_WITH_INACTIVE_SUBAGENT;
    renderRail();
    await screen.findByText("Session one");
    const user = userEvent.setup();
    // The fold is a child of the session row, so the session has to be open
    // before the fold is even on screen.
    await user.click(within(rowFor("Session one")).getByTestId("rail-chevron"));
    await user.click(within(rowFor("Inactive subagent (1)")).getByTestId("rail-chevron"));
    expect(screen.getByText("Finished helper")).toBeTruthy();

    await remount();
    expect(await screen.findByText("Finished helper")).toBeTruthy();
  });

  test("a rail nobody has touched starts from the server's own defaults", async () => {
    renderRail();
    // PROJECT ships default_expanded: true and no stored override contradicts it.
    expect(await screen.findByText("Session one")).toBeTruthy();
  });
});

describe("project expansion", () => {
  test("collapsing and re-expanding a project toggles its sessions' visibility", async () => {
    renderRail();
    await screen.findByText("Session one"); // default_expanded: true

    // test-run-1 also has a (collapsed) chevron of its own - scope to
    // prime-radiant's row specifically rather than a global lookup.
    const projectRow = rowFor("prime-radiant");
    const user = userEvent.setup();
    await user.click(within(projectRow).getByTestId("rail-chevron"));
    expect(screen.queryByText("Session one")).toBeNull();

    await user.click(within(projectRow).getByTestId("rail-chevron"));
    expect(screen.getByText("Session one")).toBeTruthy();
  });

  test("expanding the Archived disclosure reveals its projects; expanding an archived project lazily loads and shows its sessions", async () => {
    renderRail();
    await screen.findByText("Live session");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /archived/i }));
    await screen.findByText("old-project");
    expect(fetchMock.mock.calls.some(([url]) => url === "/api/tree/project?key=archproj")).toBe(false);

    // "prime-radiant" (default_expanded: true) also shows a chevron, so
    // this must be scoped to old-project's own row rather than a global
    // getByTestId lookup.
    const oldProjectRow = rowFor("old-project");
    await user.click(within(oldProjectRow).getByTestId("rail-chevron"));
    await screen.findByText("Old session");
    expect(fetchMock.mock.calls.some(([url]) => url === "/api/tree/project?key=archproj")).toBe(true);
  });
});

// --- the rail's width is independent of its tree's content ---------------
//
// jsdom evaluates no real CSS layout, so these assert the declarations the
// contract rests on, not a measured width; the browser measurement that
// proves the layout itself is in this change's own commit message.
//
// The fix is `flex-shrink: 0` on .rail. The rail is a flex item beside the
// workspace host, whose `flex: 1 1 auto` basis is its own content width; when
// the two bases together exceed the row, the browser distributes the shortfall
// across every shrinkable item, taking the rail below its declared 280px.
// .body's scrolling is the necessary other half - it is what the rows too wide
// for a non-shrinking rail spill into.
describe("width stability across expand/collapse", () => {
  // Comments are stripped before matching: these rules' own doc comments
  // quote the very declarations being asserted ("`flex: none` + `min-width:
  // 0` make this width AUTHORITATIVE"), so a naive substring check passes on
  // the prose alone and keeps passing after the declaration is deleted.
  const railCSS = (): string => {
    const here = dirname(fileURLToPath(import.meta.url));
    return readFileSync(join(here, "Rail.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  };

  test(".rail refuses to shrink below its declared width", () => {
    const rule = /\.rail\s*\{[^}]*\}/.exec(railCSS());
    expect(rule).not.toBeNull();
    // The declared width is the dragged --rail-width, falling back to the
    // sidebar-width default; either spelling of "don't flex" satisfies the
    // no-shrink half (`flex: none` IS flex-grow:0 + flex-shrink:0).
    expect(rule?.[0]).toMatch(/width:\s*var\(--rail-width/);
    expect(rule?.[0]).toMatch(/flex:\s*none|flex-shrink:\s*0/);
    // The other half of the same contract: without this, a flex item's
    // default `min-width: auto` floors the rail at its content's minimum
    // size, so a wide row widens the sidebar - which is the bug itself.
    expect(rule?.[0]).toMatch(/min-width:\s*0/);
  });

  test(".body scrolls the rows a non-shrinking rail can't widen for", () => {
    const rule = /\.body\s*\{[^}]*\}/.exec(railCSS());
    expect(rule).not.toBeNull();
    // Any scrolling overflow value satisfies this: per CSS Overflow 3 an
    // unset overflow-x computes to `auto` (not `visible`) once the other axis
    // scrolls, so `overflow-y: auto` alone already scrolls both axes.
    expect(rule?.[0]).toMatch(/overflow(-x|-y)?:\s*(auto|scroll)/);
  });
});

describe("hide affordance (onHide)", () => {
  // The « button renders only when RailHost passes onHide (desktop); the
  // mobile drawer instance passes none - the drawer is its own show/hide.
  test("renders a Hide sidebar button that calls onHide when provided", async () => {
    const onHide = vi.fn();
    render(
      <>
        <Rail onHide={onHide} />
        <Toast />
      </>,
    );
    await screen.findByText("Live session");
    await userEvent.setup().click(screen.getByRole("button", { name: /hide sidebar/i }));
    expect(onHide).toHaveBeenCalledTimes(1);
  });

  test("renders no Hide sidebar button when onHide is absent (mobile drawer instance)", async () => {
    renderRail();
    await screen.findByText("Live session");
    expect(screen.queryByRole("button", { name: /hide sidebar/i })).toBeNull();
  });
});

// parity §6/§7/§8/§15: the row reflects your click before the round trip
// resolves. Rail refetches after every successful mutation, so the window is
// short - but on a loaded hub it is long enough to look like nothing happened,
// which is what makes people click twice.
describe("optimistic feedback", () => {
  // Hold the POST open so the assertion lands squarely inside the in-flight
  // window rather than racing the refetch.
  function heldPost(path: string): { release: () => void } {
    let release = () => {};
    const held = new Promise<void>((r) => {
      release = () => r();
    });
    const original = postResponses[path];
    postResponses[path] = { status: 200, body: { ok: true } };
    const prior = fetchMock.getMockImplementation();
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if ((init?.method ?? "GET") === "POST" && url === path) await held;
      return prior?.(url, init) as Promise<Response>;
    });
    void original;
    return { release };
  }

  test("archiving a session hides its row immediately, before the request resolves", async () => {
    renderRail();
    await screen.findByText("Session one");
    const { release } = heldPost("/api/archive");

    const user = userEvent.setup();
    const row = rowFor("Session one");
    await user.click(within(row).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Archive" }));

    await vi.waitFor(() => expect(screen.queryByText("Session one")).toBeNull());
    release();
  });

  test("pinning a session shows its star immediately", async () => {
    renderRail();
    await screen.findByText("Session one");
    expect(within(rowFor("Session one")).queryByTestId("favorite-star")).toBeNull();
    const { release } = heldPost("/api/favorite");

    const user = userEvent.setup();
    await user.click(within(rowFor("Session one")).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));

    await vi.waitFor(() => expect(within(rowFor("Session one")).queryByTestId("favorite-star")).toBeTruthy());
    release();
  });

  test("renaming a session shows the new title immediately", async () => {
    renderRail();
    await screen.findByText("Session one");
    const { release } = heldPost("/api/sessions/local%3As1/rename");

    const user = userEvent.setup();
    await user.click(within(rowFor("Session one")).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    const input = screen.getByRole("textbox");
    await user.clear(input);
    await user.type(input, "Brand new name");
    await user.click(screen.getByRole("button", { name: "Rename" }));

    await vi.waitFor(() => expect(screen.getByText("Brand new name")).toBeTruthy());
    release();
  });

  // The overlay is a guess about what the server will do. When the server
  // says no, the guess has to come off - otherwise a row the user still owns
  // stays invisible until they reload.
  test("a rejected archive puts the row back", async () => {
    postResponses["/api/archive"] = { status: 500, body: { error: "archive store error: boom" } };
    renderRail();
    await screen.findByText("Session one");

    const user = userEvent.setup();
    await user.click(within(rowFor("Session one")).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Archive" }));

    await screen.findByText(/archive store error: boom/i);
    expect(screen.getByText("Session one")).toBeTruthy();
  });

  // Unarchive has no optimistic form on purpose: which tier the row lands in
  // is a server-side classification this layer cannot predict, so guessing
  // would mean showing it in the wrong place. Matches the htmx behavior.
  test("unarchiving does not optimistically move anything", async () => {
    treeResponseBody = WIRE_TREE_WITH_ARCHIVED_SESSION;
    renderRail();
    await screen.findByText("Session one");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /archived sessions/i }));
    const sec = screen.getByRole("button", { name: /archived sessions/i }).closest("section");
    if (!sec) throw new Error("no archived section");
    const { release } = heldPost("/api/archive");
    const sub = within(sec).getByRole("treeitem", { name: /prime-radiant/ });
    await user.click(within(sub).getByTestId("rail-chevron"));
    const archivedRow = within(sec).getByText("Old archived session").closest('[role="treeitem"]');
    if (!(archivedRow instanceof HTMLElement)) throw new Error("no archived row");
    await user.click(within(archivedRow).getByRole("button", { name: /actions for/i }));
    await user.click(screen.getByRole("menuitem", { name: "Unarchive" }));

    expect(within(sec).getByText("Old archived session")).toBeTruthy();
    release();
  });
});

describe("favorite action", () => {
  test("toggling favorite on a session POSTs /api/favorite with the exact body and refetches the tree on success", async () => {
    renderRail();
    await screen.findByText("Session one");
    expect(treeGetCallCount()).toBe(1);

    const user = userEvent.setup();
    const row = rowFor("Session one");
    await user.click(within(row).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));

    expect(postCalls).toContainEqual({
      path: "/api/favorite",
      body: { kind: "session", id: "local:s1", favorited: true },
    });
    await vi.waitFor(() => expect(treeGetCallCount()).toBe(2));
  });

  test("a failed favorite toggle shows an error toast and does not refetch", async () => {
    postResponses["/api/favorite"] = { status: 500, body: { error: "favorite store error: boom" } };
    renderRail();
    await screen.findByText("Session one");

    const user = userEvent.setup();
    const row = rowFor("Session one");
    await user.click(within(row).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));

    await screen.findByText(/favorite store error: boom/i);
    expect(treeGetCallCount()).toBe(1); // no refetch on failure
  });

  // Project rows gain pin/unpin the same way session rows already have -
  // TreeProject.Favorite (the hub's own tree-wire gaps round) is what makes
  // this possible; see actions.ts's setFavorite, generic across both kinds
  // already.
  test("toggling favorite on a project POSTs /api/favorite with kind:project and refetches on success", async () => {
    renderRail();
    await screen.findByText("prime-radiant");
    expect(treeGetCallCount()).toBe(1);

    const user = userEvent.setup();
    const row = rowFor("prime-radiant");
    await user.click(within(row).getByRole("button", { name: /actions for prime-radiant/i }));
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));

    expect(postCalls).toContainEqual({
      path: "/api/favorite",
      body: { kind: "project", id: "proj1", favorited: true },
    });
    await vi.waitFor(() => expect(treeGetCallCount()).toBe(2));
  });
});

describe("archive action", () => {
  test("toggling archive on a project POSTs /api/archive with working_dir and refetches on success", async () => {
    renderRail();
    await screen.findByText("prime-radiant");

    const user = userEvent.setup();
    const row = rowFor("prime-radiant");
    await user.click(within(row).getByRole("button", { name: /actions for prime-radiant/i }));
    await user.click(screen.getByRole("menuitem", { name: "Archive project" }));

    expect(postCalls).toContainEqual({
      path: "/api/archive",
      body: { kind: "project", id: "proj1", archived: true, working_dir: "/home/user/prime-radiant" },
    });
    await vi.waitFor(() => expect(treeGetCallCount()).toBe(2));
  });
});

describe("rename flow", () => {
  test("opens a dialog prefilled with the current title; confirming POSTs the rename and refetches", async () => {
    renderRail();
    await screen.findByText("Session one");

    const user = userEvent.setup();
    const row = rowFor("Session one");
    await user.click(within(row).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));

    const input = await screen.findByDisplayValue("Session one");
    await user.clear(input);
    await user.type(input, "Renamed session");
    await user.click(screen.getByRole("button", { name: "Rename" }));

    expect(postCalls).toContainEqual({ path: "/api/sessions/local%3As1/rename", body: { name: "Renamed session" } });
    await vi.waitFor(() => expect(treeGetCallCount()).toBe(2));
    expect(screen.queryByDisplayValue("Renamed session")).toBeNull(); // dialog closed
  });

  test("canceling the rename dialog does not POST anything", async () => {
    renderRail();
    await screen.findByText("Session one");

    const user = userEvent.setup();
    const row = rowFor("Session one");
    await user.click(within(row).getByRole("button", { name: /actions for session one/i }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    await screen.findByDisplayValue("Session one");

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(postCalls).toHaveLength(0);
    expect(screen.queryByDisplayValue("Session one")).toBeNull();
  });
});

describe("delete project flow", () => {
  test("opens a confirmation dialog; confirming POSTs the delete and refetches", async () => {
    renderRail();
    await screen.findByText("Live session");
    await userEvent.setup().click(screen.getByRole("button", { name: /archived/i }));
    await screen.findByText("old-project");

    const user = userEvent.setup();
    const row = rowFor("old-project");
    await user.click(within(row).getByRole("button", { name: /actions for old-project/i }));
    await user.click(screen.getByRole("menuitem", { name: "Delete project…" }));

    await screen.findByRole("heading", { name: "Delete project?" });
    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(postCalls).toContainEqual({
      path: "/api/project/delete",
      body: { key: "archproj", working_dir: "/home/user/old-project" },
    });
    await vi.waitFor(() => expect(treeGetCallCount()).toBe(2));
  });

  test("canceling the delete confirmation does not POST anything", async () => {
    renderRail();
    await screen.findByText("Live session");
    await userEvent.setup().click(screen.getByRole("button", { name: /archived/i }));
    await screen.findByText("old-project");

    const user = userEvent.setup();
    const row = rowFor("old-project");
    await user.click(within(row).getByRole("button", { name: /actions for old-project/i }));
    await user.click(screen.getByRole("menuitem", { name: "Delete project…" }));
    await screen.findByRole("heading", { name: "Delete project?" });

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(postCalls).toHaveLength(0);
  });

  test("a delete that partially skips sessions still refetches but also shows a warning toast", async () => {
    postResponses["/api/project/delete"] = {
      status: 200,
      body: { deleted: [], skipped: [{ id: "local:old1", reason: "resumed live" }] },
    };
    renderRail();
    await screen.findByText("Live session");
    await userEvent.setup().click(screen.getByRole("button", { name: /archived/i }));
    await screen.findByText("old-project");

    const user = userEvent.setup();
    const row = rowFor("old-project");
    await user.click(within(row).getByRole("button", { name: /actions for old-project/i }));
    await user.click(screen.getByRole("menuitem", { name: "Delete project…" }));
    await screen.findByRole("button", { name: "Delete" });
    await user.click(screen.getByRole("button", { name: "Delete" }));

    await screen.findByText(/could not be removed/i);
    await vi.waitFor(() => expect(treeGetCallCount()).toBe(2));
  });
});

describe("reveal (railController /project)", () => {
  const REVEAL_SESSION = wireNode({
    row_id: "project:revealp:local:rs1",
    ref: "local:rs1",
    title: "Reveal target session",
  });
  const COLLAPSED_PROJECT = wireProject({
    key: "revealp",
    name: "reveal-project",
    default_expanded: false, // starts collapsed: reveal must un-collapse it
    sessions: [REVEAL_SESSION],
  });
  // A top-level-tier entry (no project to expand) that is always visible -
  // the auto-grouped "Needs you" tier this test previously targeted (vbh8)
  // no longer renders as its own section, so "Live" (still retained per
  // Jesse's decision - see Rail.tsx's own comment) stands in for "a
  // top-level tier session with nothing to expand."
  const REVEAL_LIVE_NODE = wireNode({
    row_id: "live:local:live-reveal",
    ref: "local:live-reveal",
    title: "Reveal live session",
    state: "active",
  });
  const REVEAL_TREE = {
    ...EMPTY_WIRE_TREE,
    live: [REVEAL_LIVE_NODE],
    projects: [COLLAPSED_PROJECT],
    attentionSummary: { needsYou: 0, error: 0, working: 1 },
  };

  // jsdom implements no scrollIntoView at all (verified: the property is
  // absent, so vi.spyOn can't wrap it); assign a fresh spy each test so the
  // reveal effect's call is observable and doesn't throw.
  let scrollSpy: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    scrollSpy = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollSpy as unknown as typeof HTMLElement.prototype.scrollIntoView;
  });
  afterEach(() => {
    // @ts-expect-error restore jsdom's honest absence of scrollIntoView
    delete HTMLElement.prototype.scrollIntoView;
  });

  function renderRailWith(props: { revealTarget?: string | null; onRevealConsumed?: () => void }) {
    return render(
      <>
        <Rail revealTarget={props.revealTarget} onRevealConsumed={props.onRevealConsumed} />
        <Toast />
      </>,
    );
  }

  test("expands a collapsed project and scrolls the target session's row into view (block:center)", async () => {
    treeResponseBody = REVEAL_TREE;
    const onRevealConsumed = vi.fn();

    renderRailWith({ revealTarget: "local:rs1", onRevealConsumed });

    // The session starts hidden (project collapsed); reveal un-collapses it.
    const row = await screen.findByText("Reveal target session");
    await vi.waitFor(() => expect(scrollSpy).toHaveBeenCalled());
    expect(scrollSpy.mock.calls[0]?.[0]).toMatchObject({ block: "center" });
    // The scrolled element is the target session's own row.
    expect(row.closest("[data-session-ref]")?.getAttribute("data-session-ref")).toBe("local:rs1");
    await vi.waitFor(() => expect(onRevealConsumed).toHaveBeenCalledTimes(1));
  });

  test("scrolls an already-visible top-level tier session without any expand", async () => {
    treeResponseBody = REVEAL_TREE;
    const onRevealConsumed = vi.fn();

    renderRailWith({ revealTarget: "local:live-reveal", onRevealConsumed }); // REVEAL_LIVE_NODE, always shown

    await screen.findByText("Reveal live session");
    await vi.waitFor(() => expect(scrollSpy).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(onRevealConsumed).toHaveBeenCalledTimes(1));
  });

  test("consumes without scrolling when the ref belongs to no rendered session", async () => {
    treeResponseBody = REVEAL_TREE;
    const onRevealConsumed = vi.fn();

    renderRailWith({ revealTarget: "local:ghost", onRevealConsumed });

    // The tree must have loaded before the effect gives up (it waits for it).
    await screen.findByText("Reveal live session");
    await vi.waitFor(() => expect(onRevealConsumed).toHaveBeenCalled());
    expect(scrollSpy).not.toHaveBeenCalled();
  });

  test("a null reveal target does nothing", async () => {
    treeResponseBody = REVEAL_TREE;
    const onRevealConsumed = vi.fn();

    renderRailWith({ revealTarget: null, onRevealConsumed });

    await screen.findByText("Reveal live session");
    expect(scrollSpy).not.toHaveBeenCalled();
    expect(onRevealConsumed).not.toHaveBeenCalled();
  });
});
