import { cleanup, render, screen } from "@testing-library/react";
import type { IDockviewPanelHeaderProps } from "dockview-core";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../protocol/model";
import type { ThreadCapabilities, ThreadStatus } from "../protocol/types.gen";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { PaneTab } from "./PaneTab";
import type { PanePanelParams } from "./workspace";

// This suite exercises the tab's status dot only - title rendering and the
// close affordance are dockview's own DockviewDefaultTab, exercised by
// DockHost.test.tsx's live-host tests.

const NO_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  queue: false,
  goal: false,
  rename: false,
};

function fixtureThread(ref: string, status: ThreadStatus): ThreadModel {
  return {
    ref,
    threadId: `thr_${ref}`,
    name: `Thread ${ref}`,
    status,
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: NO_CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    jobsTreeRevision: null,
  };
}

// A minimal IDockviewPanelHeaderProps stand-in - PaneTab reads api.title (via
// DockviewDefaultTab's own useTitle hook) and params off the (much wider)
// real props, same loose-cast technique PopoutHeaderAction.test.tsx uses for
// DockviewApi.
function tabProps(params: PanePanelParams, title = "a pane"): IDockviewPanelHeaderProps<PanePanelParams> {
  return {
    api: { title, onDidTitleChange: () => ({ dispose: () => {} }) },
    containerApi: {},
    params,
    tabLocation: "header",
  } as unknown as IDockviewPanelHeaderProps<PanePanelParams>;
}

beforeEach(() => {
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("renders the panel's title", () => {
  render(<PaneTab {...tabProps({ paneType: "doc", paneParams: { ref: "doc_a" } }, "A document")} />);
  expect(screen.getByText("A document")).toBeTruthy();
});

test("shows no status dot for a non-session pane", () => {
  render(<PaneTab {...tabProps({ paneType: "doc", paneParams: { ref: "doc_a" } })} />);
  expect(screen.queryByRole("img")).toBeNull();
});

test("shows no status dot for a session pane with no tracked thread", () => {
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_untracked" } })} />);
  expect(screen.queryByRole("img")).toBeNull();
});

test("shows no status dot when the thread is idle", () => {
  threadsStore.setState({ threads: new Map([["ref_a", fixtureThread("ref_a", { type: "idle" })]]) });
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_a" } })} />);
  expect(screen.queryByRole("img")).toBeNull();
});

test("shows no status dot when the thread has ended", () => {
  threadsStore.setState({ threads: new Map([["ref_a", fixtureThread("ref_a", { type: "closed" })]]) });
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_a" } })} />);
  expect(screen.queryByRole("img")).toBeNull();
});

test("shows a 'needs you' dot when the thread is awaiting", () => {
  threadsStore.setState({ threads: new Map([["ref_a", fixtureThread("ref_a", { type: "awaiting" })]]) });
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_a" } })} />);
  expect(screen.getByRole("img", { name: "Needs you" })).toBeTruthy();
});

test("shows a 'working' dot when the thread is active", () => {
  threadsStore.setState({ threads: new Map([["ref_a", fixtureThread("ref_a", { type: "active" })]]) });
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_a" } })} />);
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
});

test("shows a 'failed' dot when the thread has a system error", () => {
  threadsStore.setState({ threads: new Map([["ref_a", fixtureThread("ref_a", { type: "systemError" })]]) });
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_a" } })} />);
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test("the dot re-renders when the thread's status changes, with no remount", async () => {
  threadsStore.setState({ threads: new Map([["ref_a", fixtureThread("ref_a", { type: "idle" })]]) });
  render(<PaneTab {...tabProps({ paneType: "session", paneParams: { ref: "ref_a" } })} />);
  expect(screen.queryByRole("img")).toBeNull();

  threadsStore.setState((s) => {
    const next = new Map(s.threads);
    next.set("ref_a", { ...next.get("ref_a")!, status: { type: "awaiting" } });
    return { threads: next };
  });
  expect(await screen.findByRole("img", { name: "Needs you" })).toBeTruthy();
});
