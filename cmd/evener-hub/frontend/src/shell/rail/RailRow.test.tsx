import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { sessionPanelPaneType } from "../../panes/sessionPanels";
import { type NormalizedResource, normalizedGraphFromSnapshot } from "../../stores/navigation/codec";
import { selectRailModel } from "../../stores/navigation/selectors";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import {
  keyID,
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
  type ResourceState,
} from "../../stores/navigation/types";
import { Tree, type TreeRowInfo } from "../../widgets";
import { registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { activityGloss, cadenceStateFor, RailRow, type RailRowActions } from "./RailRow";
import railStyles from "./RailRow.module.css";
import type {
  CompletedJobsFoldRailNode,
  InactiveFoldRailNode,
  JobRailNode,
  LoadingRailNode,
  OverflowRailNode,
  ProjectRailNode,
  RailProject,
  RailSession,
  SessionRailNode,
} from "./railNodes";
import { RailRenderObserver } from "./railRenderObserver";

// "Pin this session…" mounts the real PinSectionPicker, which reads
// pin sections from the navigation store's bounded pin-catalog resource
// (loadPinCatalogPages + selectPinSections). Seed the store with a pin_catalog resource and
// stub loadPinCatalogPages so the picker's mount effect resolves without a
// real network fetch.
const pinKey = { kind: "pin_catalog" as const, offset: 0, limit: 100 };
const generation = "generation_test";

type LoadPinCatalogPages = (force?: boolean) => Promise<void>;

function seedPinCatalogForPicker(): void {
  const resource: ResourceState = {
    key: pinKey,
    data: {
      generation_id: generation,
      revision: 1,
      pin_sections: [{ id: "sec_1", name: "Client", count: 0 }],
      remaining: 0,
    },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "a",
    loading: false,
    stale: false,
    error: null,
    generationID: generation,
  };
  navigationStore.setState({ mode: "v2", resources: new Map([[keyID(resource.key), resource]]) });
  navigationStore.setState({ loadPinCatalogPages: vi.fn(async () => undefined) as LoadPinCatalogPages });
}

function PaneFixture() {
  return <div>pane</div>;
}

// paneRegistry.ts is a shared module singleton, not fresh per file - the
// afterAll below restores whatever each of these ids resolved to before
// this file ran, so a later file sharing the same module registry never
// inherits these fixtures in place of the real session/sessionTasks/
// sessionActivity/sessionDetails panes.
const restorePaneFixtures: Array<() => void> = [];

beforeAll(() => {
  // Minimal, test-only pane registrations (TreeDrawer.test.tsx's precedent):
  // the workspace store's openPane refuses an unregistered type, and the
  // unified menu's Details/Tasks/Activity items open real panes now.
  restorePaneFixtures.push(
    registerPaneForTests<{ ref: string }>({
      id: "session",
      title: () => "Session",
      component: lazy(() => Promise.resolve({ default: PaneFixture })),
    }),
  );
  for (const id of ["sessionTasks", "sessionActivity", "sessionDetails"] as const) {
    restorePaneFixtures.push(
      registerPaneForTests<{ ref: string }>({
        id,
        title: () => id,
        component: lazy(() => Promise.resolve({ default: PaneFixture })),
      }),
    );
  }
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  seedPinCatalogForPicker();
});

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
});

function apiNode(overrides: Partial<RailSession> = {}): RailSession {
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

function apiProject(overrides: Partial<RailProject> = {}): RailProject {
  return {
    key: "p1",
    name: "Proj",
    sessions: [],
    more_current: 0,
    more_recent: 0,
    more_archived: 0,
    loaded: false,
    nextOffsets: {},
    ...overrides,
  };
}

function sessionRailNode(session: RailSession, overrides: Partial<SessionRailNode> = {}): SessionRailNode {
  return { id: session.row_id, kind: "session", session, expanded: false, children: [], ...overrides };
}

function projectRailNode(project: RailProject, children: ProjectRailNode["children"] = []): ProjectRailNode {
  return { id: `projectnode:${project.key}`, kind: "project", project, expanded: false, children };
}

function loadingRailNode(): LoadingRailNode {
  return { id: "loading-1", kind: "loading" };
}

function overflowRailNode(count: number): OverflowRailNode {
  return { id: "projectnode:p1:overflow", kind: "overflow", count, pages: [] };
}

function inactiveFoldRailNode(count: number): InactiveFoldRailNode {
  return { id: "inactive:parent", kind: "inactiveFold", count, expanded: false, children: [] };
}

function jobRailNode(overrides: Partial<JobRailNode["job"]> = {}): JobRailNode {
  return {
    id: "job:parent:job-1",
    kind: "job",
    job: { job_id: "job-1", job_type: "shell", status: "running", row_id: "job:parent:job-1", ...overrides },
    active: true,
    children: [],
  };
}

function completedJobsFoldRailNode(count: number): CompletedJobsFoldRailNode {
  return { id: "completed-jobs:parent", kind: "completedJobsFold", count, expanded: false, children: [] };
}

function info(overrides: Partial<TreeRowInfo> = {}): TreeRowInfo {
  return { depth: 0, expanded: false, hasChildren: false, toggle: vi.fn(), activate: vi.fn(), ...overrides };
}

function actions(overrides: Partial<RailRowActions> = {}): RailRowActions {
  return {
    onOpenSessionPane: vi.fn(),
    onRenameSession: vi.fn().mockResolvedValue(undefined),
    onShutdownSession: vi.fn().mockResolvedValue(undefined),
    onPinSession: vi.fn().mockResolvedValue(undefined),
    onUnpinRequest: vi.fn().mockResolvedValue(undefined),
    onToggleArchiveSession: vi.fn().mockResolvedValue(undefined),
    onDeleteSession: vi.fn().mockResolvedValue(undefined),
    onToggleFavoriteProject: vi.fn(),
    onToggleArchiveProject: vi.fn(),
    onDeleteProjectRequest: vi.fn(),
    ...overrides,
  };
}

// renderRow mounts a top-level local session row with the menu fully
// populated (renameable, live, deletable), the way the rail's own tiers
// would render it.
function renderRow(sessionOverrides: Partial<RailSession> = {}, acts: RailRowActions = actions()) {
  const session = apiNode({ rename: true, ...sessionOverrides });
  render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
  return session;
}

async function openMenu(name: RegExp | string) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name }));
  return user;
}

function normalizedRailResource(
  key: Extract<ResourceKey, { kind: "project_page" | "pin_section" }>,
  parentOverrides: Partial<RailSession> = {},
  childOverrides: Partial<RailSession> = {},
): NormalizedResource {
  const parentKey = `${navigationViewScope(key)}/entity/${"1".repeat(64)}`;
  const childKey = `${navigationViewScope(key)}/entity/${"2".repeat(64)}`;
  return {
    key,
    graph: normalizedGraphFromSnapshot({
      metadata: {},
      entities: [
        {
          key: parentKey,
          kind: "session",
          value: apiNode({ ref: "parent", title: "Parent", children: [], ...parentOverrides }),
        },
        {
          key: childKey,
          kind: "session",
          value: apiNode({ ref: "child", title: "Child", children: [], ...childOverrides }),
        },
      ],
      containers: [
        {
          key: navigationRootContainerKey(key, "sessions"),
          owner: { kind: "resource_root", slot: "sessions" },
          children: [parentKey],
        },
        {
          key: navigationOwnedContainerKey(parentKey, "children"),
          owner: { kind: "entity", entityKey: parentKey, slot: "children" },
          children: [childKey],
        },
        {
          key: navigationOwnedContainerKey(childKey, "children"),
          owner: { kind: "entity", entityKey: childKey, slot: "children" },
          children: [],
        },
      ],
    }),
    version: { generationId: generation, revision: 1, etag: "v2" },
    presence: "present",
  };
}

test.each([
  [
    "archived project page",
    { kind: "project_page", projectKey: "project", tier: "archived", offset: 0, limit: 50 },
    { tier: "archived", project_key: "project" },
    "Unarchive",
  ],
  [
    "pinned section",
    { kind: "pin_section", sectionId: "research", offset: 0, limit: 50 },
    { pin_section_id: "research" },
    "Unpin",
  ],
] as const)(
  "normalized V2 %s preserves context through recursive rail projection and actions",
  async (_name, key, context, action) => {
    const resource = normalizedRailResource(key);
    const model = selectRailModel(resource);
    const parent = [...model.sessions.values()].find((session) => session.ref === "parent");
    const child = [...model.sessions.values()].find((session) => session.ref === "child");
    expect(parent).toMatchObject(context);
    expect(child).toMatchObject(context);
    if (!parent) throw new Error("expected projected parent session");

    const acts = actions();
    render(<RailRow node={sessionRailNode(parent)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: action }));
    if (action === "Unarchive") expect(acts.onToggleArchiveSession).toHaveBeenCalledWith(parent);
    else expect(acts.onUnpinRequest).toHaveBeenCalledWith(parent);
  },
);

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

