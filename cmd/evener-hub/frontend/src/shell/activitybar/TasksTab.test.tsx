// The Tasks tab uses the shared task body and real thread/task stores.
// Script only the transport boundary, so hydration, pushes and recovery run.

import type { NavigationManifest, Task, ThreadModel } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { connectionStore } from "../../stores/connection";
import { navigationStore } from "../../stores/navigation/store";
import { activityClient, activityThread } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { Toast } from "../../widgets";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { resetFocusedActivityScopeForTests } from "../focusedSession";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "../paneRegistry";
import { summaryOf } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

type TaskCounts = NonNullable<ThreadModel["tasks"]>;
const REF = "local:a";
const TASKS: [Task, Task, Task, Task] = [
  { id: 1, type: "implement", description: "Completed work", prompt: "", status: "done" },
  { id: 2, type: "implement", description: "Current work", prompt: "Read the contract", status: "in_progress" },
  { id: 3, type: "verify", description: "Remaining work", prompt: "", status: "open" },
  { id: 4, type: "implement", description: "Cancelled work", prompt: "", status: "cancelled" },
];

function fixtureDescriptor<P>(id: PaneDescriptor<P>["id"]): PaneDescriptor<P> {
  return {
    id,
    title: () => `title for ${id}`,
    component: lazy(() => new Promise<{ default: React.ComponentType<PaneProps<P>> }>(() => {})),
  };
}

const restorePaneFixtures: Array<() => void> = [];

beforeAll(() => {
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("session")));
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

function manifest(): NavigationManifest {
  return {
    generation_id: "g1",
    revision: 1,
    sources: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
    sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  } as NavigationManifest;
}

function resource<T>(key: ResourceKey, data: T): ResourceState {
  return {
    key,
    data,
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "e",
    loading: false,
    stale: false,
    error: null,
    generationID: "g1",
  } as ResourceState;
}

function installSummary(tasks?: TaskCounts) {
  const root = summaryOf({
    ref: REF,
    title: "A",
    state: "active",
    tasks: tasks ? { ...tasks, current: tasks.current?.description } : undefined,
  });
  const liveKey: ResourceKey = { kind: "section", section: "live", offset: 0, limit: 50 };
  navigationStore.setState({
    mode: "v3",
    capability: { version: 1, generationId: "g1", sequence: 1, readVersions: [3] },
    clientGenerationID: "g1",
    manifest: resource({ kind: "manifest" }, manifest()) as ResourceState<NavigationManifest>,
    resources: new Map([
      [keyID(liveKey), resource(liveKey, { sessions: [root] })],
      [
        keyID({ kind: "location", ref: REF }),
        resource({ kind: "location", ref: REF }, { ref: REF, top_level_ref: REF, top_level: true, session: root }),
      ],
    ]),
    expanded: new Map(),
    attention: { changed: [], summary: manifest().attentionSummary },
  });
}

function openTasks(tasks?: TaskCounts) {
  installSummary(tasks);
  workspaceStore.getState().openPane("session", { ref: REF });
  activitySidebarStore.getState().openWith("tasks");
}

function connectTasks(rows: () => Task[], counts: TaskCounts) {
  const fake = activityClient();
  fake.on("thread/read", ({ ref }) => {
    const response = activityThread(ref);
    return { thread: { ...response.thread, evener: { ...response.thread.evener, tasks: counts } } };
  });
  fake.on("evener/tasks/list", () => ({ data: rows() }));
  connectionStore.getState().connect(fake);
  return fake;
}

function renderSidebar() {
  return render(
    <MotionProvider>
      <ActivitySidebar />
      <Toast />
    </MotionProvider>,
  );
}

