import type { ActivityTree, ThreadCapabilities, ThreadModel, ThreadReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { activityContext, activityJob, activityWatch } from "../../stores/sessionActivityTestUtils";
import { tasksPanelStore } from "../../stores/tasksPanel";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { NOW_TICK_MS } from "../session/liveness";
import { sessionPanelTitle } from "./index";
import { SessionPanelPane } from "./SessionPanelPane";

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetDisclosureStoreForTests();
  // This suite mounts the pane without the app shell that wires the stores.
});

afterEach(() => {
  cleanup();
  // restoreAllMocks() before useRealTimers(), not after: "Details owns a
  // clock after hydration" spies on globalThis.setInterval WHILE fake
  // timers are active, so vi.restoreAllMocks() (which restores a spy to
  // whatever it captured as "original" at spyOn time - here, the FAKE
  // setInterval) must run before useRealTimers() gets the last word on what
  // globalThis.setInterval actually is. Reversed, restoreAllMocks() runs
  // last and reinstalls that stale fake implementation, so every timer
  // registered afterward - including testing-library's own real-timer
  // waitFor() polling - silently never fires for the rest of this file.
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thread_a",
    name: "Build session",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    lastFrameAt: 0,
    capabilities: CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...overrides,
  };
}

function seedModel(model: ThreadModel): void {
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const RETAINED_TASKS = [
  {
    id: 1,
    type: "implement",
    description: "Retain the first task",
    prompt: "Keep this disclosure open across the pane remount.",
    status: "in_progress",
  },
];

function readResponse(ref: string): ThreadReadResponse {
  return {
    thread: {
      id: `thread_${ref}`,
      sessionId: `session_${ref}`,
      preview: "Build session",
      ephemeral: false,
      modelProvider: "anthropic/claude",
      createdAt: 1_000,
      updatedAt: 1_000,
      status: { type: "idle" },
      cwd: "/tmp/project",
      cliVersion: "1.0.0",
      source: "evener",
      evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    },
  };
}

function retainedActivity(): ActivityTree {
  return {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "session_a",
      ref: "ref_a",
      label: "Build session",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 1, complete: true },
      entries: [
        {
          kind: "shell",
          job: {
            jobId: "job_a",
            ownerSessionId: "session_a",
            ownerRef: "ref_a",
            type: "shell",
            status: "running",
            terminal: false,
            background: false,
            hasOutput: false,
            description: "compile retained shell",
            startedAt: "2026-08-05T00:00:00Z",
            outputBytes: 0,
          },
        },
        {
          kind: "shell",
          job: {
            jobId: "job_done",
            ownerSessionId: "session_a",
            ownerRef: "ref_a",
            type: "shell",
            status: "completed",
            terminal: true,
            background: false,
            hasOutput: false,
            description: "retained done shell",
            startedAt: "2026-08-05T00:01:00Z",
            endedAt: "2026-08-05T00:02:00Z",
            outputBytes: 0,
          },
        },
      ],
      branch: {},
    },
  };
}

test("renders a scaffold loading state before the session model hydrates", () => {
  render(<SessionPanelPane params={{ ref: "ref_a" }} paneId="panel-1" focused kind="tasks" />);

  expect(screen.getByText("Loading session panel…")).toBeTruthy();
});

test("keeps the scaffold heading consistent with the registered title after rename", async () => {
  const model = testModel({ name: "Initial name" });
  seedModel(model);
  const { rerender } = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-title" focused kind="details" />,
  );
  expect(screen.getByRole("heading", { name: sessionPanelTitle("details", model.ref, model.name) })).toBeTruthy();

  const renamed = testModel({ name: "Renamed session" });
  await act(async () => {
    seedModel(renamed);
  });
  rerender(<SessionPanelPane params={{ ref: renamed.ref }} paneId="panel-title" focused kind="details" />);
  expect(screen.getByRole("heading", { name: sessionPanelTitle("details", renamed.ref, renamed.name) })).toBeTruthy();
});

