import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import type { ThreadModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { type ActivityPanelEntry, activityPanelStore } from "../../stores/activityPanel";
import { activitySummaryStore } from "../../stores/activitySummary";
import { connectionStore } from "../../stores/connection";
import { tasksPanelStore } from "../../stores/tasksPanel";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import type { ActivityTree } from "../session/chrome/activityData";
import { NOW_TICK_MS } from "../session/liveness";
import { sessionPanelTitle } from "./index";
import { SessionPanelPane } from "./SessionPanelPane";

vi.mock("../backToParentAction", () => ({
  BackToParentAction: () => <span data-testid="back-to-parent" />,
}));

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetDisclosureStoreForTests();
});

afterEach(() => {
  cleanup();
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
  queue: true,
  goal: true,
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
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
      source: "serf",
      serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
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

function activityRootTree(): ActivityTree {
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
          kind: "delegate",
          delegate: {
            delegateId: "delegate_partial",
            childSessionId: "session_partial",
            childRef: "ref_partial",
            mandate: "Continue retained branch",
            turns: [
              {
                jobId: "delegate_turn",
                ownerSessionId: "session_a",
                ownerRef: "ref_a",
                type: "delegate",
                status: "completed",
                terminal: true,
                background: true,
                hasOutput: true,
                description: "partial delegate report",
                startedAt: "2026-08-05T00:01:00Z",
                endedAt: "2026-08-05T00:02:00Z",
                outputBytes: 4,
              },
            ],
            child: {
              kind: "session",
              sessionId: "session_partial",
              ref: "ref_partial",
              label: "Partial session",
              aggregate: "running",
              counts: { active: 1, failed: 0, completed: 1, complete: false },
              entries: [],
              branch: { truncated: true, continuation: "page-2", error: "child unavailable" },
            },
            branch: {},
          },
        },
      ],
      branch: {},
    },
  };
}

function continuedActivityTree(): ActivityTree {
  return {
    revision: 2,
    root: {
      kind: "session",
      sessionId: "session_a",
      ref: "ref_a",
      label: "Build session",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 1, complete: true },
      entries: [
        {
          kind: "delegate",
          delegate: {
            delegateId: "delegate_partial",
            childSessionId: "session_partial",
            childRef: "ref_partial",
            mandate: "Continue retained branch",
            turns: [
              {
                jobId: "delegate_turn",
                ownerSessionId: "session_a",
                ownerRef: "ref_a",
                type: "delegate",
                status: "completed",
                terminal: true,
                background: true,
                hasOutput: true,
                description: "partial delegate report",
                startedAt: "2026-08-05T00:01:00Z",
                endedAt: "2026-08-05T00:02:00Z",
                outputBytes: 4,
              },
            ],
            child: {
              kind: "session",
              sessionId: "session_partial",
              ref: "ref_partial",
              label: "Partial session",
              aggregate: "running",
              counts: { active: 1, failed: 0, completed: 1, complete: true },
              entries: [
                {
                  kind: "shell",
                  job: {
                    jobId: "continued_shell",
                    ownerSessionId: "session_partial",
                    ownerRef: "ref_partial",
                    type: "shell",
                    status: "running",
                    terminal: false,
                    background: false,
                    hasOutput: false,
                    description: "continued shell",
                    startedAt: "2026-08-05T00:03:00Z",
                    outputBytes: 0,
                  },
                },
              ],
              branch: {},
            },
            branch: {},
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
  expect(screen.getByTestId("back-to-parent")).toBeTruthy();
});

test("keeps the scaffold heading consistent with the registered title after rename", () => {
  const model = testModel({ name: "Initial name" });
  seedModel(model);
  const { rerender } = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-title" focused kind="details" />,
  );
  expect(screen.getByRole("heading", { name: sessionPanelTitle("details", model.ref, model.name) })).toBeTruthy();

  const renamed = testModel({ name: "Renamed session" });
  seedModel(renamed);
  rerender(<SessionPanelPane params={{ ref: renamed.ref }} paneId="panel-title" focused kind="details" />);
  expect(screen.getByRole("heading", { name: sessionPanelTitle("details", renamed.ref, renamed.name) })).toBeTruthy();
});

test("mounts Tasks body and fetches after the model is hydrated", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({
    data: [{ id: 1, type: "verify", description: "Run checks", prompt: "", status: "open" }],
  }));
  const model = testModel({ tasks: { total: 1, done: 0 } });
  seedModel(model);

  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks" focused kind="tasks" />);

  expect(await screen.findByText("Run checks")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "serf/tasks/list")).toHaveLength(1);
});

test("retains Tasks rows and disclosure state across a pane remount", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: RETAINED_TASKS }));
  const model = testModel({ tasks: { total: 1, done: 0 } });
  seedModel(model);

  const first = render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks" focused kind="tasks" />);
  const summary = await screen.findByText("Retain the first task");
  await userEvent.click(summary);
  expect(screen.getByTestId("task-detail-prompt")).toBeTruthy();
  first.unmount();

  expect(tasksPanelStore.getState().entries.get(model.ref)?.rows).toHaveLength(1);
  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks-remount" focused kind="tasks" />);

  expect(await screen.findByText("Retain the first task")).toBeTruthy();
  expect(screen.getByTestId("task-detail-prompt")).toBeTruthy();
});

