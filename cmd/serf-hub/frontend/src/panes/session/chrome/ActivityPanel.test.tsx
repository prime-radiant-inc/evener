import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { activityPanelStore } from "../../../stores/activityPanel";
import { activitySummaryStore } from "../../../stores/activitySummary";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
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
  queue: true,
  goal: true,
  rename: true,
};

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_root",
    threadId: "thr_root",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision,
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
    ...rest,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function activityTree(revision = 1) {
  return {
    revision,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root session",
      aggregate: "running",
      counts: { active: 3, failed: 1, completed: 4, complete: true },
      entries: [
        {
          kind: "shell",
          job: {
            jobId: "job_root_shell",
            ownerSessionId: "sess_root",
            ownerRef: "ref_root",
            type: "shell",
            status: "running",
            terminal: false,
            background: false,
            hasOutput: true,
            description: "compile root shell",
            command: "npm test",
            startedAt: "2026-08-03T00:00:00Z",
            outputBytes: 11,
          },
        },
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_active",
            childSessionId: "sess_child",
            childRef: "ref_child",
            mandate: "Inspect the repo",
            turns: [
              {
                jobId: "job_delegate_turn_1",
                ownerSessionId: "sess_root",
                ownerRef: "ref_root",
                type: "delegate",
                status: "running",
                terminal: false,
                background: true,
                hasOutput: false,
                description: "delegate started",
                startedAt: "2026-08-03T00:01:00Z",
                outputBytes: 0,
              },
              {
                jobId: "job_delegate_turn_2",
                ownerSessionId: "sess_root",
                ownerRef: "ref_root",
                type: "delegate",
                status: "failed",
                terminal: true,
                background: true,
                hasOutput: true,
                description: "delegate report",
                startedAt: "2026-08-03T00:02:00Z",
                endedAt: "2026-08-03T00:03:00Z",
                outputBytes: 9,
              },
            ],
            child: {
              sessionId: "sess_child",
              ref: "ref_child",
              label: "Child session",
              aggregate: "running",
              counts: { active: 1, failed: 0, completed: 1, complete: true },
              entries: [
                {
                  kind: "shell",
                  job: {
                    jobId: "job_child_shell",
                    ownerSessionId: "sess_child",
                    ownerRef: "ref_child",
                    type: "shell",
                    status: "quarantined",
                    terminal: false,
                    background: false,
                    hasOutput: false,
                    description: "child shell",
                    command: "make test",
                    startedAt: "2026-08-03T00:04:00Z",
                    outputBytes: 0,
                  },
                },
              ],
              branch: {},
            },
            branch: {},
          },
        },
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_completed",
            childSessionId: "sess_done",
            childRef: "ref_done",
            turns: [
              {
                jobId: "job_completed_turn",
                ownerSessionId: "sess_root",
                ownerRef: "ref_root",
                type: "delegate",
                status: "completed",
                terminal: true,
                background: true,
                hasOutput: false,
                description: "completed branch",
                startedAt: "2026-08-03T00:05:00Z",
                endedAt: "2026-08-03T00:06:00Z",
                outputBytes: 0,
              },
            ],
            child: {
              sessionId: "sess_done",
              ref: "ref_done",
              label: "Done session",
              aggregate: "completed",
              counts: { active: 0, failed: 0, completed: 1, complete: true },
              entries: [],
              branch: {},
            },
            branch: {},
          },
        },
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_partial",
            childSessionId: "sess_partial",
            childRef: "ref_partial",
            mandate: "Continue retained branch",
            turns: [
              {
                jobId: "job_partial_turn",
                ownerSessionId: "sess_root",
                ownerRef: "ref_root",
                type: "delegate",
                status: "completed",
                terminal: true,
                background: true,
                hasOutput: true,
                description: "partial delegate report",
                startedAt: "2026-08-03T00:07:00Z",
                endedAt: "2026-08-03T00:08:00Z",
                outputBytes: 13,
              },
            ],
            child: {
              sessionId: "sess_partial",
              ref: "ref_partial",
              label: "Partial session",
              aggregate: "completed",
              counts: { active: 0, failed: 0, completed: 2, complete: false },
              entries: [],
              branch: { truncated: true, continuation: "partial-page-2", error: "child unavailable" },
            },
            branch: { error: "child unavailable" },
          },
        },
      ],
      branch: {},
    },
  };
}