// The signal dot (§ the dot earns its space): a row only shows a Cadence
// dot for a state that TELLS you something - working, waiting on you, failed. A
// quiet row shows no dot and, since the 2026-07-31 sidebar-density pass, holds
// no space for one either: the old always-reserved 6px gutter is gone, so a
// title's x-position now shifts one slot between quiet and signal rows -
// deliberate, matching how state already moves row height via the gloss line.
describe("signal gutter", () => {
  test.each([
    ["active", "Working"],
    ["awaiting", "Needs you"],
    ["warning", "Needs you"],
    ["errored", "Failed"],
  ] as const)("state %s shows a %s dot in the gutter", (state, label) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);
    const gutter = screen.getByTestId("rail-row-signal");
    expect(within(gutter).getByTestId("cadence-dot")).toBeTruthy();
    expect(within(gutter).getByRole("img", { name: label })).toBeTruthy();
  });

  test.each(["ended", "idle", "notLoaded", ""] as const)("state %s shows no dot and holds no slot", (state) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    // ...and the gutter itself is gone too - no space held for a dot that
    // is not there (the pre-density-pass behavior reserved it).
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("a project row's rollup state follows the same rule", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject({ rollup_state: "active" }))} info={info()} actions={actions()} />,
    );
    expect(within(screen.getByTestId("rail-row-signal")).getByTestId("cadence-dot")).toBeTruthy();

    rerender(
      <RailRow node={projectRailNode(apiProject({ rollup_state: "ended" }))} info={info()} actions={actions()} />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("a project row with no rollup state at all shows no dot and holds no slot", () => {
    render(<RailRow node={projectRailNode(apiProject())} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });
});