function expectNoSummaries() {
  const sidebar = within(screen.getByTestId("activity-sidebar"));
  expect(sidebar.queryByText(/\d+ of \d+ done/)).toBeNull();
  expect(sidebar.queryByTestId("tasks-body-head")).toBeNull();
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
  resetDisclosureStoreForTests();
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetFocusedActivityScopeForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

describe("TasksTab", () => {
  test("no hydrated model yet reads as a loading state, never an empty list", () => {
    openTasks({ total: 5, done: 2 });
    renderSidebar();
    expect(screen.getByText("Loading tasks…")).toBeTruthy();
  });

  test("mounting subscribes to the thread and loads its real task list", async () => {
    const counts = { total: 4, done: 1, current: { id: 2, description: "Current work" } };
    const fake = connectTasks(() => TASKS, counts);
    openTasks(counts);
    renderSidebar();
    await screen.findByText("Current work", { selector: "span" });
    expect(fake.calls.find((call) => call.method === "thread/read")?.params).toMatchObject({ ref: REF });
    expect(fake.calls.find((call) => call.method === "evener/tasks/list")?.params).toEqual({ ref: REF });
  });

  test("renders groups and disclosures inline without redundant summaries or opening a pane", async () => {
    const user = userEvent.setup();
    const counts = { total: 4, done: 1, current: { id: 2, description: "Current work" } };
    connectTasks(() => TASKS, counts);
    openTasks(counts);
    renderSidebar();
    await screen.findByText("Remaining work");
    expect(screen.getByRole("radio", { name: "Tasks, 1 of 4 done" }).textContent).toBe("Tasks\n1/4");
    expect(screen.getByRole("heading", { name: "Current 1" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Remaining 1" })).toBeTruthy();
    const settled = screen.getByTestId("task-settled-group");
    expect(settled.textContent).toContain("1 completed task · 1 cancelled task");
    await user.click(within(settled).getByTestId("task-settled-group-summary"));
    expect(screen.getByText("Completed work")).toBeTruthy();
    expect(screen.getByText("Cancelled work")).toBeTruthy();
    const current = screen.getAllByTestId("task-row").find((row) => row.textContent?.includes("Current work"));
    expect(current).toBeTruthy();
    if (!current) throw new Error("Current task row missing");
    await user.click(within(current).getByText("Current work"));
    expect(screen.getByText("Read the contract")).toBeTruthy();
    expectNoSummaries();
    expect(screen.queryByRole("button", { name: "Open tasks" })).toBeNull();
    expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionTasks")).toBe(false);
  });

  test.each([
    { state: "empty", rows: [], counts: { total: 0, done: 0 }, label: "No tasks yet" },
    { state: "completed", rows: [TASKS[0]], counts: { total: 1, done: 1 }, label: "1 completed task" },
    {
      state: "cancelled",
      rows: [TASKS[3]],
      counts: { total: 1, done: 0, cancelled: 1, remaining: 0 },
      label: "1 cancelled task",
    },
  ])("keeps the $state state and tab count without either summary", async ({ rows, counts, label }) => {
    connectTasks(() => rows, counts);
    openTasks(counts);
    renderSidebar();
    await screen.findByText(label);
    expect(screen.getByRole("radio", { name: `Tasks, ${counts.done} of ${counts.total} done` }).textContent).toBe(
      `Tasks\n${counts.done}/${counts.total}`,
    );
    expectNoSummaries();
  });

  test("live task updates refresh groups and tab counts without restoring summaries", async () => {
    let rows: Task[] = TASKS;
    const counts = { total: 4, done: 1 };
    const fake = connectTasks(() => rows, counts);
    openTasks(counts);
    renderSidebar();
    await screen.findByText("Remaining work");
    rows = TASKS.map((task) => ({ ...task, status: "done" }));
    act(() => {
      fake.emitNotification({
        method: "evener/task/updated",
        params: { threadId: "owner", ref: REF, total: 4, done: 4 },
      });
      // Badge projection uses independently refreshed navigation counts.
      // This test exercises task transport, not navigation invalidation.
      installSummary({ total: 4, done: 4 });
    });
    await screen.findByText("4 completed tasks");
    expect(screen.queryByRole("heading", { name: /Current|Remaining/ })).toBeNull();
    expect(screen.getByRole("radio", { name: "Tasks, 4 of 4 done" }).textContent).toBe("Tasks\n4/4");
    expectNoSummaries();
  });

  test("a failed refresh retains rows and retry recovers without restoring summaries", async () => {
    const user = userEvent.setup();
    const fake = connectTasks(() => TASKS, { total: 4, done: 1 });
    openTasks({ total: 4, done: 1 });
    renderSidebar();
    await screen.findByText("Remaining work");
    fake.on("evener/tasks/list", () => {
      throw new Error("task refresh unavailable");
    });
    act(() => {
      fake.emitNotification({
        method: "evener/task/updated",
        params: { threadId: "owner", ref: REF, total: 4, done: 2 },
      });
    });
    const stale = await screen.findByTestId("tasks-stale");
    expect(within(stale).getByRole("alert").textContent).toBe("Couldn't load tasks: task refresh unavailable");
    expect(stale.textContent).toContain("Showing the last list that loaded.");
    expect(screen.getByText("Remaining work")).toBeTruthy();
    expectNoSummaries();
    fake.on("evener/tasks/list", () => ({ data: [{ ...TASKS[2], id: 5, description: "Recovered work" }] }));
    await user.click(within(stale).getByRole("button", { name: "Try again" }));
    await screen.findByText("Recovered work");
    expect(screen.queryByTestId("tasks-stale")).toBeNull();
    expect(screen.queryByText("Remaining work")).toBeNull();
    expectNoSummaries();
  });
});
