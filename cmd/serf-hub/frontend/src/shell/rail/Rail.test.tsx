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

  test("omits the Archived disclosure entirely when there are no archived projects", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...SAMPLE_WIRE_TREE, archived_projects: [] }));
    renderRail();
    await screen.findByText("Live session");
    expect(screen.queryByRole("button", { name: /archived/i })).toBeNull();
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
    expect(screen.getByText("⌘K")).toBeTruthy();
    expect(screen.getByRole("button", { name: /new session/i })).toBeTruthy();
    expect(screen.getByTestId("rail-settings")).toBeTruthy();
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