function continuedPartialTree() {
  return {
    revision: 2,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root session",
      aggregate: "running",
      counts: { active: 3, failed: 1, completed: 4, complete: true },
      entries: [
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_partial",
            childSessionId: "sess_partial",
            childRef: "ref_partial",
            turns: [
              {
                jobId: "job_partial_turn",
                ownerSessionId: "sess_root",
                ownerRef: "ref_root",
                type: "delegate",
                status: "completed",
                terminal: true,
                background: true,
                hasOutput: true,
                description: "partial delegate report",
                startedAt: "2026-08-03T00:07:00Z",
                endedAt: "2026-08-03T00:08:00Z",
                outputBytes: 13,
              },
            ],
            child: {
              sessionId: "sess_partial",
              ref: "ref_partial",
              label: "Partial session",
              aggregate: "running",
              counts: { active: 1, failed: 0, completed: 2, complete: true },
              entries: [
                {
                  kind: "shell",
                  job: {
                    jobId: "job_partial_shell",
                    ownerSessionId: "sess_partial",
                    ownerRef: "ref_partial",
                    type: "shell",
                    status: "running",
                    terminal: false,
                    background: false,
                    hasOutput: false,
                    description: "continued shell",
                    startedAt: "2026-08-03T00:09:00Z",
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

function emptyTree() {
  return {
    revision: 1,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root session",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function installMatchMediaStub(initialMatches: boolean) {
  class FakeMediaQueryList {
    matches: boolean;
    media: string;
    private listeners = new Set<(event: MediaQueryListEvent) => void>();

    constructor(media: string, matches: boolean) {
      this.media = media;
      this.matches = matches;
    }

    addEventListener(type: string, listener: (event: MediaQueryListEvent) => void): void {
      if (type === "change") this.listeners.add(listener);
    }

    removeEventListener(type: string, listener: (event: MediaQueryListEvent) => void): void {
      if (type === "change") this.listeners.delete(listener);
    }

    emit(matches: boolean): void {
      this.matches = matches;
      for (const listener of this.listeners) listener({ matches } as MediaQueryListEvent);
    }
  }

  const lists = new Map<string, FakeMediaQueryList>();
  window.matchMedia = vi.fn((query: string) => {
    let list = lists.get(query);
    if (!list) {
      list = new FakeMediaQueryList(query, initialMatches);
      lists.set(query, list);
    }
    return list as unknown as MediaQueryList;
  }) as unknown as typeof window.matchMedia;
  return lists;
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

function cloneFixture<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
  installMatchMediaStub(false);
});

afterEach(() => {
  cleanup();
  // @ts-expect-error restore jsdom baseline
  delete window.matchMedia;
});

describe("ActivityPanel", () => {
  test("starts with Activity, fetches on open, shows loading, then badges complete active count", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    const gate = deferred<{ data: unknown }>();
    fake.on("serf/jobs/list", () => gate.promise);

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    expect(screen.getByRole("button", { name: "Activity" })).toBeTruthy();
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Activity" }));
    expect(await screen.findByText("Loading activity…")).toBeTruthy();
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1);

    act(() => gate.resolve({ data: activityTree() }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).not.toBe("");
    await screen.findByRole("tree");
    expect(screen.getByRole("button", { name: "Activity · 3" })).toBeTruthy();
  });

  test("establishes a failed first attempt and does not retry the same bump while closed", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => {
      throw new Error("first activity failure");
    });

    render(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: 1 })} now={0} />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Activity" }));
    expect(await screen.findByRole("button", { name: "Try again" })).toBeTruthy();
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1);
  });

  test("keeps the badge bare when the root counts are incomplete", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    const incomplete = activityTree();
    incomplete.root.counts.complete = false;
    fake.on("serf/jobs/list", () => ({ data: incomplete }));

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");

    expect(screen.getByRole("button", { name: "Activity" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Activity · 3" })).toBeNull();
  });

  test("renders empty, unsupported, and exited states", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: emptyTree() }));

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: /Activity/ }));
    expect(await screen.findByText("No retained activity yet")).toBeTruthy();

    cleanup();
    resetThreadsStoreForTests();
    const unsupported = connectFakeClient();
    unsupported.on("serf/jobs/list", () => ({ data: null }));
    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: /Activity/ }));
    expect(await screen.findByText(/activity isn't available/i)).toBeTruthy();

    cleanup();
    resetThreadsStoreForTests();
    const ended = connectFakeClient();
    ended.on("serf/jobs/list", () => {
      throw new WireError("thread not found: thr_root", -32014, { serfErrorInfo: "sessionUnavailable" });
    });
    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: /Activity/ }));
    expect(await screen.findByText("This session has ended")).toBeTruthy();
  });

  test("renders live rows plus a fold row for inactive entries, and an expanded fold survives refresh", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", ({ continuation }) => ({
      data: continuation ? continuedPartialTree() : activityTree(1),
    }));

    const panel = (bump: number | null) => (
      <ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: bump })} now={0} />
    );
    const { rerender } = render(panel(1));
    await user.click(screen.getByRole("button", { name: "Activity" }));

    expect(await screen.findByRole("tree")).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /inspect the repo/i })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /child shell/i })).toBeTruthy();
    // Inactive delegates sit behind the root session's fold row, folded by default.
    const fold = screen.getByRole("treeitem", { name: "2 inactive" });
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("treeitem", { name: /done session/i })).toBeNull();
    expect(screen.queryByTestId("activity-inspector")).toBeNull();

    await user.click(fold);
    expect(activityPanelStore.getState().entries.get("ref_root")?.expandedFoldIDs).toEqual([
      "session:sess_root:inactive-fold",
    ]);
    expect(screen.getByRole("treeitem", { name: /done session/i })).toBeTruthy();

    rerender(panel(2));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2));

    expect(activityPanelStore.getState().entries.get("ref_root")?.expandedFoldIDs).toEqual([
      "session:sess_root:inactive-fold",
    ]);
    expect(screen.getByRole("treeitem", { name: /done session/i })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();

    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    expect(activityPanelStore.getState().entries.get("ref_root")?.expandedFoldIDs).toEqual([]);
    expect(screen.queryByRole("treeitem", { name: /done session/i })).toBeNull();
  });

  test("a stale refresh failure keeps the last good tree and shows a stale notice", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/jobs/list", () => {
      calls += 1;
      if (calls === 1) return { data: activityTree() };
      throw new Error("broken pipe");
    });

    const { rerender } = render(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: 1 })} now={0} />
        <Toast />
      </>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");

    rerender(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: 2 })} now={0} />
        <Toast />
      </>,
    );

    expect(await screen.findByText(/showing the last activity that loaded/i)).toBeTruthy();
    expect(screen.getByRole("tree")).toBeTruthy();
    expect(screen.getByText("compile root shell")).toBeTruthy();
  });

  test("refreshes while closed once established, but suppresses hidden-trigger refresh", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: activityTree() }));

    const visible = (bump: number | null) => (
      <ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: bump })} now={0} />
    );
    const { rerender } = render(visible(1));
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");
    await user.click(screen.getByRole("button", { name: "Close" }));
    rerender(visible(2));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2));

    const handle = createRef<ActivityPanelHandle>();
    const hidden = (bump: number | null) => (
      <ActivityPanel
        ref={handle}
        sessionRef="ref_root"
        model={testModel({ jobsUpdatedAt: bump })}
        now={0}
        hideTrigger
      />
    );
    cleanup();
    const hiddenRender = render(hidden(3));
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2);
    act(() => handle.current?.open());
    await screen.findByRole("tree");
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(3);
    await user.click(screen.getByRole("button", { name: "Close" }));
    hiddenRender.rerender(hidden(4));
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(3);
  });

  test("refreshes the visible badge when an open panel body is backgrounded", async () => {
    const fake = connectFakeClient();
    let calls = 0;
    const refreshed = cloneFixture(activityTree(2));
    refreshed.root.counts.active = 8;
    fake.on("serf/jobs/list", () => ({ data: calls++ === 0 ? activityTree() : refreshed }));

    const model = testModel({ jobsUpdatedAt: 1 });
    const { rerender } = render(
      <>
        <ActivityPanel sessionRef="ref_root" model={model} now={0} />
        <ActivityPanelBody sessionRef="ref_root" model={model} />
      </>,
    );
    await screen.findByRole("tree");
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1);

    rerender(<ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: 2 })} now={0} />);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2));
    expect(screen.getByRole("button", { name: "Activity · 8" })).toBeTruthy();
  });

  // jobsUpdatedAt only ever comes from live pushes and re-hydrates to null
  // after a thread-model eviction (protocol/reducer.ts), so a null bump can
  // never prove retained data is current: bumps that happened while nothing
  // held the model are simply gone. A remounting body must fetch in that case
  // instead of trusting null === null.
  test("remounting the body re-fetches when the model cannot prove freshness (null jobs bump)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: activityTree() }));

    const first = render(<ActivityPanelBody sessionRef="ref_null_bump" model={testModel()} />);
    await screen.findByRole("tree");
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1);
    first.unmount();

    render(<ActivityPanelBody sessionRef="ref_null_bump" model={testModel()} />);
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2));
  });

  // A MOUNTED body has the same blind spot across a wholesale model
  // rehydration (reconnect/resync): jobsUpdatedAt can be null before and
  // after, so no effect dependency changes even though activity missed in
  // the gap may be stale. The threads store's per-ref hydration generation
  // is the signal that the model was replaced underneath.
  test("a mounted body re-fetches when its model rehydrates without a provable bump", async () => {
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: activityTree() }));

    render(<ActivityPanelBody sessionRef="ref_rehydrated" model={testModel()} />);
    await screen.findByRole("tree");
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1);

    act(() => {
      threadsStore.setState({ hydrations: new Map([["ref_rehydrated", 2]]) });
    });
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(2));
  });

  // The closed Sheet's trigger owns background badge refresh; it has the same
  // null-to-null rehydration blind spot as a mounted body and watches the same
  // hydration generation.
  test("a closed panel's badge refresh notices rehydration when the bump stays null", async () => {
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: activityTree() }));
    activitySummaryStore.setState({
      entries: new Map([
        [
          "ref_gen",
          {
            counts: undefined,
            established: true,
            mountedBodies: 0,
            loading: false,
            lastFetchedBump: null,
            requestID: 1,
          },
        ],
      ]),
    });

    render(<ActivityPanel sessionRef="ref_gen" model={testModel({ ref: "ref_gen" })} now={0} />);
    await act(async () => Promise.resolve());
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(0);

    act(() => {
      threadsStore.setState({ hydrations: new Map([["ref_gen", 1]]) });
    });
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "serf/jobs/list")).toHaveLength(1));
  });

  test("does not let a continuation patch change the root badge summary", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", ({ continuation }) => {
      if (!continuation) return { data: activityTree() };
      const patch = cloneFixture(continuedPartialTree());
      patch.root.counts.active = 99;
      return { data: patch };
    });

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");
    expect(screen.getByRole("button", { name: "Activity · 3" })).toBeTruthy();
    // The partial branch's continuation strip follows its row, which sits
    // behind the folded-by-default inactive fold.
    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    await user.click(screen.getByRole("button", { name: /load more/i }));

    await screen.findByRole("treeitem", { name: /continued shell/i });
    expect(screen.getByRole("button", { name: "Activity · 3" })).toBeTruthy();
  });

  test("continuation grafts only the targeted branch", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", ({ continuation }) => ({ data: continuation ? continuedPartialTree() : activityTree() }));

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");

    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    await user.click(screen.getByRole("button", { name: /load more/i }));

    expect(await screen.findByRole("treeitem", { name: /continued shell/i })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();
    expect(fake.calls.filter((call) => call.method === "serf/jobs/list").at(-1)?.params).toEqual({
      ref: "ref_root",
      continuation: "partial-page-2",
    });
  });

  test("a malformed continuation response stays local to the targeted branch and preserves retry affordance", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", ({ continuation }) => ({ data: continuation ? null : activityTree() }));

    render(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");
    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    await user.click(screen.getByRole("button", { name: /load more/i }));

    expect(await screen.findByText("Couldn't load more retained activity for this branch.")).toBeTruthy();
    expect(screen.getByRole("tree")).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /load more/i })).toBeTruthy();
    expect(screen.queryByText(/activity isn't available/i)).toBeNull();
    expect(screen.queryByText(/showing the last activity that loaded/i)).toBeNull();
    expect(screen.queryByText(/couldn't load activity/i)).toBeNull();
  });

  test("a rejected continuation request stays local to the targeted branch without root stale or toast UI", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", ({ continuation }) => {
      if (continuation) throw new Error("branch boom");
      return { data: activityTree() };
    });

    render(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");
    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    await user.click(screen.getByRole("button", { name: /load more/i }));

    expect(await screen.findByText("Couldn't load more retained activity for this branch: branch boom")).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /load more/i })).toBeTruthy();
    expect(screen.queryByText(/showing the last activity that loaded/i)).toBeNull();
    expect(screen.queryByText(/couldn't load activity/i)).toBeNull();
  });

  test("a refresh that drops a retained row keeps rendering the surviving tree", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    let calls = 0;
    fake.on("serf/jobs/list", () => {
      calls += 1;
      if (calls === 1) return { data: activityTree() };
      const next = activityTree(2);
      next.root.entries = next.root.entries.filter(
        (entry) => !(entry.kind === "shell" && entry.job && entry.job.jobId === "job_root_shell"),
      );
      return { data: next };
    });

    const panel = (bump: number | null) => (
      <ActivityPanel sessionRef="ref_root" model={testModel({ jobsUpdatedAt: bump })} now={0} />
    );
    const { rerender } = render(panel(1));
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");
    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();

    rerender(panel(2));
    await waitFor(() => expect(screen.queryByRole("treeitem", { name: /compile root shell/i })).toBeNull());
    expect(screen.getByRole("tree")).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /inspect the repo/i })).toBeTruthy();
  });

  test("renders dense tree rows with no inspector element", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: activityTree() }));

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");

    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: /inspect the repo/i })).toBeTruthy();
    expect(screen.queryByTestId("activity-inspector")).toBeNull();
    expect(screen.queryByText(/select activity/i)).toBeNull();
  });

  test("mobile renders the tree directly with no inspector swap or back button", async () => {
    const user = userEvent.setup();
    installMatchMediaStub(true);
    const fake = connectFakeClient();
    fake.on("serf/jobs/list", () => ({ data: activityTree() }));

    render(<ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />);
    await user.click(screen.getByRole("button", { name: "Activity" }));
    await screen.findByRole("tree");

    expect(screen.getByRole("treeitem", { name: /compile root shell/i })).toBeTruthy();
    expect(screen.getByRole("treeitem", { name: "2 inactive" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Back to activity" })).toBeNull();
    expect(screen.queryByTestId("activity-inspector")).toBeNull();
  });

  test("ignores stale fetch results after the session ref changes", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    const first = deferred<{ data: unknown }>();
    fake.on("serf/jobs/list", ({ ref }) => {
      if (ref === "ref_root") return first.promise;
      return Promise.resolve({ data: emptyTree() });
    });

    const { rerender } = render(<ActivityPanel sessionRef="ref_root" model={testModel({ ref: "ref_root" })} now={0} />);
    await user.click(screen.getByRole("button", { name: "Activity" }));
    rerender(<ActivityPanel sessionRef="ref_other" model={testModel({ ref: "ref_other" })} now={0} />);
    act(() => first.resolve({ data: activityTree() }));

    await user.click(screen.getByRole("button", { name: "Activity" }));
    expect(await screen.findByText("No retained activity yet")).toBeTruthy();
    expect(screen.queryByText("compile root shell")).toBeNull();
  });

  test("ignores a deferred root listJobs rejection after unmount without emitting a toast", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    const gate = deferred<{ data: unknown }>();
    fake.on("serf/jobs/list", () => gate.promise);

    const { rerender } = render(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Activity" }));
    rerender(<Toast />);

    await act(async () => {
      gate.reject(new Error("late root rejection"));
      await Promise.resolve();
    });

    expect(screen.queryByText(/late root rejection/i)).toBeNull();
    expect(screen.queryByText(/couldn't load activity/i)).toBeNull();
  });

  test("ignores a deferred root listJobs resolution after unmount", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    const gate = deferred<{ data: unknown }>();
    fake.on("serf/jobs/list", () => gate.promise);

    const { rerender } = render(
      <>
        <ActivityPanel sessionRef="ref_root" model={testModel()} now={0} />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Activity" }));
    rerender(<Toast />);

    await act(async () => {
      gate.resolve({ data: activityTree() });
      await Promise.resolve();
    });

    expect(screen.queryByText(/compile root shell/i)).toBeNull();
    expect(screen.queryByText(/couldn't load activity/i)).toBeNull();
  });
});
