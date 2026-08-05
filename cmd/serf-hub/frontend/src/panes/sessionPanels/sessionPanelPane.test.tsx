import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import type { ThreadModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../protocol/types.gen";
import { type ActivityPanelEntry, activityPanelStore } from "../../stores/activityPanel";
import { activitySummaryStore } from "../../stores/activitySummary";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import type { ActivityTree } from "../session/chrome/activityData";
import { activityNodeID } from "../session/chrome/activityData";
import { SessionPanelPane } from "./SessionPanelPane";

vi.mock("../backToParentAction", () => ({
  BackToParentAction: () => <span data-testid="back-to-parent" />,
}));

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
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

function retainedActivity(): ActivityTree {
  return {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "session_a",
      ref: "ref_a",
      label: "Build session",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 0, complete: true },
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

test("renders retained Activity selection and expansion after a pane remount", () => {
  const model = testModel({ jobsUpdatedAt: 1 });
  const tree = retainedActivity();
  const rootID = activityNodeID(tree.root);
  const shellEntry = tree.root.entries[0];
  if (!shellEntry) throw new Error("retained activity fixture has no shell entry");
  const shellID = activityNodeID(shellEntry);
  const entry: ActivityPanelEntry = {
    load: { kind: "ready", tree },
    disclosure: { expandedIDs: [rootID], selectedID: shellID, selectionPruned: false, tree },
    established: true,
    continuationFailures: {},
    requestID: 0,
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
  const firstRow = screen.getByRole("treeitem", { name: /compile retained shell/i });
  expect(firstRow.getAttribute("aria-selected")).toBe("true");
  first.unmount();

  seedModel(model);
  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-activity-2" focused kind="activity" />);
  const remountedRow = screen.getByRole("treeitem", { name: /compile retained shell/i });
  expect(remountedRow.getAttribute("aria-selected")).toBe("true");
  expect(screen.getByRole("tree")).toBeTruthy();
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

test("Details owns a clock after hydration", () => {
  vi.useFakeTimers();
  const start = new Date("2026-08-05T00:00:00.000Z");
  vi.setSystemTime(start);
  const model = testModel({ activeTurnStartedAt: start.toISOString(), workMillis: 1_000 });
  seedModel(model);

  render(<SessionPanelPane params={{ ref: model.ref }} paneId="panel-details" focused kind="details" />);
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("1s");

  act(() => vi.advanceTimersByTime(3_000));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("4s");
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
