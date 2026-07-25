import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import { Tree, type TreeRowInfo } from "../../widgets";
import { activityGloss, cadenceStateFor, RailRow, type RailRowActions } from "./RailRow";
import type { LoadingRailNode, ProjectRailNode, SessionRailNode } from "./railNodes";

afterEach(cleanup);

function apiNode(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "project:p1:local:a",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Fix flaky test",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

function apiProject(overrides: Partial<ApiTreeProject> = {}): ApiTreeProject {
  return { key: "p1", name: "Proj", sessions: [], ...overrides };
}

function sessionRailNode(session: ApiTreeNode, overrides: Partial<SessionRailNode> = {}): SessionRailNode {
  return { id: session.row_id, kind: "session", session, expanded: false, children: [], ...overrides };
}

function projectRailNode(project: ApiTreeProject, children: ProjectRailNode["children"] = []): ProjectRailNode {
  return { id: `projectnode:${project.key}`, kind: "project", project, expanded: false, children };
}

function loadingRailNode(): LoadingRailNode {
  return { id: "loading-1", kind: "loading" };
}

function info(overrides: Partial<TreeRowInfo> = {}): TreeRowInfo {
  return { depth: 0, expanded: false, hasChildren: false, toggle: vi.fn(), activate: vi.fn(), ...overrides };
}

function actions(overrides: Partial<RailRowActions> = {}): RailRowActions {
  return {
    onToggleFavorite: vi.fn(),
    onToggleArchiveSession: vi.fn(),
    onRenameRequest: vi.fn(),
    onToggleFavoriteProject: vi.fn(),
    onToggleArchiveProject: vi.fn(),
    onDeleteProjectRequest: vi.fn(),
    ...overrides,
  };
}

async function openMenu(name: RegExp | string) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name }));
  return user;
}

describe("cadenceStateFor", () => {
  test.each([
    ["errored", "failed"],
    ["awaiting", "needs-you"],
    ["active", "working"],
    ["warning", "needs-you"],
    ["idle", "idle"],
    ["ended", "ended"],
    ["notLoaded", "idle"],
    ["", "idle"],
    ["some-unknown-future-state", "idle"],
  ] as const)("maps wire state %s to Cadence state %s", (wireState, expected) => {
    expect(cadenceStateFor(wireState)).toBe(expected);
  });
});

describe("activityGloss", () => {
  test("states the humanized state alone when the session carries no other metadata", () => {
    expect(activityGloss(apiNode({ state: "active" }))).toBe("working");
  });

  test("joins state, branch, tier and model in that order", () => {
    const session = apiNode({ state: "awaiting", branch: "main", tier: "archived", model: "opus" });
    expect(activityGloss(session)).toBe("waiting on you · main · archived · opus");
  });

  test("omits an empty branch and the unremarkable 'current' tier", () => {
    expect(activityGloss(apiNode({ state: "idle", branch: "", tier: "current", model: "opus" }))).toBe("idle · opus");
  });
});

describe("loading row", () => {
  test("renders a non-interactive loading indicator", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByText(/loading/i)).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  test("announces itself via role=status, like the top-level Skeleton", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByRole("status").textContent).toMatch(/loading/i);
  });
});

