// ActivityPanelBody plumbing: the session's watches reach the tree, and an
// idle session whose only pending work is a watch still shows its rows instead
// of the "no retained activity" empty state.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { NavigationWatchSummary, ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { ActivityPanelBody } from "./ActivityPanel";

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
  rename: true,
};

const NOW = Date.parse("2026-08-05T15:00:12.000Z");

function testModel(ref: string): ThreadModel {
  return {
    ref,
    threadId: `thr_${ref}`,
    name: "",
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
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
  };
}

function emptyTree(ref: string) {
  return {
    revision: 1,
    root: {
      sessionId: `sess_${ref}`,
      ref,
      label: "Root session",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function watch(overrides: Partial<NavigationWatchSummary> = {}): NavigationWatchSummary {
  return {
    id: "watch_1",
    source: "sess_root",
    deliveries: 0,
    created_at: "2026-08-05T12:48:00Z",
    active: true,
    ...overrides,
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

describe("ActivityPanelBody watches", () => {
  test("the session's watches render as rows inside the panel", async () => {
    const ref = "ref_watch_plumb";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => ({ data: emptyTree(ref) }));

    render(
      <ActivityPanelBody
        sessionRef={ref}
        model={testModel(ref)}
        now={NOW}
        watches={[
          watch({ id: "watch_plumb", note: "Poll the queue depth", cadence: [{ kind: "every", seconds: 600 }] }),
        ]}
      />,
    );

    await screen.findByRole("tree");
    expect(screen.getByRole("treeitem", { name: "Watch: Poll the queue depth" })).toBeTruthy();
    expect(screen.getByTestId("watch-facts").textContent).toContain("Fires every 10m");
    expect(screen.queryByText("No retained activity yet")).toBeNull();
  });

  test("an idle session with no watches and no activity keeps the empty state", async () => {
    const ref = "ref_watch_empty";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => ({ data: emptyTree(ref) }));

    render(<ActivityPanelBody sessionRef={ref} model={testModel(ref)} now={NOW} watches={undefined} />);

    await screen.findByText("No retained activity yet");
    expect(screen.queryByTestId("watch-group")).toBeNull();
  });
});
