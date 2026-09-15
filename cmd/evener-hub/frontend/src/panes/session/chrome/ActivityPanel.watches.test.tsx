// ActivityPanelBody plumbing: the session's watches reach the tree, and an
// idle session whose only pending work is a watch still shows its rows instead
// of the "no retained activity" empty state.
import * as activityRows from "@evener/appwire-client";
import { act, cleanup, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { NavigationWatchSummary, ThreadCapabilities } from "../../../protocol/types.gen";
import { activityPanelStore, resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { ActivityPanel, ActivityPanelBody, type ActivityPanelHandle } from "./ActivityPanel";

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
  resetActivityPanelStoreForTests();
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

    render(<ActivityPanelBody sessionRef={ref} model={testModel(ref)} watches={undefined} />);

    await screen.findByText("No retained activity yet");
    expect(screen.queryByTestId("watch-group")).toBeNull();
  });

  test("a session whose watches were all omitted still shows the watch group, not the empty state", async () => {
    // The hub caps the watch list and reports the dropped rows as
    // omitted_watches. Emptiness of the RETAINED rows is not emptiness of the
    // session's watch content: the group and its "+N more" must render, or the
    // panel claims there is nothing while the rail row says otherwise.
    const ref = "ref_watch_omitted";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => ({ data: emptyTree(ref) }));

    render(<ActivityPanelBody sessionRef={ref} model={testModel(ref)} watches={undefined} omittedWatches={4} />);

    await screen.findByTestId("watch-group");
    expect(screen.getByText("0 armed total · +4 more")).toBeTruthy();
    expect(screen.queryByText("No retained activity yet")).toBeNull();
  });

  test("the Watches header reports the armed total across omitted rows", async () => {
    // More armed watches than the hub's per-session cap: 32 retained, 8 omitted
    // and armed. The header must state the true armed total, not the retained
    // subset, and make clear it covers the omitted rows.
    const ref = "ref_watch_omitted_armed";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => ({ data: emptyTree(ref) }));

    render(
      <ActivityPanelBody
        sessionRef={ref}
        model={testModel(ref)}
        watches={Array.from({ length: 32 }, (_, i) => watch({ id: `armed-${i}` }))}
        omittedWatches={8}
        omittedArmedWatches={8}
      />,
    );

    await screen.findByTestId("watch-group");
    expect(screen.getByText("40 armed total · +8 more")).toBeTruthy();
  });

  test("a failed activity load still shows the watches the rail counts", async () => {
    // The watches are session data, not retained activity: a load that failed
    // must not hide them, because an armed watch is often the only pending work
    // the session has -- the exact case the rail's count exists to surface.
    const ref = "ref_watch_failed_load";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => {
      throw new Error("hub is down");
    });

    render(
      <ActivityPanelBody
        sessionRef={ref}
        model={testModel(ref)}
        watches={[
          watch({ id: "watch_failed_load", note: "Poll the queue depth", cadence: [{ kind: "every", seconds: 600 }] }),
        ]}
      />,
    );

    expect(await screen.findByRole("treeitem", { name: "Watch: Poll the queue depth" })).toBeTruthy();
    expect(screen.getByTestId("watch-facts").textContent).toContain("Fires every 10m");
  });

  test("a still-loading activity request does not hide the watches", async () => {
    // Same contract as the failed load: the watches are session data, and a
    // request that has not answered yet must not hide the only pending work the
    // session has.
    const ref = "ref_watch_loading";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => new Promise(() => {}));

    render(
      <ActivityPanelBody
        sessionRef={ref}
        model={testModel(ref)}
        watches={[
          watch({ id: "watch_loading", note: "Poll the queue depth", cadence: [{ kind: "every", seconds: 600 }] }),
        ]}
      />,
    );

    expect(screen.getByText("Loading activity…")).toBeTruthy();
    expect(await screen.findByRole("treeitem", { name: "Watch: Poll the queue depth" })).toBeTruthy();
  });

  test("an ended session with no retained tree still shows its watches", async () => {
    // The ended-and-no-tree state is the one where the tree is gone for good, so
    // the watch rows cannot come from retained activity at all.
    const ref = "ref_watch_ended";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    // The request never answers, so nothing can replace the ended result with a
    // retained tree: this is the session whose activity is gone for good.
    fake.on("evener/jobs/list", () => new Promise(() => {}));

    render(
      <ActivityPanelBody
        sessionRef={ref}
        model={testModel(ref)}
        watches={[watch({ id: "watch_ended", note: "Deploy watch" })]}
      />,
    );
    const request = activityPanelStore.getState().beginFetch(ref);
    activityPanelStore.getState().publishFetch(ref, request, { kind: "ended" });

    expect(await screen.findByText("This session has ended")).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: "Watch: Deploy watch" })).toBeTruthy();
  });

  // The chrome hands the panel its own ticking clock as `now`. The panel must
  // not thread that clock into the tree: the tree owns TreeNowContext and only
  // its live-duration leaves consume it, so a tick of the chrome clock must
  // leave the whole row list untouched. Before the fix the changed `now` prop
  // reached WatchRowView and re-rendered the row (and its watchMeta) every tick.
  test("the chrome's ticking clock does not re-render the watch rows", async () => {
    const ref = "ref_watch_tick";
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("evener/jobs/list", () => ({ data: emptyTree(ref) }));

    const watches = [
      watch({ id: "watch_tick", note: "Poll the queue depth", cadence: [{ kind: "every", seconds: 600 }] }),
    ];
    // One model object across both renders, so a re-render can only come from
    // the clock changing, never from a fresh prop identity.
    const model = testModel(ref);
    const handle = createRef<ActivityPanelHandle>();
    const panel = (now: number) => (
      <ActivityPanel ref={handle} sessionRef={ref} model={model} now={now} watches={watches} hideTrigger />
    );
    const { rerender } = render(panel(NOW));
    act(() => handle.current?.open());
    await screen.findByRole("treeitem", { name: "Watch: Poll the queue depth" });

    // WatchRowView renders its right-hand meta through watchMeta, so a row
    // re-render shows up here. Spy after the tree has settled so the count
    // measures only the clock tick below.
    const rowRender = vi.spyOn(activityRows, "watchMeta");
    const atRest = rowRender.mock.calls.length;

    act(() => {
      rerender(panel(NOW + 3000));
    });

    expect(rowRender.mock.calls.length).toBe(atRest);
  });
});