test("mounts Tasks body and fetches after the model is hydrated", async () => {
  const fake = connectFakeClient();
  fake.on("evener/tasks/list", () => ({
    data: [{ id: 1, type: "verify", description: "Run checks", prompt: "", status: "open" }],
  }));
  const model = testModel({ tasks: { total: 1, done: 0 } });
  seedModel(model);

  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks" focused kind="tasks" />);

  expect(await screen.findByText("Run checks")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "evener/tasks/list")).toHaveLength(1);
});

test("retains Tasks rows and disclosure state across a pane remount", async () => {
  const fake = connectFakeClient();
  fake.on("evener/tasks/list", () => ({ data: RETAINED_TASKS }));
  const model = testModel({ tasks: { total: 1, done: 0 } });
  seedModel(model);

  const first = render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks" focused kind="tasks" />);
  const summary = await screen.findByText("Retain the first task");
  await userEvent.click(summary);
  expect(screen.getByTestId("task-prompt")).toBeTruthy();
  first.unmount();

  expect(tasksPanelStore.getState().entries.get(model.ref)?.rows).toHaveLength(1);
  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks-remount" focused kind="tasks" />);

  expect(await screen.findByText("Retain the first task")).toBeTruthy();
  expect(screen.getByTestId("task-prompt")).toBeTruthy();
});

test("renders daemon-gone state from the retained Tasks store result", async () => {
  const fake = connectFakeClient();
  fake.on("evener/tasks/list", () => {
    throw new WireError("thread not found: thread_a", -32014, { evenerErrorInfo: "sessionUnavailable" });
  });
  const model = testModel({ tasks: { total: 1, done: 0 } });
  seedModel(model);

  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks" focused kind="tasks" />);

  expect(await screen.findByText("This session has ended")).toBeTruthy();
});

test.each(["tasks", "activity"] as const)("%s pane does not install the Details clock", async (kind) => {
  const fake = connectFakeClient();
  fake.on("evener/tasks/list", () => ({ data: [] }));
  fake.on("evener/jobs/list", () => ({ data: retainedActivity() }));
  const model = testModel({ tasks: { total: 0, done: 0 }, jobsUpdatedAt: 1 });
  seedModel(model);
  const setIntervalSpy = vi.spyOn(globalThis, "setInterval");

  render(<SessionPanelPane params={{ ref: model.ref }} paneId={`panel-${kind}`} focused kind={kind} />);
  await act(async () => Promise.resolve());

  // The dense activity tree runs its own 1s live-row ticker; the assertion is
  // only that neither pane installs the Details clock's NOW_TICK_MS cadence.
  expect(setIntervalSpy).not.toHaveBeenCalledWith(expect.any(Function), NOW_TICK_MS);
  setIntervalSpy.mockRestore();
});

test("Details owns a clock after hydration", () => {
  vi.useFakeTimers();
  const start = new Date("2026-08-05T00:00:00.000Z");
  vi.setSystemTime(start);
  const model = testModel({ activeTurnStartedAt: start.toISOString(), workMillis: 1_000 });
  seedModel(model);
  const setIntervalSpy = vi.spyOn(globalThis, "setInterval");

  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-details" focused kind="details" />);
  expect(setIntervalSpy).toHaveBeenCalledExactlyOnceWith(expect.any(Function), NOW_TICK_MS);
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("1s");

  act(() => vi.advanceTimersByTime(3_000));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("4s");
});

test("retains Details rendering and the current clock value across a pane remount", () => {
  vi.useFakeTimers();
  const start = new Date("2026-08-05T00:00:00.000Z");
  vi.setSystemTime(start);
  const model = testModel({ activeTurnStartedAt: start.toISOString(), workMillis: 1_000 });
  seedModel(model);

  const first = render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-details" focused kind="details" />);
  act(() => vi.advanceTimersByTime(3_000));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("4s");

  const mutated = { ...model, workMillis: 2_000 };
  act(() => {
    threadsStore.setState({ threads: new Map([[mutated.ref, mutated]]) });
  });
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("5s");
  first.unmount();

  seedModel(mutated);
  render(<SessionPanelPane params={{ ref: mutated.ref }} paneId="panel-details-remount" focused kind="details" />);
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("5s");
  act(() => vi.advanceTimersByTime(3_000));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("8s");
});