test("renders retained Activity rows and expanded fold state after a pane remount", () => {
  const model = testModel({ jobsUpdatedAt: 1 });
  const tree = retainedActivity();
  const entry: ActivityPanelEntry = {
    load: { kind: "ready", tree },
    disclosure: { expandedIDs: [], selectedID: undefined, selectionPruned: false, tree },
    established: true,
    continuationFailures: {},
    requestID: 0,
    expandedFoldIDs: ["session:session_a:inactive-fold"],
  };
  activityPanelStore.setState({ entries: new Map([[model.ref, entry]]) });
  activitySummaryStore.setState({
    entries: new Map([
      [
        model.ref,
        {
          counts: tree.root.counts,
          established: true,
          mountedBodies: 0,
          loading: false,
          lastFetchedBump: model.jobsUpdatedAt,
          requestID: 1,
        },
      ],
    ]),
  });
  seedModel(model);

  const first = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity" focused kind="activity" />,
  );
  expect(screen.getByRole("treeitem", { name: /compile retained shell/i })).toBeTruthy();
  expect(screen.getByRole("treeitem", { name: "1 inactive" }).getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByRole("treeitem", { name: /retained done shell/i })).toBeTruthy();
  first.unmount();

  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity-2" focused kind="activity" />);
  expect(screen.getByRole("treeitem", { name: /compile retained shell/i })).toBeTruthy();
  expect(screen.getByRole("treeitem", { name: "1 inactive" }).getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByRole("treeitem", { name: /retained done shell/i })).toBeTruthy();
});

test("renders daemon-gone state from the retained Tasks store result", async () => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => {
    throw new WireError("thread not found: thread_a", -32014, { serfErrorInfo: "sessionUnavailable" });
  });
  const model = testModel({ tasks: { total: 1, done: 0 } });
  seedModel(model);

  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-tasks" focused kind="tasks" />);

  expect(await screen.findByText("This session has ended")).toBeTruthy();
});

test.each(["tasks", "activity"] as const)("%s pane does not install the Details clock", async (kind) => {
  const fake = connectFakeClient();
  fake.on("serf/tasks/list", () => ({ data: [] }));
  fake.on("serf/jobs/list", () => ({ data: retainedActivity() }));
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

test("retains deferred Activity root completion after unmount and remount", async () => {
  const fake = connectFakeClient();
  const root = deferred<{ data: unknown }>();
  fake.on("serf/jobs/list", () => root.promise);
  const model = testModel({ jobsUpdatedAt: 1 });
  seedModel(model);

  const first = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity" focused kind="activity" />,
  );
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1));
  first.unmount();

  await act(async () => {
    root.resolve({ data: retainedActivity() });
    await Promise.resolve();
  });
  expect(activityPanelStore.getState().entries.get(model.ref)?.load.kind).toBe("ready");

  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity-remount" focused kind="activity" />);
  const row = await screen.findByRole("treeitem", { name: /compile retained shell/i });
  expect(row).toBeTruthy();

  await userEvent.click(screen.getByRole("treeitem", { name: "1 inactive" }));
  expect(activityPanelStore.getState().entries.get(model.ref)?.expandedFoldIDs).toEqual([
    "session:session_a:inactive-fold",
  ]);
  expect(screen.getByRole("treeitem", { name: /retained done shell/i })).toBeTruthy();
});

test("retains Activity continuation failure, retry, and graft across remounts", async () => {
  const fake = connectFakeClient();
  const root = deferred<{ data: unknown }>();
  const failedContinuation = deferred<{ data: unknown }>();
  const retriedContinuation = deferred<{ data: unknown }>();
  let continuationCalls = 0;
  fake.on("serf/jobs/list", ({ continuation }) => {
    if (!continuation) return root.promise;
    continuationCalls += 1;
    return continuationCalls === 1 ? failedContinuation.promise : retriedContinuation.promise;
  });
  const model = testModel({ jobsUpdatedAt: 1 });
  seedModel(model);

  const first = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity" focused kind="activity" />,
  );
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1));
  first.unmount();
  await act(async () => {
    root.resolve({ data: activityRootTree() });
    await Promise.resolve();
  });

  seedModel(model);
  const second = render(
    <SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity-second" focused kind="activity" />,
  );
  expect(await screen.findByRole("treeitem", { name: "Continue retained branch" })).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "Load more" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2));
  second.unmount();

  await act(async () => {
    failedContinuation.reject(new Error("continuation unavailable"));
    await Promise.resolve();
  });
  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity-failed" focused kind="activity" />);
  expect(await screen.findByText(/couldn't load more retained activity/i)).toBeTruthy();
  expect(screen.getByRole("treeitem", { name: "Continue retained branch" })).toBeTruthy();

  await userEvent.click(screen.getByRole("button", { name: "Load more" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(3));
  cleanup();
  await act(async () => {
    retriedContinuation.resolve({ data: continuedActivityTree() });
    await Promise.resolve();
  });

  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity-grafted" focused kind="activity" />);
  expect(await screen.findByRole("treeitem", { name: "Continue retained branch" })).toBeTruthy();
  expect(await screen.findByRole("treeitem", { name: /continued shell/i })).toBeTruthy();
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