describe("activityGloss", () => {
  test("states the humanized state alone when the session carries no branch", () => {
    expect(activityGloss(apiNode({ state: "active" }))).toBe("working");
  });

  test("reports one recursive working subagent", () => {
    expect(
      activityGloss(
        apiNode({
          state: "active",
          children: [apiNode({ state: "idle", children: [apiNode({ state: "active" })] })],
        }),
      ),
    ).toBe("1 subagent working");
  });

  test("reports multiple recursive working subagents with plural wording", () => {
    expect(activityGloss(apiNode({ children: [apiNode({ state: "active" }), apiNode({ state: "active" })] }))).toBe(
      "2 subagents working",
    );
  });

  test("joins state and branch, in that order", () => {
    expect(activityGloss(apiNode({ state: "awaiting", branch: "main" }))).toBe("your move · main");
  });

  // A plain "your move" (turn ended, nothing further queued) and a real
  // blocked ask_user question both wire up as state "awaiting" - the ONLY
  // wire signal telling them apart is ask_pending. Reusing hubapi.StateWord's
  // own vocabulary (Track A §2 ask-tiering: "Question waiting" vs "Your
  // move") here is what lets a person scanning the rail tell "the agent is
  // blocked on my answer" from "the agent finished, read it when you like"
  // without opening every amber row - see k9-navigation's persona panel,
  // where every persona hit this exact wall.
  test("an ask_pending awaiting session glosses as a question, not a generic move", () => {
    expect(activityGloss(apiNode({ state: "awaiting", ask_pending: true }))).toBe("question waiting");
  });

  test("omits an empty branch", () => {
    expect(activityGloss(apiNode({ state: "idle", branch: "" }))).toBe("idle");
  });

  // kata 59mx: hubapi.StateWord already gives "warning" its own word
  // ("Warning"), distinct from either "awaiting" band - this gloss used to
  // fold it into the same "waiting on you" text a plain awaiting session
  // gets, even though the two wire states are not the same situation.
  test("glosses warning as its own word, not a generic waiting-on-you", () => {
    expect(activityGloss(apiNode({ state: "warning" }))).toBe("warning");
  });

  // The model is a property of the session, not a reason to look at it - and
  // the session pane's own status strip reports it the moment you open the row.
  // On a triage surface it was noise on every row.
  test("never carries the model, whatever the state", () => {
    expect(activityGloss(apiNode({ state: "active", branch: "main", model: "opus" }))).toBe("working · main");
  });

  // Tier isn't dropped, it's relocated: it survives in the row's title tooltip,
  // where a fact a title cannot carry stays reachable without spending a line.
  test("never carries the tier on the visible line", () => {
    expect(activityGloss(apiNode({ state: "errored", tier: "archived" }))).toBe("failed");
  });

  test("keeps a branch suffix after the working subagent count", () => {
    expect(
      activityGloss(
        apiNode({ branch: "fix/thing", children: [apiNode({ state: "active" }), apiNode({ state: "active" })] }),
      ),
    ).toBe("2 subagents working · fix/thing");
  });

  test("reports active jobs alongside active subagents", () => {
    const session = apiNode({ children: [apiNode({ state: "active" })] });
    Object.assign(session, {
      running_jobs: [{ job_id: "job-1", job_type: "shell", status: "running" }],
    });
    expect(activityGloss(session)).toBe("1 subagent working · 1 job running");
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

describe("overflow row", () => {
  test("says how many rows the server capped away", () => {
    render(<RailRow node={overflowRailNode(12)} info={info()} actions={actions()} />);
    expect(screen.getByText("+12 older")).toBeTruthy();
  });

  // Nothing to open and nothing to act on: the rows it counts were never sent
  // to the client, so a chevron or a menu would both be lies.
  test("offers nothing to click", () => {
    render(<RailRow node={overflowRailNode(3)} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("inactive-subagent fold row", () => {
  test("names what it hides and how many", () => {
    render(<RailRow node={inactiveFoldRailNode(3)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.getByText("Inactive subagents (3)")).toBeTruthy();
  });

  test("counts one in the singular", () => {
    render(<RailRow node={inactiveFoldRailNode(1)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.getByText("Inactive subagent (1)")).toBeTruthy();
  });

  // It stands for finished work, so it has no state to signal and nothing to
  // act on - the two things every session row spends its right edge on.
  test("carries no actions menu", () => {
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByRole("button", { name: /actions for/i })).toBeNull();
  });

  test("carries no cadence dot and no signal slot", () => {
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
  });

  test("toggles on click, the way its chevron does", async () => {
    const toggle = vi.fn();
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true, toggle })} actions={actions()} />);
    await userEvent.setup().click(screen.getByText("Inactive subagents (2)"));
    expect(toggle).toHaveBeenCalled();
  });

  // The fold's text lines up with every other row at its nesting depth: it
  // carries no alignment class of its own (the old .inactiveFold padding
  // overrides existed to left-justify a LEADING chevron at the parent's
  // label x - the chevron trails the label now, so there is nothing to
  // align but the text itself, which the shared .railRow rule already does).
  test("carries no alignment class - its text lines up with its depth's rows", () => {
    render(<RailRow node={inactiveFoldRailNode(2)} info={info({ hasChildren: true })} actions={actions()} />);
    expect(screen.getByTestId("rail-row-inactive-fold").className).toBe(railStyles.railRow as string);
  });

  test("the stylesheet carries no inactiveFold override at all", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(join(here, "RailRow.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    expect(css).not.toMatch(/\.inactiveFold/);
  });

  // The outdented-dot contract the whole list's alignment rests on: .railRow
  // reserves the leading padding the dot hangs in, and .signal's negative
  // margin exactly cancels the dot's own width (6px) plus the title line's
  // gap (--space-1), so a dotted row's title and a quiet row's title start
  // at the same x.
  test("the row reserves the dot's outdent padding", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(join(here, "RailRow.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    expect(css).toMatch(/\.railRow\s*\{[^}]*padding-left:\s*14px;/);
  });

  test("the signal dot outdents by exactly its own advance", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(join(here, "RailRow.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    expect(css).toMatch(/\.signal\s*\{[^}]*width:\s*6px;[^}]*margin-left:\s*-10px;/);
  });
});

describe("touch tap floor (RailRow.module.css, pointer: coarse)", () => {
  // shellguard's tap-target pass measures these in a real phone context; these
  // source assertions pin the rules themselves (jsdom evaluates no cascade).
  const CSS = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "RailRow.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );
  const coarseBlock = CSS.match(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/)?.[1] ?? null;

  test("row action buttons meet the 44px floor in BOTH dimensions", () => {
    expect(coarseBlock, "RailRow.module.css is missing its pointer:coarse block").not.toBeNull();
    const rule = coarseBlock!.match(/\.actions button\s*\{([^}]*)\}/);
    expect(rule).not.toBeNull();
    expect(rule![1]).toContain("min-width: var(--tap-min)");
    expect(rule![1]).toContain("min-height: var(--tap-min)");
  });

  test("the widened menu trigger centres its glyph instead of hugging an edge", () => {
    expect(coarseBlock, "RailRow.module.css is missing its pointer:coarse block").not.toBeNull();
    const rule = coarseBlock!.match(/\.actions button\[aria-haspopup="menu"\]\s*\{([^}]*)\}/);
    expect(rule).not.toBeNull();
    expect(rule![1]).toContain("padding: 0");
    expect(rule![1]).toContain("justify-content: center");
  });
});

describe("job rows", () => {
  test("renders an active job label and green status", () => {
    render(<RailRow node={jobRailNode({ command: "go test ./..." })} info={info()} actions={actions()} />);
    expect(screen.getByText("go test ./...")).toBeTruthy();
    expect(screen.getByTestId("rail-row-job-status").className.split(" ")).toContain(railStyles.activityAlive);
  });

  test("the tooltip shows the command and the tool call's intent", () => {
    render(
      <RailRow
        node={jobRailNode({
          command: "go test ./...",
          intent: "Running the package tests to find the failure",
        })}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTitle("go test ./... · Running the package tests to find the failure · running")).toBeTruthy();
  });

  test("the tooltip prefers the full command when the label was truncated", () => {
    const long = "echo a".repeat(200);
    render(
      <RailRow
        node={jobRailNode({
          command: "echo a…",
          full_command: long,
        })}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTitle(`${long} · running`)).toBeTruthy();
  });

  test("renders a separate completed-jobs disclosure", () => {
    const toggle = vi.fn();
    render(
      <RailRow node={completedJobsFoldRailNode(3)} info={info({ hasChildren: true, toggle })} actions={actions()} />,
    );
    expect(screen.getByText("Completed jobs (3)")).toBeTruthy();
    fireEvent.click(screen.getByText("Completed jobs (3)"));
    expect(toggle).toHaveBeenCalledTimes(1);
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

  // The chevron trails the title text, so a leaf row renders nothing for it
  // and reserves nothing either: unlike the old leading gutter (whose
  // conditional fill moved every title after it), a missing TRAILING chevron
  // leaves no hole - the label is the title line's first text either way.
  test("a leaf session renders no chevron and holds no slot for one", () => {
    render(<RailRow node={sessionRailNode(apiNode())} info={info({ hasChildren: false })} actions={actions()} />);
    expect(screen.queryByTestId("rail-chevron")).toBeNull();
    const label = screen.getByText("Fix flaky test");
    expect(label.nextElementSibling).toBeNull();
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

  // The title line's anatomy is signal-dot (outdented) then label then
  // trailing chevron, in that order, whether or not the row has children -
  // the chevron is simply absent on a leaf. This is what keeps one title
  // x-position across a mixed tree: the dot hangs in the row's leading
  // padding (see .signal in RailRow.module.css), so it participates in the DOM
  // order without shifting the label.
  test.each([true, false])(
    "the title line is signal-dot, label, then trailing chevron (hasChildren %s)",
    (hasChildren) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state: "active" }))}
          info={info({ hasChildren })}
          actions={actions()}
        />,
      );
      const signal = screen.getByTestId("rail-row-signal");
      const label = screen.getByText("Fix flaky test");
      const titleLine = signal.parentElement;
      expect(titleLine).toBeTruthy();
      const expected = hasChildren ? [signal, label, screen.getByTestId("rail-chevron")] : [signal, label];
      expect([...(titleLine?.children ?? [])]).toEqual(expected);
    },
  );

  test("a quiet row holds no signal slot; its title line leads with the label", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "idle" }))} info={info()} actions={actions()} />);
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
    const label = screen.getByText("Fix flaky test");
    expect(label.parentElement?.firstElementChild).toBe(label);
  });

  test("shows a pin star on a nested row with a section assignment, and hides it on flat Live/pinned rows", () => {
    // depth > 0: nested under its own project - the star is the only in-list
    // signal there that the session is pinned.
    const { rerender } = render(
      <RailRow
        node={sessionRailNode(apiNode({ pin_section_id: "research" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();

    // depth 0: the flat Live and named-pin-section tiers - being listed there
    // already says the session is pinned, so the star is pure redundancy.
    rerender(
      <RailRow
        node={sessionRailNode(apiNode({ pin_section_id: "research" }))}
        info={info({ depth: 0 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("favorite-star")).toBeNull();

    rerender(
      <RailRow
        node={sessionRailNode(apiNode({ pin_section_id: undefined }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
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
  // subagent tree - a right-aligned relative timestamp OR the Task-7 Badge,
  // whichever slot applies, plus (on a signal row) the gloss line.
  test("shows a humanized activity line and a relative timestamp", () => {
    const session = apiNode({ state: "active", age: "2m" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/working/i);
    expect(screen.getByTestId("rail-row-time").textContent).toBe("2m");
  });

  test("shows recursive working subagent count and preserves the branch suffix on a working row", () => {
    const session = apiNode({
      state: "active",
      branch: "fix/thing",
      children: [apiNode({ state: "active" }), apiNode({ state: "active" })],
    });
    render(<RailRow node={sessionRailNode(session)} info={info({ depth: 1 })} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("2 subagents working · fix/thing");
  });

  test("shows recursive working subagent count on a quiet row with active descendants", () => {
    const session = apiNode({
      state: "idle",
      branch: "fix/thing",
      age: "2m",
      children: [apiNode({ state: "idle", children: [apiNode({ state: "active" })] }), apiNode({ state: "active" })],
    });
    render(<RailRow node={sessionRailNode(session)} info={info({ depth: 1 })} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("2 subagents working · fix/thing");
  });

  test("shows an active job on a quiet session as green working activity", () => {
    const session = apiNode({ state: "idle" });
    Object.assign(session, {
      running_jobs: [{ job_id: "job-1", job_type: "shell", status: "running" }],
    });
    render(<RailRow node={sessionRailNode(session)} info={info({ depth: 1 })} actions={actions()} />);
    expect(screen.getByTestId("rail-row-signal")).toBeTruthy();
    expect(screen.getByTestId("rail-row-activity").className.split(" ")).toContain(railStyles.activityAlive);
  });

  test("keeps a quiet row without active descendants one line", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "2m" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("rail-row-activity")).toBeNull();
    expect(screen.getByTestId("rail-row-time").textContent).toBe("2m");
  });

  // --- the gloss line is a SIGNAL, not row furniture ---------------------
  //
  // The rail is a triage surface: who needs me, and nothing else. A quiet row's
  // empty signal gutter and grey age already say "nothing happening here", so a
  // second line restating "idle" in words put the same fact at two altitudes on
  // the one surface that exists to be skimmed. Only a signal state earns the
  // line - and it's the SAME predicate that earns the dot, so the two never
  // disagree about whether a row matters.
  //
  // kata hxjn is the one deliberate exception: a row at depth 0 (a top-level
  // entry in the flat, cross-project Live/Pinned tiers - see
  // SessionRow's own showsProject comment) gets a second line for its
  // project even when otherwise quiet, because that fact has nowhere else to
  // live on a flat list. A depth>0 row (nested under its own ProjectRow, or
  // a subagent child) is unaffected - `info()`'s own default is depth 0, so
  // the tests below that want the OLD one-line-quiet-row behavior pass
  // depth: 1 explicitly.

  test.each([
    ["active", /working/i],
    ["awaiting", /your move/i],
    ["warning", /^warning/i],
    ["errored", /failed/i],
  ] as const)("a signal row (%s) keeps a gloss line leading with the state", (state, expected) => {
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 1 })} actions={actions()} />);
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(expected);
  });

  // kata zq7g: the rail's only "waiting on you" signal used to be a 6px dot
  // plus uniformly grey (--ink-low) gloss text, identical for working/
  // needs-you/failed rows - a person had to already know to look at the dot
  // to tell them apart. The gloss text now carries the same family color as
  // its Cadence dot (cadenceStateFor), so a failed or needs-you row reads
  // distinctly even at a glance, no dot inspection required. Idle/ended never
  // render a gloss line at all (SIGNAL_STATES), so they need no family class.
  test.each([
    ["active", railStyles.activityAlive, "activityAlive"],
    ["awaiting", railStyles.activityAttention, "activityAttention"],
    ["warning", railStyles.activityAttention, "activityAttention"],
    ["errored", railStyles.activityDanger, "activityDanger"],
  ] as const)("a signal row (%s) tints its gloss text with the matching family color", (state, familyClass, name) => {
    if (familyClass === undefined) throw new Error(`RailRow.module.css is missing the "${name}" class`);
    render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 1 })} actions={actions()} />);
    const activity = screen.getByTestId("rail-row-activity");
    expect(activity.className.split(" ")).toContain(familyClass);
  });

  // The one state where the SAME wire state ("awaiting") means two different
  // things depending on ask_pending - see the activityGloss describe block
  // above for why this distinction exists.
  test("an ask_pending awaiting row glosses as a blocked question, not a generic move", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "awaiting", ask_pending: true }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/question waiting/i);
  });

  // --- a turn-ended subagent is quiet, never "your move" -------------------
  //
  // "awaiting" means "the turn ended; the next input comes from this session's
  // owner". For a TOP-LEVEL session that owner is the user, so the row says
  // "your move". A subagent's owner is its parent session - the user never
  // steers a subagent directly - so a turn-ended subagent is simply idle, and
  // glossing it "your move" made every finished delegate read as attention it
  // does not need. Only a genuine ask_user (ask_pending) still reaches the
  // user, so that one keeps its signal treatment.

  test("a turn-ended subagent row shows no dot, no gloss - just title + age", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ kind: "subagent", state: "awaiting", age: "4m" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-signal")).toBeNull();
    expect(screen.queryByTestId("rail-row-activity")).toBeNull();
    expect(screen.getByText("Fix flaky test")).toBeTruthy();
    expect(screen.getByTestId("rail-row-time").textContent).toBe("4m");
  });

  test("a turn-ended subagent's title tooltip reports idle, never your move", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ kind: "subagent", state: "awaiting" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · idle");
  });

  test("a subagent blocked on ask_user still glosses as a question, not idle", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ kind: "subagent", state: "awaiting", ask_pending: true }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/question waiting/i);
  });

  test.each(["idle", "ended", "notLoaded", ""] as const)(
    "a quiet, nested row (%s, depth > 0) is title + age on one line, with no gloss at all",
    (state) => {
      render(
        <RailRow node={sessionRailNode(apiNode({ state, age: "3h" }))} info={info({ depth: 1 })} actions={actions()} />,
      );
      expect(screen.queryByTestId("rail-row-activity")).toBeNull();
      expect(screen.getByText("Fix flaky test")).toBeTruthy();
      expect(screen.getByTestId("rail-row-time").textContent).toBe("3h");
    },
  );

  // kata hxjn: the exception above, in the flat Live/Pinned tiers (depth 0).
  // A quiet row there still names its project, since a flat list gives it no
  // other way to say which project it belongs to.
  test.each(["idle", "ended", "notLoaded", ""] as const)(
    "a quiet, top-level row (%s, depth 0) names its project on a second line",
    (state) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state, age: "3h", project: "prime-radiant" }))}
          info={info({ depth: 0 })}
          actions={actions()}
        />,
      );
      expect(screen.getByTestId("rail-row-activity").textContent).toBe("prime-radiant");
      expect(screen.getByTestId("rail-row-time").textContent).toBe("3h");
    },
  );

  // The dot and the gloss answer the same question in a NESTED row (depth >
  // 0, where hxjn's project line never applies) - one predicate
  // (SIGNAL_STATES) drives both there, which is what stops a nested row from
  // ever showing a dot with no explanation or an explanation with no dot. At
  // depth 0 that one-to-one correspondence is deliberately broken by the
  // project line (tested above and below).
  test.each(["active", "awaiting", "warning", "errored", "idle", "ended", "notLoaded", ""] as const)(
    "the dot and the gloss line agree for state %s on a nested row",
    (state) => {
      render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 1 })} actions={actions()} />);
      const hasDot = screen.queryByTestId("cadence-dot") !== null;
      const hasGloss = screen.queryByTestId("rail-row-activity") !== null;
      expect(hasGloss).toBe(hasDot);
    },
  );

  // At depth 0 a dot still always implies a gloss (a signal is never silent),
  // but the reverse no longer holds: a quiet top-level row shows a gloss line
  // for its project with no dot at all.
  test.each(["active", "awaiting", "warning", "errored", "idle", "ended", "notLoaded", ""] as const)(
    "a dot on a top-level row always implies a gloss line",
    (state) => {
      render(<RailRow node={sessionRailNode(apiNode({ state }))} info={info({ depth: 0 })} actions={actions()} />);
      const hasDot = screen.queryByTestId("cadence-dot") !== null;
      const hasGloss = screen.queryByTestId("rail-row-activity") !== null;
      if (hasDot) expect(hasGloss).toBe(true);
    },
  );

  // Branch survives on a signal row because it distinguishes SIBLINGS in the
  // case that matters: two working sessions in one project on different
  // branches. Rendered nested (depth > 0) to isolate it from hxjn's project
  // line, tested separately below.
  test("a signal row's gloss carries the branch when the session has one", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", branch: "fix/thing" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("working · fix/thing");
  });

  // kata hxjn: a top-level (depth 0) signal row's gloss leads with the
  // project, then the usual state · branch join - project answers "where",
  // the rest answers "what's happening", in that reading order.
  test("a top-level signal row's gloss leads with the project, then state and branch", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", branch: "fix/thing", project: "prime-radiant" }))}
        info={info({ depth: 0 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("prime-radiant · working · fix/thing");
  });

  // UX fix: an empty project name must not leave an orphaned leading " · "
  // separator in front of the gloss - the separator only belongs between two
  // real parts.
  test("a top-level signal row with an empty project has no orphaned leading separator", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", project: "" }))}
        info={info({ depth: 0 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("working");
  });

  // Three facts of noise on every row: the model is a property of the session,
  // not a reason to look at it, and the session pane's own status strip reports
  // it the moment the row is opened.
  test("no row - signal or quiet - carries the model anywhere on its visible face", () => {
    const { rerender } = render(
      <RailRow node={sessionRailNode(apiNode({ state: "active", model: "opus" }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByTestId("rail-row-activity").textContent).not.toMatch(/opus/);

    rerender(
      <RailRow node={sessionRailNode(apiNode({ state: "idle", model: "opus" }))} info={info()} actions={actions()} />,
    );
    expect(screen.queryByText(/opus/)).toBeNull();
  });

  // --- a session that has never run says so ------------------------------
  //
  // An empty-prompt spawn starts a session dormant (kata ytpa), and the server
  // reports it "idle" - the same word a session that ran and went quiet gets.
  // Every quiet row is title + age, so the two were the same row. Worse, the
  // age READ as activity: it falls back to the creation time, so a session that
  // has never done anything showed a confident "now" or "4m".
  //
  // The row spends no new space on this. The one slot that was actively lying -
  // the age - is the slot that carries the correction, so a dormant row stays
  // one line and the mobile drawer still shows the same six rows.

  test("a dormant row says it has not started, in place of an age that would read as activity", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "4m", dormant: true }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-not-started").textContent).toBe("Not started");
    expect(screen.queryByTestId("rail-row-time")).toBeNull();
  });

  test("a session that has run keeps its age", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "4m", dormant: false }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("rail-row-time").textContent).toBe("4m");
    expect(screen.queryByTestId("rail-row-not-started")).toBeNull();
  });

  // "Not started" is a fact about a row's history, not a call for help. The
  // signal gutter is reserved for the states worth crossing the room for
  // (working / needs you / failed) - a dot here would put a dormant session in
  // that company, and a rail full of dots is a rail whose dots mean nothing.
  // Rendered nested (depth > 0) so hxjn's project line - orthogonal to
  // dormancy - doesn't participate in this assertion; see the dedicated
  // depth-0 case just below.
  test("a dormant, nested row earns no dot and no gloss line", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", dormant: true }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.queryByTestId("rail-row-activity")).toBeNull();
  });

  // kata hxjn: a dormant row still needs its project named when it's a
  // top-level Live/Pinned row - dormancy says nothing about which project a
  // flat row belongs to.
  test("a dormant, top-level row still names its project, with no dot", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", dormant: true, project: "prime-radiant" }))}
        info={info({ depth: 0 })}
        actions={actions()}
      />,
    );
    expect(screen.queryByTestId("cadence-dot")).toBeNull();
    expect(screen.getByTestId("rail-row-activity").textContent).toBe("prime-radiant");
  });

  // The moment a dormant session is given something to do it is working, and
  // the row must say THAT. Dormancy is only ever the most useful thing to
  // report on a row that is otherwise quiet.
  test.each(["active", "awaiting", "warning", "errored"] as const)(
    "a signal state (%s) outranks dormancy in the right slot",
    (state) => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ state, age: "now", dormant: true }))}
          info={info()}
          actions={actions()}
        />,
      );
      expect(screen.queryByTestId("rail-row-not-started")).toBeNull();
      expect(screen.getByTestId("rail-row-time").textContent).toBe("now");
    },
  );

  // The visible row gave up its age, so the tooltip has to keep it - the same
  // contract every other fact this row drops is held to.
  test("a dormant row's tooltip keeps the age its visible line gave up", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", age: "4m", dormant: true }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · not started · 4m");
  });

  // --- what the visible row drops stays reachable on hover --------------
  //
  // Tier is real information a title cannot carry, and a quiet row no longer
  // prints its state - so both land in the row's own title tooltip, which
  // already existed for truncated titles.

  test("a quiet row's title tooltip carries the state word its visible line no longer prints", () => {
    render(<RailRow node={sessionRailNode(apiNode({ state: "ended" }))} info={info()} actions={actions()} />);
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · ended");
  });

  test("the tier rides the title tooltip on both quiet and signal rows", () => {
    const { rerender } = render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "idle", tier: "archived" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · idle · archived");

    // A signal row already prints its state, so the tooltip doesn't repeat it.
    rerender(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", tier: "archived" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test · archived");
  });

  // "current" is the unremarkable default state of a session - the same
  // exclusion the old visible line made, kept in its new home.
  test("the unremarkable 'current' tier is omitted from the tooltip too", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ state: "active", tier: "current" }))}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("Fix flaky test").getAttribute("title")).toBe("Fix flaky test");
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

  test("menu offers 'Pin this session…' for an unassigned top-level session, assigning through onPinSession", async () => {
    const acts = actions();
    const session = renderRow({ pin_section_id: undefined, ref: "local:a" }, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
    // The shared SessionMenu owns the picker now (one owner per dialog) -
    // the row's duty stops at feeding the picked target out through
    // onPinSession.
    await user.click(await screen.findByRole("button", { name: "Client" }));
    await waitFor(() =>
      expect(acts.onPinSession).toHaveBeenCalledWith(
        session,
        { section_id: "sec_1" },
        { id: "sec_1", name: "Client", member_count: 0 },
      ),
    );
  });

  test("menu offers 'Unpin' for an assigned top-level session", async () => {
    const acts = actions();
    const session = apiNode({ pin_section_id: "research" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);
    const user = await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
    await user.click(screen.getByRole("menuitem", { name: "Unpin" }));
    expect(acts.onUnpinRequest).toHaveBeenCalledWith(session);
  });

  // The unified menu keeps one stable item list, so Rename is always LISTED -
  // a session whose wire `rename` flag is absent gets it disabled instead of
  // dropped.
  test("menu disables Rename when the session does not support it", async () => {
    renderRow({ rename: false });
    await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
  });

  test("menu offers Rename when the session supports it, saving through onRenameSession", async () => {
    const acts = actions();
    const session = renderRow({ rename: true }, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    const dialog = screen.getByRole("dialog", { name: "Rename session" });
    const input = within(dialog).getByLabelText("Name");
    await user.clear(input);
    await user.type(input, "New name");
    await user.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(acts.onRenameSession).toHaveBeenCalledWith(session, "New name"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
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

  // Archive is a decision about a TOP-LEVEL row, and only a top-level row can
  // act on it: the server stores one archive decision per session id, and a
  // nested row has no independent existence in the tree its parent isn't
  // already deciding for. hubcore's nodeKind (internal/hubcore/tree.go) names
  // the three kinds that are never top-level - "subagent" (nested under its
  // parent), "fork" (a snapshotted original nested under the branch that
  // superseded it), and the synthetic "cluster" fold row - so `kind` is the
  // whole test, at any depth.
  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`menu omits Archive on a ${kind} row - only top-level sessions are archivable`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind, tier: "current" }))} info={info()} actions={actions()} />);
      // The unified menu is on every session row (the pane items are always
      // meaningful), so the trigger is always there - what the row loses is
      // the organization group.
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Archive" })).toBeNull();
      expect(screen.queryByRole("menuitem", { name: "Unarchive" })).toBeNull();
    });
  }

  // Delete (kata n15j) is a decision about a TOP-LEVEL LOCAL session: it
  // targets a stable local session ref (identifier.ValidateSessionID via
  // cmd/evener-hub/app_session_delete.go), so it is offered unconditionally
  // for a top-level local row - including a live one, which the server
  // refuses via the same skipped/toast path deleteProject already uses for a
  // session that raced back to live (no client-side liveness gate to
  // duplicate the server's own crash-vs-live predicate).
  test("menu offers 'Delete…' for a top-level local session, confirming through onDeleteSession", async () => {
    const acts = actions();
    const session = renderRow({ host_id: "local" }, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
    const dialog = screen.getByRole("dialog", { name: "Delete session?" });
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(acts.onDeleteSession).toHaveBeenCalledWith(session));
  });

  // "do not offer this capability for remote-source threads" (kata n15j) -
  // the menu itself withholds Delete for a non-local session rather than
  // relying solely on the server's own isLocalRouteID refusal.
  test("menu omits Delete for a remote-source session", async () => {
    render(<RailRow node={sessionRailNode(apiNode({ host_id: "remote" }))} info={info()} actions={actions()} />);
    await openMenu(/actions for/i);
    expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
  });

  // Delete is scoped like Archive: only a top-level row names a real,
  // independently deletable session (see the Archive loop's own comment
  // above for why these three kinds are never top-level).
  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`menu omits Delete on a ${kind} row - only top-level sessions are deletable`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind, host_id: "local" }))} info={info()} actions={actions()} />);
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
    });
  }

  // Favorite is scoped for the same reason as Archive: session rows use the
  // separate session-pin action, while cluster rows have a synthetic
  // "cluster:<hex>" identity rather than an independently pinnable session.
  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`menu omits pin and unpin on a ${kind} row`, async () => {
      render(<RailRow node={sessionRailNode(apiNode({ kind }))} info={info()} actions={actions()} />);
      await openMenu(/actions for/i);
      expect(screen.queryByRole("menuitem", { name: "Pin this session…" })).toBeNull();
      expect(screen.queryByRole("menuitem", { name: "Unpin" })).toBeNull();
    });
  }

  // With pin, archive and delete gone, a subagent's unified menu is down to
  // the items that are always meaningful: the pane group, Rename (disabled -
  // the wire withholds `rename` from every nested/synthetic node), and
  // Shut down.
  test("a subagent row's menu is panes + rename + shut down only", async () => {
    renderRow({ kind: "subagent", rename: false });
    await openMenu(/actions for/i);
    const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(items).toEqual(["Details", "Tasks", "Activity", "Rename", "Shut down"]);
  });

  // The row's menu is THE shared SessionMenu now - the same item list, in the
  // same order, the session pane's chrome shows (SessionMenu.test.tsx pins
  // the component's own copy of this contract).
  test("session row menu is the unified menu: panes group first, shut down present", async () => {
    renderRow({ kind: "session", host_id: "local" });
    await openMenu(/actions for/i);
    const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
    expect(items).toEqual([
      "Details",
      "Tasks",
      "Activity",
      "Rename",
      "Pin this session…",
      "Archive",
      "Shut down",
      "Delete…",
    ]);
  });

  test("Details opens the session pane, then the sessionDetails pane", async () => {
    const acts = actions({
      // The same wiring Rail's rowActions gives this callback: the session
      // pane itself, plus the selected panel beside it.
      onOpenSessionPane: (target, pane) => {
        const workspace = workspaceStore.getState();
        workspace.openPane("session", { ref: target.ref });
        workspace.openPane(sessionPanelPaneType(pane), { ref: target.ref });
      },
    });
    renderRow({}, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Details" }));
    const panes = workspaceStore.getState().panes.map((p) => p.type);
    expect(panes).toContain("session");
    expect(panes).toContain("sessionDetails");
  });

  test("shut down confirms through onShutdownSession", async () => {
    const acts = actions();
    const session = renderRow({}, acts);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
    const dialog = screen.getByRole("dialog", { name: "Shut down this session?" });
    await user.click(within(dialog).getByRole("button", { name: "Shut down" }));
    await waitFor(() => expect(acts.onShutdownSession).toHaveBeenCalledWith(session));
  });

  // Title-first row (rail truncation round): the branch is secondary metadata
  // that used to sit in the row's main line as a flex:none sibling, so at the
  // rail's 280px it took its width off the top of the ONE thing that identifies
  // a row. It rides the gloss line, which ellipsizes on its own; the title keeps
  // the whole main line minus the (short, fixed) age.
  test("keeps the branch out of the title's line, on the gloss line instead", () => {
    const session = apiNode({ state: "active", branch: "main", age: "47m" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);

    expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/main/);
    // The row's main line holds the title and the age, and nothing else
    // that reserves width: every other text node lives on line two. The
    // walk is label -> titleLine -> textCol -> row.
    const title = screen.getByText("Fix flaky test");
    const textCol = title.parentElement?.parentElement;
    const mainLine = textCol?.parentElement;
    expect(mainLine).toBeTruthy();
    const mainLineText = [...(mainLine?.children ?? [])]
      .filter((child) => child !== textCol)
      .map((child) => child.textContent)
      .join(" ");
    expect(mainLineText).not.toMatch(/main/);
    expect(screen.getByTestId("rail-row-time").textContent).toBe("47m");
  });

  // vitest leaves CSS Modules unprocessed (vite.config.ts sets no test.css),
  // so the rule that actually keeps a long gloss from wrapping the row to a
  // third line is only checkable against the stylesheet text - the same way
  // StackHost.test.tsx / radiogroup.test.tsx pin their own layout-critical
  // declarations.
  test("the activity line's stylesheet rule ellipsizes rather than wraps, so metadata never grows the row", () => {
    const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "RailRow.module.css"), "utf8");
    const activityRule = /\.activity\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(activityRule).toMatch(/white-space:\s*nowrap/);
    expect(activityRule).toMatch(/text-overflow:\s*ellipsis/);
    expect(activityRule).toMatch(/overflow:\s*hidden/);
  });

  // The tooltip's original job, unchanged: a truncated title stays recoverable.
  // The title always LEADS the tooltip, so the recovery still works even now
  // that the tooltip carries the row's dropped facts after it.
  test("a truncated title stays readable via a hover tooltip", () => {
    const long = "It looks like a lot of the sidebar rows are truncating their titles";
    render(
      <RailRow node={sessionRailNode(apiNode({ title: long, state: "active" }))} info={info()} actions={actions()} />,
    );
    expect(screen.getByText(long).getAttribute("title")).toBe(long);
  });

  test("the activity line carries its own full text as a tooltip, since it ellipsizes too", () => {
    const session = apiNode({ state: "active", model: "opus", branch: "feature/long-branch-name" });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={actions()} />);
    const activity = screen.getByTestId("rail-row-activity");
    expect(activity.getAttribute("title")).toBe(activity.textContent);
  });

  // A live-tier row's own Tier/PinSectionID/Rename fields must all survive the
  // duplicate projection. RailRow reads pin_section_id/rename directly,
  // regardless of the session's real decisions, since the navigation
  // projection stamps the live tier separately. RailRow never gated
  // these on tier itself - it just reads session.favorite/session.rename
  // directly, same as every other row - so once the hub fix landed, this
  // was already correct with no rail-side code change; pinned explicitly
  // here (rather than left to incidental coverage from fixtures that never
  // set tier at all) since a live row is the realistic shape a reviewer
  // would specifically want proof for. The star stays hidden: a depth-0 row
  // (Live, like a named pin section) never carries the pin star at all.
  test("Unpin and Rename work on a live-tier duplicate, and its pin star stays hidden", async () => {
    const acts = actions();
    const session = apiNode({ tier: "live", pin_section_id: "research", rename: true });
    render(<RailRow node={sessionRailNode(session)} info={info()} actions={acts} />);

    expect(screen.queryByTestId("favorite-star")).toBeNull();
    const user = await openMenu(/actions for/i);
    expect(screen.getByRole("menuitem", { name: "Unpin" })).toBeTruthy();
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    // The dialog opens prefilled with the current title; saving unchanged
    // still round-trips through onRenameSession.
    const dialog = screen.getByRole("dialog", { name: "Rename session" });
    await user.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(acts.onRenameSession).toHaveBeenCalledWith(session, session.title));
  });
});

