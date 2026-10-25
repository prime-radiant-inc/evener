// The Tasks tab uses the shared task body and real thread/task stores.
// Script only the transport boundary, so hydration, pushes and recovery run.

import type { NavigationManifest, Task, ThreadModel } from "@evener/appwire-client";
import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { connectionStore } from "../../stores/connection";
import { navigationStore } from "../../stores/navigation/store";
import { activityClient, activityThread } from "../../stores/sessionActivityTestUtils";
import { tasksPanelStore } from "../../stores/tasksPanel";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { Toast } from "../../widgets";
import { resetDisclosureStoreForTests, setDisclosureOpen } from "../../widgets/disclosure/disclosureStore";
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

function numbersIn(text: string | null | undefined): number[] {
  return (text?.match(/\d+/g) ?? []).map(Number);
}

// The open tab is the Tasks tab; its visible label and accessible name both
// carry the settled and total counts, in that order.
function expectTabCounts(settled: number, total: number) {
  const tab = screen.getByRole("radio", { checked: true });
  expect(numbersIn(tab.textContent)).toEqual([settled, total]);
  expect(tab.getAttribute("aria-label") ?? "").toMatch(new RegExp(`\\b${settled}\\b\\D+\\b${total}\\b`));
}

function liveGroupSizes(): Record<string, number> {
  return Object.fromEntries(
    screen.queryAllByTestId("task-group-live").map((group) => {
      expect(within(group).getByRole("heading")).toBeTruthy();
      return [group.dataset.status, within(group).getAllByTestId("task-row").length];
    }),
  );
}

function settledSummaryCounts(): number[] {
  return numbersIn(screen.getByTestId("task-settled-group-summary").textContent);
}