describe("session row", () => {
  test("renders the session's title and a Cadence reflecting its state", () => {
    const session = apiNode({ title: "Fix flaky test", state: "active" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByText("Fix flaky test")).toBeTruthy();
    // Cadence's wrapper carries the state as its accessible name (see
    // widgets/cadence) - "Working" is the family "active" maps to.
    expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
  });

  test("clicking the label activates the row via info.activate", async () => {
    const rowInfo = info();
    render(<RailRow node={sessionRailNode(apiNode())} info={rowInfo} actions={actions()} />);
    await userEvent.setup().click(screen.getByText("Fix flaky test"));
    expect(rowInfo.activate).toHaveBeenCalledTimes(1);
  });

  test("shows no chevron for a leaf session (info.hasChildren false)", () => {
    render(<RailRow node={sessionRailNode(apiNode())} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByTestId("rail-chevron")).toBeNull();
  });

  test("shows a chevron for a branch session (subagent cluster) that calls info.toggle", async () => {
    const rowInfo = info({ hasChildren: true, expanded: false });
    render(<RailRow node={sessionRailNode(apiNode())} info={rowInfo} actions={actions()} />);
    // The chevron is deliberately aria-hidden (decorative mouse shortcut -
    // see widgets/tree's own doc comment and RailRow.tsx's Chevron), so
    // it's found by test id rather than an accessible role query.
    await userEvent.setup().click(screen.getByTestId("rail-chevron"));
    expect(rowInfo.toggle).toHaveBeenCalledTimes(1);
  });

  test("shows a favorite star when the session is favorited, hides it otherwise", () => {
    const { rerender } = render(
      <RailRow node={sessionRailNode(apiNode({ favorite: true }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();

    rerender(<RailRow node={sessionRailNode(apiNode({ favorite: false }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("favorite-star")).toBeNull();
  });

  // vbh8/§2.2: a derived amber count of needs-you descendants - distinct
  // from the row's own Cadence dot (which already goes amber when the
  // SESSION ITSELF needs you - see cadenceStateFor above). A leaf session
  // with no needs-you descendants shows no badge at all, even if it needs
  // you itself - the dot alone covers that case, so a redundant "0"/"1"
  // badge would double up on the same signal.
  test("shows a derived needs-you-descendant count Badge when a child needs you", () => {
    const session = apiNode({
      state: "active",
      children: [{ ...apiNode({ row_id: "child", ref: "local:child", state: "awaiting" }) }],
    });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByText("1")).toBeTruthy();
  });

  test("shows no Badge for a leaf session that itself needs you (the Cadence dot already covers it)", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "awaiting" }))} info={info()} actions={actions()} />);
    expect(screen.queryByText("1")).toBeNull();
    expect(screen.queryByText("0")).toBeNull();
  });

  // vbh8 new capability, §2.3: row anatomy for the (already-existing)
  // subagent tree - a humanized second activity line, and a right-aligned
  // relative timestamp OR the Task-7 Badge, whichever slot applies.
  test("shows a humanized activity line and a relative timestamp", () => {
    const session = apiNode({ state: "active", age: "2m" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/working/i);
    expect(screen.getByTestId("rail-row-time").textContent).toBe("2m");
  });

  test.each([
    ["active", /working/i],
    ["awaiting", /waiting on you/i],
    ["warning", /waiting on you/i],
    ["errored", /failed/i],
    ["ended", /ended/i],
    ["idle", /idle/i],
    ["notLoaded", /idle/i],
  ] as const)("humanizes wire state %s as %s in the activity line", (state, expected) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(expected);
  });

  test("appends the model name to the activity line when present", () => {
    const session = apiNode({ state: "active", model: "opus" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/working.*opus/i);
  });

  test("a needs-you count takes the right slot instead of the timestamp", () => {
    const session = apiNode({
      state: "active",
      age: "2m",
      children: [apiNode({ row_id: "child", ref: "local:child", state: "awaiting" })],
    });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
    expect(screen.getByText("1")).toBeTruthy(); // the Badge from Task 7
  });

  test("shows no timestamp when the session carries no age", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", age: undefined }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
  });

  test("menu offers 'Add to pinned' for an unfavorited session and calls onToggleFavorite on select", async () => {
    const acts = actions();
    const session = apiNode({ favorite: false, ref: "local:a" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(acts.onToggleFavorite).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Remove from pinned' for a favorited session", async () => {
    const session = apiNode({ favorite: true });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Remove from pinned" })).toBeTruthy();
  });

  test("menu omits Rename when the session does not support it", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ rename: false }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
  });

  test("menu offers Rename when the session supports it, and calls onRenameRequest on select", async () => {
    const acts = actions();
    const session = apiNode({ rename: true });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(acts.onRenameRequest).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Archive' for a session outside the archived tier, and calls onToggleArchiveSession", async () => {
    const acts = actions();
    const session = apiNode({ tier: "current" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Archive" }));
    expect(acts.onToggleArchiveSession).toHaveBeenCalledWith(session);
  });

  test("menu offers 'Unarchive' for a session already in the archived tier", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ tier: "archived" }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unarchive" })).toBeTruthy();
  });

  test("shows the tier as a secondary label when it isn't 'current'", () => {
    render(<RailRow node={sessionRailNode(apiNode({ tier: "recent" }))} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/recent/);
  });

  // Title-first row (rail truncation round): branch/tier are secondary
  // metadata that used to sit in the row's main line as flex:none siblings,
  // so at the rail's 280px they took their width off the top of the ONE
  // thing that identifies a row. They now ride the second line, which
  // ellipsizes on its own; the title keeps the whole main line minus the
  // (short, fixed) age.
  test("keeps branch and tier out of the title's line, on the activity line instead", () => {
    const session = apiNode({ state: "active", branch: "main", tier: "archived", age: "47m" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);

    const activity = screen.getByTestId("rail-row-activity");
    expect(activity.textContent).toMatch(/main/);
    expect(activity.textContent).toMatch(/archived/);
    // The row's main line holds the title and the age, and nothing else
    // that reserves width: every other text node lives on line two.
    const title = screen.getByText("Fix flaky test");
    const mainLine = title.parentElement?.parentElement;
    expect(mainLine).toBeTruthy();
    const mainLineText = [...(mainLine?.children ?? [])]
      .filter((child) => child !== title.parentElement)
      .map((child) => child.textContent)
      .join(" ");
    expect(mainLineText).not.toMatch(/main/);
    expect(mainLineText).not.toMatch(/archived/);
    expect(screen.getByTestId("rail-row-time").textContent).toBe("47m");
  });

  // vitest leaves CSS Modules unprocessed (vite.config.ts sets no test.css),
  // so the rule that actually keeps a long gloss from wrapping the row to a
  // third line is only checkable against the stylesheet text - the same way
  // StackHost.test.tsx / radiogroup.test.tsx pin their own layout-critical
  // declarations.
  test("the activity line's stylesheet rule ellipsizes rather than wraps, so metadata never grows the row", () => {
    const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "Rail.module.css"), "utf8");
    const activityRule = /\.activity\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(activityRule).toMatch(/white-space:\s*nowrap/);
    expect(activityRule).toMatch(/text-overflow:\s*ellipsis/);
    expect(activityRule).toMatch(/overflow:\s*hidden/);
  });

  test("a truncated title stays readable via a hover tooltip", () => {
    const long = "It looks like a lot of the sidebar rows are truncating their titles";
    render(<RailRow node={sessionRailNode(apiNode({ title: long }))} info={info()} actions={actions()} />);
    expect(screen.getByText(long).getAttribute("title")).toBe(long);
  });

  test("the activity line carries its own full text as a tooltip, since it ellipsizes too", () => {
    const session = apiNode({ state: "active", model: "opus", branch: "feature/long-branch-name" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    const activity = screen.getByTestId("rail-row-activity");
    expect(activity.getAttribute("title")).toBe(activity.textContent);
  });

  // hub tree-wire gaps round (wave 3 task 6): a live-tier row's own
  // Tier/Favorite/Rename fields used to arrive unstamped (undefined/false)
  // regardless of the session's real decisions, since handleAPITree's Live
  // loop bypassed the tier-stamping helper entirely. RailRow never gated
  // these on tier itself - it just reads session.favorite/session.rename
  // directly, same as every other row - so once the hub fix landed, this
  // was already correct with no rail-side code change; pinned explicitly
  // here (rather than left to incidental coverage from fixtures that never
  // set tier at all) since a live row is the realistic shape a reviewer
  // would specifically want proof for.
  test("favorite star and Rename affordance both work on a live-tier row, not just current/archived ones", async () => {
    const acts = actions();
    const session = apiNode({ tier: "live", favorite: true, rename: true });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);

    expect(screen.getByTestId("favorite-star")).toBeTruthy();
    const user = await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Remove from pinned" })).toBeTruthy();
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(acts.onRenameRequest).toHaveBeenCalledWith(session);
  });
});

describe("project row", () => {
  test("renders the project's name and a Cadence reflecting its rollup state", () => {
    const project = apiProject({ name: "prime-radiant", rollup_state: "errored" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
  });

  test("shows an attention Badge when rollup_attn is nonzero, hides it when zero", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ rollup_attn: 3 }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByText("3")).toBeTruthy();

    rerender(<RailRow node={projectRailNode(apiProject({ rollup_attn: 0 }))} info={info()} actions={actions()} />);
    expect(screen.queryByText("0")).toBeNull();
  });

  test("menu offers 'Archive project' for an active project and calls onToggleArchiveProject", async () => {
    const acts = actions();
    const project = apiProject({ is_archived: false });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Archive project" }));
    expect(acts.onToggleArchiveProject).toHaveBeenCalledWith(project);
  });

  test("menu offers 'Unarchive project' for an already-archived project", async () => {
    render(<RailRow node={projectRailNode(apiProject({ is_archived: true }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unarchive project" })).toBeTruthy();
  });

  test("menu offers 'Delete project…' and calls onDeleteProjectRequest on select", async () => {
    const acts = actions();
    const project = apiProject();
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Delete project…" }));
    expect(acts.onDeleteProjectRequest).toHaveBeenCalledWith(project);
  });

  test("menu never offers Rename for a project row - only sessions can be renamed", async () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
  });

  test("shows a favorite star when the project is favorited, hides it otherwise", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ favorite: true }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();

    rerender(<RailRow node={projectRailNode(apiProject({ favorite: false }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("favorite-star")).toBeNull();
  });

  test("menu offers 'Add to pinned' for an unfavorited project and calls onToggleFavoriteProject on select", async () => {
    const acts = actions();
    const project = apiProject({ favorite: false, key: "p1" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(acts.onToggleFavoriteProject).toHaveBeenCalledWith(project);
  });

  test("menu offers 'Remove from pinned' for a favorited project", async () => {
    render(<RailRow node={projectRailNode(apiProject({ favorite: true }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Remove from pinned" })).toBeTruthy();
  });

  test("the synthetic '(no project)' bucket gets no actions menu at all - archive/delete always 400 for it server-side", () => {
    render(
      <RailRow
        node={projectRailNode(apiProject({ key: "no-project", name: "(no project)" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.queryByRole("button")).toBeNull();
  });
});

// --- fix-wave: nested Menu triggers must not corrupt Tree's roving
// tabindex (Important) -----------------------------------------------
//
// These render RailRow through a REAL Tree (not the hand-built `info`
// double every other test in this file uses) - the bug this covers is
// specifically about Tree's own keyboard/focus machinery interacting with
// RailRow's content, which a fake `info` object can't exercise.
describe("roving-tabindex integration (Tree + RailRow)", () => {
  function twoSessionRows(): [SessionRailNode, SessionRailNode] {
    return [
      sessionRailNode(apiNode({ row_id: "rowA", ref: "local:a", title: "Row A" })),
      sessionRailNode(apiNode({ row_id: "rowB", ref: "local:b", title: "Row B" })),
    ];
  }

  function renderTree(nodes: SessionRailNode[]) {
    return render(
      <Tree
        nodes={nodes}
        onToggle={() => {}}
        onActivate={() => {}}
        renderRow={(node, rowInfo) => <RailRow node={node} info={rowInfo} actions={actions()} />}
      />,
    );
  }

  test("only the roving treeitem is a Tab stop - neither row's actions trigger is", () => {
    renderTree(twoSessionRows());
    const treeitems = screen.getAllByRole("treeitem");
    expect(treeitems.map((el) => el.tabIndex)).toEqual([0, -1]); // Row A (first) starts as the roving one

    const triggers = screen.getAllByRole("button", { name: /actions for/i });
    expect(triggers).toHaveLength(2);
    for (const trigger of triggers) expect(trigger.tabIndex).toBe(-1);
  });

  test("Tab from before the tree lands on the roving treeitem, never a row's own trigger", async () => {
    render(
      <>
        <button type="button">Before</button>
        <Tree
          nodes={twoSessionRows()}
          onToggle={() => {}}
          onActivate={() => {}}
          renderRow={(node, rowInfo) => <RailRow node={node} info={rowInfo} actions={actions()} />}
        />
      </>,
    );
    const user = userEvent.setup();
    act(() => screen.getByRole("button", { name: "Before" }).focus());
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole("treeitem", { name: /Row A/ }));
  });

  test("ArrowDown on a row's trigger opens the menu; the tree's roving tabindex survives closing it again (post-Escape corruption probe)", () => {
    // Reproduces the reviewer's exact probe, inverted. The corruption is
    // NOT visible right after opening - FocusScope's own mount effect
    // (widgets/focusscope/index.tsx) captures document.activeElement as
    // its restore target, THEN focuses the popup's first item; that
    // second focus move bubbles back up to Row A's own treeitem (the
    // popup is rendered INSIDE it) and reasserts currentId="rowA" as a
    // side effect, momentarily masking the bug. But WITHOUT
    // stopPropagation, Tree's own moveTo("rowB") already ran (and moved
    // real DOM focus to Row B's treeitem) BEFORE that effect captured its
    // restore target - so the restore target FocusScope captured is Row
    // B's treeitem, not Row A's trigger. Closing the menu (Escape unmounts
    // FocusScope, running its cleanup) restores focus to that stale
    // target: Row B, silently stealing the roving tabindex out from under
    // Row A even though the menu that just closed belonged to Row A.
    renderTree(twoSessionRows());
    const rowATreeitem = screen.getByRole("treeitem", { name: /Row A/ });
    const rowBTreeitem = screen.getByRole("treeitem", { name: /Row B/ });
    const rowATrigger = within(rowATreeitem).getByRole("button", { name: /actions for/i });

    act(() => rowATrigger.focus());
    fireEvent.keyDown(rowATrigger, { key: "ArrowDown" });
    expect(screen.getByRole("menu")).toBeTruthy(); // the menu still opens

    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull(); // closed

    expect(rowATreeitem.tabIndex).toBe(0); // still Row A's roving tabindex...
    expect(rowBTreeitem.tabIndex).toBe(-1); // ...not silently moved to Row B
    expect(document.activeElement).toBe(rowATrigger); // and focus is back on Row A's own trigger
  });
});