describe("project row", () => {
  test("descendant-only project changes do not invoke the project RailRow again", () => {
    // This fails until RailRow's memo boundary compares project rows by the
    // fields ProjectRow renders/captures instead of by ancestor node identity.
    const observer = vi.fn();
    const rowInfo = info({ hasChildren: true });
    const rowActions = actions();
    const firstSession = apiNode({ row_id: "project:p1:local:first", ref: "local:first", session_id: "first" });
    const secondSession = apiNode({ row_id: "project:p1:local:second", ref: "local:second", session_id: "second" });
    const firstProject = apiProject({ sessions: [firstSession] });
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow
          node={projectRailNode(firstProject, [sessionRailNode(firstSession)])}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledTimes(1);
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow
          node={projectRailNode({ ...firstProject, sessions: [secondSession] }, [sessionRailNode(secondSession)])}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(0);
  });

  test.each([
    ["node id", (node: ProjectRailNode) => ({ ...node, id: "projectnode:p1-replaced" })],
    ["display name", (node: ProjectRailNode) => ({ ...node, displayName: "Decorated project" })],
    ["resource error", (node: ProjectRailNode) => ({ ...node, resourceError: "load failed" })],
    ["retry callback", (node: ProjectRailNode) => ({ ...node, retry: vi.fn() })],
    ["project key", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, key: "p2" } })],
    ["project name", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, name: "Renamed" } })],
    [
      "project working directory",
      (node: ProjectRailNode) => ({ ...node, project: { ...node.project, working_dir: "/repo/next" } }),
    ],
    [
      "project rollup state",
      (node: ProjectRailNode) => ({ ...node, project: { ...node.project, rollup_state: "active" } }),
    ],
    ["project attention count", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, rollup_attn: 2 } })],
    ["project favorite", (node: ProjectRailNode) => ({ ...node, project: { ...node.project, favorite: true } })],
    [
      "project archive state",
      (node: ProjectRailNode) => ({ ...node, project: { ...node.project, is_archived: true } }),
    ],
  ] as const)("%s changes still invoke the project RailRow", (_name, change) => {
    const observer = vi.fn();
    const rowInfo = info();
    const rowActions = actions();
    const firstNode = { ...projectRailNode(apiProject()), retry: vi.fn() };
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow node={firstNode} info={rowInfo} actions={rowActions} />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow node={change(firstNode)} info={rowInfo} actions={rowActions} />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(1);
  });

  test("changed retry, spawn directory, and project action input replace captured project-row behavior", async () => {
    window.history.replaceState({}, "", "/");
    const observer = vi.fn();
    const rowInfo = info();
    const firstRetry = vi.fn();
    const secondRetry = vi.fn();
    const rowActions = actions();
    const firstProject = apiProject({ working_dir: "/repo/first" });
    const secondProject = { ...firstProject, key: "p2", working_dir: "/repo/next" };
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow
          node={{ ...projectRailNode(firstProject), resourceError: "load failed", retry: firstRetry }}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow
          node={{
            ...projectRailNode(secondProject),
            id: "projectnode:p1",
            resourceError: "load failed",
            retry: secondRetry,
          }}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(firstRetry).not.toHaveBeenCalled();
    expect(secondRetry).toHaveBeenCalledTimes(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "New session in Proj" }));
    expect(`${window.location.pathname}${window.location.search}`).toBe("/new?dir=%2Frepo%2Fnext");
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(rowActions.onToggleFavoriteProject).toHaveBeenCalledWith(secondProject);
    window.history.replaceState({}, "", "/");
  });

  test("changed TreeRowInfo and actions identities still invoke the project RailRow and replace handlers", async () => {
    const observer = vi.fn();
    const firstInfo = info({ hasChildren: true });
    const secondInfo = info({ hasChildren: true });
    const firstActions = actions();
    const secondActions = actions();
    const node = projectRailNode(apiProject(), [sessionRailNode(apiNode())]);
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow node={node} info={firstInfo} actions={firstActions} />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow node={node} info={secondInfo} actions={firstActions} />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledTimes(1);
    await userEvent.setup().click(screen.getByText("Proj"));
    expect(firstInfo.activate).not.toHaveBeenCalled();
    expect(secondInfo.activate).toHaveBeenCalledTimes(1);
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow node={node} info={secondInfo} actions={secondActions} />
      </RailRenderObserver>,
    );
    expect(observer).toHaveBeenCalledTimes(1);
    const user = await openMenu(/actions for/i);
    await user.click(screen.getByRole("menuitem", { name: "Add to pinned" }));
    expect(firstActions.onToggleFavoriteProject).not.toHaveBeenCalled();
    expect(secondActions.onToggleFavoriteProject).toHaveBeenCalledWith(node.project);
  });

  test("non-project rows retain default memo behavior for descendant-only node replacement", () => {
    const observer = vi.fn();
    const rowInfo = info();
    const rowActions = actions();
    const session = apiNode();
    const firstNode = sessionRailNode(session);
    const { rerender } = render(
      <RailRenderObserver value={observer}>
        <RailRow node={firstNode} info={rowInfo} actions={rowActions} />
      </RailRenderObserver>,
    );
    observer.mockClear();

    rerender(
      <RailRenderObserver value={observer}>
        <RailRow
          node={{ ...firstNode, children: [sessionRailNode(apiNode({ row_id: "child", ref: "child" }))] }}
          info={rowInfo}
          actions={rowActions}
        />
      </RailRenderObserver>,
    );

    expect(observer).toHaveBeenCalledTimes(1);
  });

  test("renders the project's name and a Cadence reflecting its rollup state", () => {
    const project = apiProject({ name: "prime-radiant", rollup_state: "errored" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
    expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
  });

  // UX fix: two projects with the same name are disambiguated upstream in
  // railNodes.ts (projectDisplayLabels), which stamps the decorated label
  // onto ProjectRailNode.displayName - the row just has to prefer it.
  test("prefers node.displayName over the bare project name, when set", () => {
    const project = apiProject({ name: "frontend" });
    render(
      <RailRow
        node={{ ...projectRailNode(project), displayName: "frontend (repoA)" }}
        info={info()}
        actions={actions()}
      />,
    );
    expect(screen.getByText("frontend (repoA)")).toBeTruthy();
    expect(screen.queryByText("frontend", { selector: "span" })).toBeNull();
  });

  test("falls back to the bare project name when displayName is unset (the common, non-colliding case)", () => {
    const project = apiProject({ name: "prime-radiant" });
    render(<RailRow node={projectRailNode(project)} info={info()} actions={actions()} />);
    expect(screen.getByText("prime-radiant")).toBeTruthy();
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

  test("a childless project renders no chevron; a parent project's chevron trails its name", () => {
    const { rerender } = render(
      <RailRow node={projectRailNode(apiProject())} info={info({ hasChildren: false })} actions={actions()} />,
    );
    expect(screen.queryByTestId("rail-chevron")).toBeNull();

    rerender(<RailRow node={projectRailNode(apiProject())} info={info({ hasChildren: true })} actions={actions()} />);
    // Inline, right after the project name - the same trailing position a
    // session row's chevron takes, before the star/Badge slots.
    const name = screen.getByText("Proj");
    expect(name.nextElementSibling).toBe(screen.getByTestId("rail-chevron"));
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

// --- the actions share the right slot with the timestamp -----------------
//
// jsdom applies no stylesheet at all (vite.config.ts's test block enables
// no `css` processing), so "the revealed menu covers the timestamp" is not
// assertable against a rendered tree here. These read RailRow.module.css off
// disk and pin the structure that makes it true - same mechanism as
// styles/display-gates.test.ts and widgets/tooltip's own touch gate.
describe("shared right slot (RailRow.module.css)", () => {
  const RAIL_CSS = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "RailRow.module.css"), "utf8");
  // Block comments stripped so a class or token named only in prose can
  // never satisfy an assertion (same discipline as token-contract.test.ts).
  const CSS = RAIL_CSS.replace(/\/\*[\s\S]*?\*\//g, " ");

  function ruleFor(selector: string): string | null {
    const escaped = selector.replace(/[.[\]"^$*+?()|{}\\]/g, "\\$&");
    const match = new RegExp(`(?:^|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(CSS);
    return match ? match[1]! : null;
  }

  // 2026-08 sidebar UX rework, successor to the issue #196 fix it keeps the
  // guarantee of. The #196 rework made `.actions` a real in-flow flex item
  // beside the timestamp, so flexbox reserved it space and the trailing
  // disclosure chevron could never be laid out under it (the pre-#196
  // absolutely-positioned overlay swallowed the chevron's clicks, invisibly,
  // because CSS opacity never disables hit-testing). The cost was that every
  // row's title column stayed narrower by the menu's full width at rest as
  // much as on hover. The shared right slot keeps the structural guarantee -
  // the slot is the in-flow item, the chevron lives in .textCol to its LEFT -
  // while the menu and the timestamp share ONE grid cell: the menu borrows
  // the timestamp's space instead of adding its own, and the two swap
  // visibility on reveal. jsdom applies no stylesheet and
  // getBoundingClientRect() always reports zero there, so this describe block
  // can only pin the STYLESHEET CONTRACT (same discipline as
  // token-contract.test.ts); the actual non-overlap geometry and per-state
  // hit-testing, at real sidebar widths with a real truncating title, is
  // what layoutguard's rail-row-chevron-actions-overlap case proves against
  // a real browser.
  test("the actions share one grid cell with the right-slot occupant - never an overlay", () => {
    const slotRule = ruleFor(".rightSlot");
    expect(slotRule, ".rightSlot must have a rule").not.toBeNull();
    expect(slotRule).toMatch(/display:\s*grid/);
    // The slot is the in-flow flex item (flexbox reserves its width, so the
    // chevron in .textCol can never be pushed underneath anything in it).
    expect(slotRule).toMatch(/flex:\s*none/);
    // Both children occupy the same area, so the slot is only ever as wide
    // as the WIDER of occupant and menu - the menu borrows the timestamp's
    // space rather than reserving its own beside it.
    const cellRule = ruleFor(".rightSlot > *");
    expect(cellRule, ".rightSlot > * must have a rule").not.toBeNull();
    expect(cellRule).toMatch(/grid-area:\s*1\s*\/\s*1/);

    const actionsRule = ruleFor(".actions");
    expect(actionsRule, ".actions must have a rule").not.toBeNull();
    expect(actionsRule).not.toMatch(/position:\s*absolute/);
    expect(actionsRule).not.toMatch(/\bright:\s*0/);
    // Hidden means BOTH: opacity never disables hit-testing (issue #196), so
    // visibility is what keeps the at-rest menu from eating the clicks it
    // covers.
    expect(actionsRule).toMatch(/opacity:\s*0/);
    expect(actionsRule).toMatch(/visibility:\s*hidden/);
  });

  test("revealing the menu covers the occupant - and only on a hover-capable desktop pointer", () => {
    // The reveal flips BOTH halves of the shared cell. Menu side (top-level
    // rule): the same three conditions as ever - row hover, treeitem focus,
    // this row's own menu held open.
    const rules = [...CSS.matchAll(/([^{}]*)\{([^}]*)\}/g)].map((m) => ({
      selector: m[1]!.trim(),
      body: m[2]!,
    }));
    const reveal = rules.find((r) => r.selector.includes(".railRow:hover .actions"));
    expect(reveal, "row hover must reveal the actions").toBeTruthy();
    expect(reveal!.body).toMatch(/opacity:\s*1/);
    expect(reveal!.body).toMatch(/visibility:\s*visible/);
    const revealTargets = reveal!.selector.split(",").map((s) => s.trim());
    expect(revealTargets).toEqual(
      expect.arrayContaining([
        '[role="treeitem"]:focus .actions',
        '.actions:has(button[aria-expanded="true"])',
        ".railRow:hover .actions",
      ]),
    );

    // Occupant side: the same three conditions hide the timestamp/Badge
    // under the menu. The flip lives inside a hover-capable desktop media
    // block - on touch the actions are always visible BESIDE the occupant
    // (the 899px block below), and a sticky tap-hover or treeitem focus
    // must never make the timestamp vanish out from under the row. Rules
    // nested in @media mangle the flat matchAll above, so this half is
    // pinned against the media block's own text.
    const flipMedia = /@media\s*\(hover:\s*hover\)\s*and\s*\(min-width:\s*900px\)\s*\{([\s\S]*?)\n\}/.exec(CSS);
    expect(flipMedia, "the occupant flip must be gated to hover-capable desktop pointers").not.toBeNull();
    const flipBlock = flipMedia![1]!;
    for (const selector of [
      '[role="treeitem"]:focus .rightSlot > :not(.actions)',
      '.rightSlot:has(button[aria-expanded="true"]) > :not(.actions)',
      ".railRow:hover .rightSlot > :not(.actions)",
    ]) {
      expect(flipBlock).toContain(selector);
    }
    expect(flipBlock).toMatch(/opacity:\s*0/);
    expect(flipBlock).toMatch(/visibility:\s*hidden/);
  });

  test("the disclosure chevron carries no stacking-order fix - it doesn't need one", () => {
    // The z-index approach the #196 rework replaced required
    // position:relative + z-index on .chevronButton to win a stacking fight
    // against .actions. Neither is needed (or wanted - see the describe
    // block's own comment) now that the chevron and the menu are
    // layout-disjoint by construction.
    const chevronRule = ruleFor(".chevronButton");
    expect(chevronRule, ".chevronButton must have a rule").not.toBeNull();
    expect(chevronRule).not.toMatch(/position:\s*relative/);
    expect(chevronRule).not.toMatch(/z-index:/);
  });

  test("nothing paints a mask over .actions - the shared cell needs no background to hide behind", () => {
    // The old absolute overlay needed a background (to match whatever it
    // covered) and a gradient + padding-left (so its leading edge didn't
    // slice covered text mid-glyph). An in-flow grid item covers its own
    // cell and nothing else, so none of that machinery belongs here - its
    // reappearance would be a sign the overlay design crept back in.
    const actionsRule = ruleFor(".actions");
    expect(actionsRule).not.toMatch(/background:/);
    expect(actionsRule).not.toMatch(/linear-gradient/);
    expect(actionsRule).not.toMatch(/padding-left:/);
  });

  test("the row's menu trigger hugs its glyph, right-justified to the slot's edge", () => {
    // The Menu widget's trigger is padded for a standalone button
    // (--space-4 on both sides), which centered the "..." glyph ~16px in
    // from the slot's right edge - the x the timestamp's own text ends at.
    // The row's override keeps padding only on the leading side, so the
    // revealed menu right-justifies to the timestamp's own edge (and the
    // shared cell narrows to the glyph's real width). Scoped by attribute
    // so a project row's "+" IconButton keeps its own square geometry.
    //
    // Anchored to the TOP-LEVEL rule (column 0): the same selector also
    // appears inside @media (pointer: coarse), where the widened tap target
    // centres the glyph instead - that override is the tap-floor describe's
    // own assertion above, not this one's.
    const justifyRule = /\n\.actions button\[aria-haspopup="menu"\]\s*\{([^}]*)\}/.exec(CSS);
    expect(justifyRule, "the row must right-justify the menu trigger's glyph").not.toBeNull();
    expect(justifyRule![1]).toMatch(/padding:\s*0\s+0\s+0\s+var\(--space-2\)/);
  });

  // The signal dot keeps a FIXED width and refuses to flex: its outdent
  // arithmetic (margin-left cancels width + the title line's gap) only
  // holds if the box it cancels is a constant. jsdom applies no stylesheet,
  // so this is only checkable against the (comment-stripped) stylesheet
  // text.
  test("the .signal slot reserves a fixed width and never flexes", () => {
    const rule = ruleFor(".signal");
    expect(rule).not.toBeNull();
    expect(rule).toMatch(/width:\s*(var\(--space-\d+\)|\d+px)/);
    expect(rule).toMatch(/flex:\s*none|flex-shrink:\s*0/);
  });

  test("touch keeps the actions permanently open beside the occupant instead of relying on a hover it doesn't have", () => {
    // Below the mobile breakpoint there's no hover to reveal the actions
    // with, so this block forces them visible AND turns the shared cell back
    // into an ordinary flex row - on touch the occupant and the menu sit
    // side by side; the visibility swap above is a desktop-hover mechanism.
    const media = /@media\s*\(max-width:\s*899px\)\s*\{([\s\S]*?)\n\}/g;
    const blocks = [...CSS.matchAll(media)].map((m) => m[1]!);
    const actionsBlock = blocks.find((b) => b.includes(".actions"));
    expect(actionsBlock, "the 899px block must still address .actions").toBeTruthy();
    expect(actionsBlock).toMatch(/\.rightSlot\s*\{[^}]*display:\s*flex/);
    expect(actionsBlock).toMatch(/opacity:\s*1/);
    expect(actionsBlock).toMatch(/visibility:\s*visible/);
    expect(actionsBlock).not.toMatch(/position:/);
    expect(actionsBlock).not.toMatch(/background:/);
  });
});

// A row that cannot be pinned must not display as pinned. The wire can still
// carry favorite:true on a nested or synthetic node - from a decision written
// before pinning was scoped, or by a direct API call - and rendering the star
// there is a dead end: the menu offers no way to take it off. Suppressing it
// keeps "only top-level sessions can be pinned" true in both directions.
// Depth 0 rows are rendered at depth 1 here so the KIND gate alone decides -
// the depth-0 flat tiers (Live, named pin sections) never show the star at
// all (see "shows a pin star on a nested row…" above).
describe("pin star follows the same scoping as the pin action", () => {
  test("a top-level session shows its star", () => {
    render(
      <RailRow
        node={sessionRailNode(apiNode({ kind: "session", pin_section_id: "research" }))}
        info={info({ depth: 1 })}
        actions={actions()}
      />,
    );
    expect(screen.getByTestId("favorite-star")).toBeTruthy();
  });

  for (const kind of ["subagent", "fork", "cluster"]) {
    test(`a ${kind} row shows no star even when the wire carries a section assignment`, () => {
      render(
        <RailRow
          node={sessionRailNode(apiNode({ kind, pin_section_id: "research" }))}
          info={info({ depth: 1 })}
          actions={actions()}
        />,
      );
      expect(screen.queryByTestId("favorite-star")).toBeNull();
    });
  }
});

test("an incompatible daemon has an attention signal and restart instruction", () => {
  renderRow({ state: "restartRequired", live: true, branch: "" });
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
  expect(screen.getByTestId("rail-row-activity").textContent).toContain("restart required");
});

test.each([
  ["own activity", { state: "active" }, { state: "idle" }, "working"],
  ["subagent activity", { state: "idle" }, { state: "active" }, "1 subagent working"],
  [
    "job activity",
    { state: "idle", running_jobs: [{ job_id: "job-a", job_type: "shell", status: "running" }] },
    { state: "idle" },
    "1 job running",
  ],
] as const)("normalized navigation preserves %s in the rendered sidebar", (_name, parentState, childState, gloss) => {
  const resource = normalizedRailResource(
    { kind: "project_page", projectKey: "project", tier: "current", offset: 0, limit: 50 },
    { ...parentState, running_jobs: "running_jobs" in parentState ? [...parentState.running_jobs] : [] },
    childState,
  );
  const parent = [...selectRailModel(resource).sessions.values()].find((session) => session.ref === "parent");
  if (!parent) throw new Error("missing parent");
  render(<RailRow node={sessionRailNode(parent)} info={info()} actions={actions()} />);
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
  expect(screen.getByTestId("rail-row-activity").textContent).toContain(gloss);
});

test("restart-required navigation disables daemon actions in the sidebar menu", async () => {
  renderRow({ state: "restartRequired", rename: false, live: true });
  await openMenu(/actions for/i);
  expect(screen.getByRole("menuitem", { name: "Shut down" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
});

test.each(["subagent", "job"] as const)("restart explanation survives %s activity", (kind) => {
  renderRow({
    state: "restartRequired",
    live: true,
    branch: "",
    children: kind === "subagent" ? [apiNode({ state: "active" })] : [],
    running_jobs: kind === "job" ? [{ job_id: "job-a", job_type: "shell", status: "running" }] : [],
  });
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
  const gloss = screen.getByTestId("rail-row-activity").textContent;
  expect(gloss).toContain("restart required");
  expect(gloss).toContain(kind === "subagent" ? "1 subagent working" : "1 job running");
});