// Counts live in the tab label alone: no element below the tab bar restates
// "<done or settled> of <total>".
function expectNoSummaries(counts: { done: number; total: number; cancelled?: number }) {
  const sidebar = screen.getByTestId("activity-sidebar");
  expect(within(sidebar).queryByTestId("tasks-body-head")).toBeNull();
  const settled = counts.done + (counts.cancelled ?? 0);
  const restates = new RegExp(`\\b(?:${counts.done}|${settled})\\b\\D+\\b${counts.total}\\b`);
  const restating = [...sidebar.querySelectorAll("*")].filter(
    (element) =>
      element.children.length === 0 &&
      element.closest('[role="radio"]') === null &&
      restates.test(element.textContent ?? ""),
  );
  expect(restating).toEqual([]);
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
  resetDisclosureStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetWorkspaceStoreForTests();
  resetFocusedActivityScopeForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

describe("TasksTab", () => {
  test("no hydrated model yet reads as a loading state, never an empty list", () => {
    openTasks({ total: 5, done: 2 });
    renderSidebar();
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
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
    const counts = { total: 4, done: 1, cancelled: 1, current: { id: 2, description: "Current work" } };
    connectTasks(() => TASKS, counts);
    openTasks(counts);
    renderSidebar();
    await screen.findByText("Remaining work");
    expectTabCounts(2, 4);
    expect(liveGroupSizes()).toEqual({ in_progress: 1, open: 1 });
    const settled = screen.getByTestId("task-settled-group");
    expect(settledSummaryCounts()).toEqual([1, 1]);
    await user.click(within(settled).getByTestId("task-settled-group-summary"));
    expect(screen.getByText("Completed work")).toBeTruthy();
    expect(screen.getByText("Cancelled work")).toBeTruthy();
    const current = screen.getAllByTestId("task-row").find((row) => row.textContent?.includes("Current work"));
    expect(current).toBeTruthy();
    if (!current) throw new Error("Current task row missing");
    await user.click(within(current).getByText("Current work"));
    expect(screen.getByText("Read the contract")).toBeTruthy();
    expectNoSummaries(counts);
    expect(screen.queryByRole("button", { name: "Open tasks" })).toBeNull();
    expect(workspaceStore.getState().panes.some((pane) => pane.type === "sessionTasks")).toBe(false);
  });

  test("an empty list keeps a zero tab count and shows no task rows or summary", async () => {
    const counts = { total: 0, done: 0 };
    connectTasks(() => [], counts);
    openTasks(counts);
    renderSidebar();
    await waitFor(() => expect(tasksPanelStore.getState().entries.get(REF)?.rows).toEqual([]));
    expect(screen.queryAllByTestId("task-row")).toHaveLength(0);
    expect(screen.queryByTestId("task-settled-group")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expectTabCounts(0, 0);
    expectNoSummaries(counts);
  });

  test.each([
    { state: "completed", rows: [TASKS[0]], counts: { total: 1, done: 1 } },
    { state: "cancelled", rows: [TASKS[3]], counts: { total: 1, done: 0, cancelled: 1, remaining: 0 } },
  ])("keeps the $state state and tab count without either summary", async ({ rows, counts }) => {
    connectTasks(() => rows, counts);
    openTasks(counts);
    renderSidebar();
    await screen.findByTestId("task-settled-group");
    expect(settledSummaryCounts()).toEqual([1]);
    expect(liveGroupSizes()).toEqual({});
    expectTabCounts(1, counts.total);
    expectNoSummaries(counts);
  });

  test("live task updates refresh groups and tab counts without restoring summaries", async () => {
    let rows: Task[] = TASKS;
    const counts = { total: 4, done: 1, cancelled: 1 };
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
    await waitFor(() => expect(liveGroupSizes()).toEqual({}));
    expect(settledSummaryCounts()).toEqual([4]);
    expectTabCounts(4, 4);
    expectNoSummaries({ total: 4, done: 4 });
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
    expect(within(stale).getByRole("alert").textContent).toContain("task refresh unavailable");
    expect(screen.getByText("Remaining work")).toBeTruthy();
    expectNoSummaries({ total: 4, done: 1 });
    fake.on("evener/tasks/list", () => ({ data: [{ ...TASKS[2], id: 5, description: "Recovered work" }] }));
    await user.click(within(stale).getByRole("button", { name: "Try again" }));
    await screen.findByText("Recovered work");
    expect(screen.queryByTestId("tasks-stale")).toBeNull();
    expect(screen.queryByText("Remaining work")).toBeNull();
    expectNoSummaries({ total: 4, done: 1 });
  });

  test("compact task timestamps select completion, legacy update, and current update evidence", async () => {
    vi.useFakeTimers({ now: Date.parse("2026-10-05T00:00:00Z"), toFake: ["Date", "setInterval", "clearInterval"] });
    const completedAt = "2026-10-04T23:40:00Z";
    const updatedAt = "2026-10-04T23:50:00Z";
    const rows: Task[] = [
      { ...TASKS[0], updated_at: "2026-10-04T23:30:00Z", completed_at: completedAt },
      { ...TASKS[1], updated_at: updatedAt },
      { ...TASKS[2], updated_at: "2026-10-04T23:45:00Z" },
      { ...TASKS[3], updated_at: "2026-10-04T23:35:00Z" },
    ];
    connectTasks(() => rows, { total: 4, done: 1, cancelled: 1 });
    openTasks({ total: 4, done: 1, cancelled: 1 });
    setDisclosureOpen(`${REF}\0settled-group`, true);
    renderSidebar();
    await screen.findByText("Completed work");
    expect(screen.getByTestId("task-settled-group").hasAttribute("open")).toBe(true);
    const taskRows = screen.getAllByTestId("task-row");
    const witness = taskRows.map((row) => ({
      anchor: row.getAttribute("data-activity-anchor"),
      text: row.textContent,
      time: row.querySelector("time")?.outerHTML,
    }));
    expect(witness.map((row) => row.anchor).sort()).toEqual([1, 2, 3, 4].map((id) => `${REF}\0${id}`));
    const times = new Map(
      taskRows.map((row) => [row.getAttribute("data-activity-anchor")?.split("").at(-1), row.querySelector("time")]),
    );
    expect(times.get("1")?.dateTime).toBe(completedAt);
    expect(times.get("1")?.dataset.meaning).toBe("completed");
    expect(times.get("2")?.dateTime).toBe(updatedAt);
    expect(times.get("2")?.dataset.meaning).toBe("updated");
    expect(times.get("4")?.dateTime).toBe("2026-10-04T23:35:00Z");
    expect(times.get("4")?.dataset.meaning).toBe("updated");
    expect(screen.getByText("Completed work").getAttribute("data-struck")).toBeNull();
    expect(screen.getByText("Cancelled work").getAttribute("data-struck")).toBe("true");
    const beforeTime = times.get("2")?.textContent;
    act(() => vi.advanceTimersByTime(30_000));
    expect(times.get("2")?.textContent).not.toBe(beforeTime);
    expect(times.get("2")?.dateTime).toBe(updatedAt);
  });

  test.each(["0", "2026-02-30T00:00:00Z"])(
    "invalid task completion time %s falls back to its update",
    async (invalid) => {
      const updatedAt = "2026-10-04T23:50:00Z";
      connectTasks(() => [{ ...TASKS[0], updated_at: updatedAt, completed_at: invalid }], { total: 1, done: 1 });
      openTasks({ total: 1, done: 1 });
      setDisclosureOpen(`${REF}\0settled-group`, true);
      renderSidebar();
      await screen.findByText("Completed work");
      const time = screen.getByTestId("task-row").querySelector("time");
      expect(time?.dateTime).toBe(updatedAt);
      expect(time?.dataset.meaning).toBe("updated");
    },
  );
});
