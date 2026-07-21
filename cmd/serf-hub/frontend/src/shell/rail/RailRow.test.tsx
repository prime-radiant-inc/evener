import { afterEach, describe, expect, test, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { TreeRowInfo } from "../../widgets";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import type { LoadingRailNode, ProjectRailNode, SessionRailNode } from "./railNodes";
import { cadenceStateFor, RailRow, type RailRowActions } from "./RailRow";

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

describe("loading row", () => {
  test("renders a non-interactive loading indicator", () => {
    render(<RailRow node={loadingRailNode()} info={info()} actions={actions()} />);
    expect(screen.getByText(/loading/i)).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("session row", () => {
  test("renders the session's title and a Cadence reflecting its state", () => {
    const session = apiNode({ title: "Fix flaky test", state: "active" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByText("Fix flaky test")).toBeTruthy();
    // Cadence's own <title> encodes the state label (see widgets/cadence) -
    // "Working" is the family "active" maps to.
    expect(screen.getByRole("img").textContent).toBe("Working");
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
    expect(screen.getByText("recent")).toBeTruthy();
  });
});

describe("project row", () => {
  test("renders the project's name and a Cadence reflecting its rollup state", () => {
    const project = apiProject({ name: "prime-radiant", rollup_state: "errored" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
    expect(screen.getByRole("img").textContent).toBe("Failed");
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

  test("menu never offers Favorite or Rename for a project row (not supported by the wire shape)", async () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: /pinned/i })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
  });

  test("the synthetic '(no project)' bucket gets no actions menu at all - archive/delete always 400 for it server-side", () => {
    render(<RailRow node={projectRailNode(apiProject({ key: "no-project", name: "(no project)" }))} info={info()} actions={actions()} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