test("claims and releases the session ref with the pane lifecycle", async () => {
  connectFakeClient();
  const model = testModel();
  seedModel(model);
  const { unmount } = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-claim" focused kind="details" />,
  );

  await act(async () => {
    await Promise.resolve();
  });
  expect(threadsStore.getState().threads.has(model.ref)).toBe(true);

  unmount();
  expect(threadsStore.getState().threads.has(model.ref)).toBe(false);
});

test("claims after a delayed connection becomes ready and releases the hydrated ref", async () => {
  const fake = new FakeClient("idle");
  fake.on("thread/read", ({ ref }) => readResponse(ref ?? "ref_a"));
  connectionStore.getState().connect(fake);
  const model = testModel();
  const { unmount } = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-delayed-claim" focused kind="details" />,
  );
  expect(threadsStore.getState().threads.has(model.ref)).toBe(false);

  act(() => fake.emitReady());
  await waitFor(() => expect(threadsStore.getState().threads.has(model.ref)).toBe(true));
  expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThanOrEqual(1);

  unmount();
  expect(threadsStore.getState().threads.has(model.ref)).toBe(false);
  expect(threadsStore.getState().frameTimes.has(model.ref)).toBe(false);
});

test("does not claim a ref when unmounted before the delayed connection is ready", async () => {
  const fake = new FakeClient("idle");
  fake.on("thread/read", ({ ref }) => readResponse(ref ?? "ref_a"));
  connectionStore.getState().connect(fake);
  const model = testModel();
  const { unmount } = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-delayed-unmount" focused kind="details" />,
  );

  unmount();
  act(() => fake.emitReady());
  await act(async () => {
    await Promise.resolve();
  });

  expect(threadsStore.getState().threads.has(model.ref)).toBe(false);
  expect(threadsStore.getState().frameTimes.has(model.ref)).toBe(false);
  expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
});

test("ordinary body remount does not move focus into the scaffold", () => {
  const model = testModel();
  seedModel(model);
  const first = render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-focus" focused kind="details" />);
  document.body.focus();
  first.unmount();
  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-focus-2" focused kind="details" />);

  expect(document.activeElement).not.toBe(screen.getByRole("heading", { name: /details/i }));
});

test("Activity pane rebuilds typed rows on remount while retaining ref-qualified fold disclosure", async () => {
  const fake = connectFakeClient(),
    model = testModel();
  seedModel(model);
  fake.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ ownerRef: ref, description: "completed retained row", terminal: true, status: "completed" })],
    page: { complete: true, issues: [] },
  }));
  fake.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  fake.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [],
    page: { complete: true, issues: [] },
  }));
  const first = render(<SessionPanelPane params={{ ref: model.ref }} paneId="activity" focused kind="activity" />);
  const fold = await screen.findByRole("treeitem", { name: "1 inactive" });
  await userEvent.click(within(fold).getByRole("button"));
  expect(screen.getByText("completed retained row")).toBeTruthy();
  first.unmount();
  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="activity-remount" focused kind="activity" />);
  await screen.findByText("completed retained row");
  expect(screen.getByRole("treeitem", { name: "1 inactive" }).getAttribute("aria-expanded")).toBe("true");
});

test("standalone activity pane preserves typed watch countdown through the tree clock", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(new Date("2026-08-05T15:00:12Z"));
  const fake = connectFakeClient(),
    model = testModel();
  seedModel(model);
  fake.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [
      activityWatch(
        {
          id: "watch_open",
          note: "clock",
          cadence: [{ kind: "every", seconds: 600, derivedNextFireAt: "2026-08-05T15:04:12Z" }],
        },
        ref,
      ),
    ],
    page: { complete: true, issues: [] },
  }));
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="activity-watch" focused kind="activity" />);
  const row = await screen.findByRole("treeitem", { name: "Watch: clock" });
  expect(row.textContent).toContain("next ~4m");
  act(() => vi.advanceTimersByTime(60000));
  expect(row.textContent).toContain("next ~3m");
});
