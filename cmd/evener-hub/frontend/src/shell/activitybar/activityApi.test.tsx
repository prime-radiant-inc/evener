import { WireError } from "@evener/appwire-client";
import { createNavigationStore } from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityJob,
  activityRef,
  activitySummary,
  activityWatch,
} from "../../stores/sessionActivityTestUtils";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { deriveScope } from "../statusbar/statusScope";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { AgentsTab } from "./AgentsTab";
import { ActivityBoundaryObserver } from "./activityPagingTestUtils";
import { JobsTab } from "./JobsTab";
import { WatchesTab } from "./WatchesTab";

beforeAll(async () => {
  await import("../../panes/session");
  await import("../../panes/transcript");
});
beforeEach(() => resetWorkspaceStoreForTests());

afterEach(() => {
  cleanup();
  resetConnectionStoreForTests();
  resetWorkspaceStoreForTests();
  resetDisclosureStoreForTests();
  vi.restoreAllMocks();
});
const NOW = Date.parse("2026-10-05T00:00:00Z");

it.each([
  { resource: "delegates" as const, Body: AgentsTab, total: 5 },
  { resource: "jobs" as const, Body: JobsTab, total: 6 },
  { resource: "watches" as const, Body: WatchesTab, total: 7 },
  { resource: "jobs" as const, Body: JobsTab, total: 301, expectedLines: 20 },
])(
  "$resource reserves its known direct-session total while the first page loads",
  async ({ resource, Body, total, expectedLines = total }) => {
    const client = activityClient();
    client.on("evener/thread/activity/read", ({ ref, scope }) => ({
      ...activitySummary(ref, scope),
      [resource]: { known: true, total, active: 0, completed: total, failed: 0 },
    }));
    let release: (() => void) | undefined;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    const methods = {
      delegates: "evener/thread/delegates/list",
      jobs: "evener/thread/jobs/list",
      watches: "evener/thread/watches/list",
    } as const;
    client.on(methods[resource], async ({ ref, scope }) => {
      await pending;
      const context = activityContext(ref);
      const page = { complete: true, issues: [] };
      if (resource === "delegates")
        return {
          context,
          scope: scope ?? "session",
          delegates: [activityDelegate({ ownerRef: ref, rootRef: ref })],
          page,
        };
      if (resource === "jobs")
        return { context, scope: scope ?? "session", jobs: [activityJob({ ownerRef: ref })], page };
      return {
        context,
        scope: scope ?? "session",
        watches: [activityWatch({}, ref)],
        page,
      };
    });
    connectionStore.getState().connect(client);
    render(<Body now={NOW} scope={scope()} />);
    expect(await screen.findByRole("status", { name: "Loading" })).toBeTruthy();
    expect(screen.getAllByTestId("skeleton-line")).toHaveLength(expectedLines);
    await act(async () => release?.());
    await waitFor(() => expect(screen.queryByRole("status", { name: "Loading" })).toBeNull());
    expect(document.querySelectorAll("[data-activity-anchor]")).toHaveLength(1);
  },
);

const scope = () =>
  deriveScope(
    createNavigationStore({ persistence: memoryNavigationPersistence() }).getState(),
    "remote:owner",
    null,
    null,
  );
it("shows incomplete empty as progress then drills the exact stable delegate child", async () => {
  const client = activityClient();
  let finish: (() => void) | undefined;
  client.on("evener/thread/delegates/list", async ({ ref, scope }) => {
    await new Promise<void>((resolve) => {
      finish = resolve;
    });
    return {
      context: activityContext(ref),
      scope: scope ?? "session",
      delegates: [activityDelegate({ delegateId: "raw-id", childRef: "other:opaque/child" })],
      page: { complete: true, issues: [] },
    };
  });
  connectionStore.getState().connect(client);
  const root = workspaceStore.getState().openPane("session", { ref: "remote:owner" });
  const source = workspaceStore.getState().mainPane();
  render(<AgentsTab now={NOW} scope={scope()} />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  expect(screen.queryByText(/No subagents/)).toBeNull();
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  finish?.();
  const inspect = await screen.findByRole("button", { name: /inspect/ });
  await act(async () => fireEvent.click(inspect));
  const cascade = workspaceStore.getState().panes.find((pane) => pane.type === "sessionZoom");
  expect(workspaceStore.getState().mainPane()).toBe(source);
  expect(cascade?.type).toBe("sessionZoom");
  expect(cascade?.slot).toBe("secondary");
  expect(cascade?.params).toEqual({
    ref: "other:opaque/child",
    source: { type: "session", params: { ref: "remote:owner" } },
    edges: [{ ownerRef: "remote:owner", childRef: "other:opaque/child", delegateId: "raw-id" }],
    inspection: { origin: { paneId: root, type: "session", ref: "remote:owner" } },
  });
  expect(cascade?.id).not.toBe(root);
  expect(workspaceStore.getState().focusedPaneId).toBe(cascade?.id);
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "transcript")).toHaveLength(0);
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(0);
});
it("opens job output using the supplied job transcript ref and raw owner", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ jobId: "raw-job", ownerRef: "source:owner", transcriptRef: "job:authoritative" })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<JobsTab now={NOW} scope={scope()} />);
  fireEvent.click(await screen.findByRole("button", { name: /run checks/ }));
  const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
  expect(opened).toMatchObject({
    slot: "secondary",
    params: { ref: "job:authoritative", parentRef: "source:owner" },
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(opened?.id);
});

it("visible page-boundary demand heals a failed continuation while the actual tab stays mounted", async () => {
  vi.useFakeTimers();
  const client = activityClient();
  let continuationReads = 0;
  let intersect: (() => void) | undefined;
  let observerCount = 0;
  class Observer {
    constructor(callback: IntersectionObserverCallback) {
      observerCount += 1;
      intersect = () =>
        callback([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
    }
    observe() {}
    disconnect() {}
  }
  vi.stubGlobal("IntersectionObserver", Observer);
  client.on("evener/thread/delegates/list", ({ cursor, ref, scope }) => {
    if (!cursor)
      return {
        context: activityContext(ref),
        scope: scope ?? "session",
        delegates: [activityDelegate({ description: "first loaded" })],
        page: { complete: false, nextCursor: "next", issues: [] },
      };
    continuationReads += 1;
    if (continuationReads === 1)
      throw new WireError("source access unavailable", -32014, {
        evenerErrorInfo: "actionUnavailable",
        retryDisposition: "automatic",
      });
    return {
      context: activityContext(ref),
      scope: scope ?? "session",
      delegates: [activityDelegate({ delegateId: "raw-second", childRef: "other:second", description: "healed page" })],
      page: { complete: true, issues: [] },
    };
  });
  try {
    connectionStore.getState().connect(client);
    let view: ReturnType<typeof render> | undefined;
    await act(async () => {
      view = render(<AgentsTab now={NOW} scope={scope()} />);
    });
    expect(screen.getByRole("button", { name: /first loaded/ })).toBeTruthy();
    expect(observerCount).toBe(1);
    expect(continuationReads).toBe(0);
    await act(async () => intersect?.());
    expect(continuationReads).toBe(1);
    expect(screen.getByRole("button", { name: /first loaded/ })).toBeTruthy();
    await act(async () => vi.advanceTimersByTimeAsync(999));
    expect(continuationReads).toBe(1);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(screen.getByRole("button", { name: /healed page/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /first loaded/ })).toBeTruthy();
    view?.unmount();
    act(() =>
      client.emitNotification(activityChangedNotification({ ref: "remote:owner", threadId: "owner" }, ["delegates"])),
    );
    await act(async () => vi.advanceTimersByTimeAsync(120000));
    expect(continuationReads).toBe(2);
  } finally {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});

it("definitive unavailable jobs never claim empty or remain a loading promise", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", () => {
    throw new WireError("deleted", -32012, { evenerErrorInfo: "resourceNotFound" });
  });
  connectionStore.getState().connect(client);
  render(<JobsTab now={NOW} scope={scope()} />);
  expect(await screen.findByText("Jobs unavailable for this session.")).toBeTruthy();
  expect(screen.queryByText("No jobs at this level.")).toBeNull();
  expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
  expect(screen.queryByRole("button", { name: /retry|repair/i })).toBeNull();
});

it("inactive subagents disclosure publishes its expansion state and controlled list", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [
      activityDelegate({
        delegateId: "first",
        childRef: "remote:first",
        description: "first inactive",
        terminal: true,
        status: "completed",
        lifecycle: "idle",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: true, active: 0, total: 1, completed: 1, failed: 0 },
  }));
  connectionStore.getState().connect(client);
  render(<AgentsTab now={NOW} scope={scope()} />);
  const fold = await screen.findByText("1 inactive subagent");
  const disclosure = fold.closest("details");
  expect(disclosure?.open).toBe(false);
  fireEvent.click(fold);
  expect(disclosure?.open).toBe(true);
});

it("closed inactive history still observes the page boundary without admitting offscreen pages", async () => {
  const client = activityClient();
  let observations = 0,
    disconnections = 0,
    intersect: ((visible: boolean) => void) | undefined;
  class Observer {
    constructor(callback: IntersectionObserverCallback) {
      intersect = (visible) =>
        callback([{ isIntersecting: visible } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
    }
    observe() {
      observations++;
    }
    disconnect() {
      disconnections++;
    }
  }
  vi.stubGlobal("IntersectionObserver", Observer);
  client.on("evener/thread/delegates/list", ({ ref, scope, cursor }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [
      activityDelegate({
        delegateId: cursor ? "second" : "first",
        childRef: cursor ? "remote:second" : "remote:first",
        description: cursor ? "second inactive" : "first inactive",
        terminal: true,
        status: "completed",
        lifecycle: "idle",
      }),
    ],
    page: cursor ? { complete: true, issues: [] } : { complete: false, nextCursor: "next", issues: [] },
  }));
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: true, active: 0, total: 2, completed: 2, failed: 0 },
  }));
  try {
    connectionStore.getState().connect(client);
    await act(async () => {
      render(<AgentsTab now={NOW} scope={scope()} />);
    });
    expect(observations).toBe(1);
    expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
    fireEvent.click(screen.getByText("2 inactive subagents"));
    await act(async () => intersect?.(false));
    expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
    await act(async () => intersect?.(true));
    expect(screen.getByRole("button", { name: /second inactive/ })).toBeTruthy();
    expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(2);
    expect(disconnections).toBe(1);
  } finally {
    cleanup();
    vi.unstubAllGlobals();
  }
});

it.each([
  { resource: "delegates" as const, Body: AgentsTab, text: "inspect" },
  { resource: "jobs" as const, Body: JobsTab, text: "run checks" },
  { resource: "watches" as const, Body: WatchesTab, text: "permanent watch" },
])(
  "$resource keeps useful rows after permanent refusal without claiming an active retry",
  async ({ resource, Body, text }) => {
    const client = activityClient();
    client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
      context: activityContext(ref),
      scope: scope ?? "session",
      jobs: [activityJob()],
      page: { complete: true, issues: [] },
    }));
    client.on("evener/thread/watches/list", ({ ref, scope }) => ({
      context: activityContext(ref),
      scope: scope ?? "session",
      watches: [activityWatch({ note: "permanent watch" }, ref)],
      page: { complete: true, issues: [] },
    }));
    connectionStore.getState().connect(client);
    render(<Body scope={scope()} now={NOW} />);
    await screen.findByText(text);
    const method =
      resource === "delegates"
        ? "evener/thread/delegates/list"
        : resource === "jobs"
          ? "evener/thread/jobs/list"
          : "evener/thread/watches/list";
    client.on(method, () => {
      throw new WireError("unsupported source", -32014, { evenerErrorInfo: "actionUnavailable" });
    });
    act(() =>
      client.emitNotification(activityChangedNotification({ ref: "remote:owner", threadId: "owner" }, [resource])),
    );
    const expectedScope = resource === "delegates" ? "subtree" : "session";
    await waitFor(() =>
      expect(sessionActivitySnapshot(client, "remote:owner", expectedScope)?.[resource].permanent).toBe(true),
    );
    expect(screen.getByText(text)).toBeTruthy();
    expect(screen.queryByText(/updating/)).toBeNull();
  },
);

it("each positive page needs fresh visible boundary evidence instead of draining unseen history", async () => {
  const client = activityClient();
  const intersections: ((visible: boolean) => void)[] = [];
  class Observer {
    constructor(callback: IntersectionObserverCallback) {
      intersections.push((visible) =>
        callback([{ isIntersecting: visible } as IntersectionObserverEntry], this as unknown as IntersectionObserver),
      );
    }
    observe() {}
    disconnect() {}
  }
  vi.stubGlobal("IntersectionObserver", Observer);
  client.on("evener/thread/jobs/list", ({ ref, scope, cursor }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ jobId: cursor ?? "first", description: cursor ?? "first", command: cursor ?? "first" })],
    page:
      cursor === "third"
        ? { complete: true, issues: [] }
        : { complete: false, nextCursor: cursor ? "third" : "second", issues: [] },
  }));
  try {
    connectionStore.getState().connect(client);
    await act(async () => {
      render(<JobsTab now={NOW} scope={scope()} />);
    });
    expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(1);
    await act(async () => intersections[0]?.(true));
    expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(2);
    expect(screen.getByText("second")).toBeTruthy();
    await act(async () => intersections.at(-1)?.(false));
    expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(2);
    await act(async () => intersections.at(-1)?.(true));
    expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(3);
    expect(screen.getByText("third")).toBeTruthy();
  } finally {
    cleanup();
    vi.unstubAllGlobals();
  }
});

// The no-read pushed merge is gated on a live context that advertises the
// bounded preview. The shared activityContext() omits reportPreview on purpose,
// so these two tests opt in per test rather than flipping every other activity
// test onto the new path.
const pushContext = () => ({ ...activityContext(), reportPreview: true });
const delegateReads = (client: ReturnType<typeof activityClient>) =>
  client.calls.filter((call) => call.method === "evener/thread/delegates/list").length;

it("a pushed delegate frame patches a loaded row with zero collection reads when the source advertises the preview", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    context: pushContext(),
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    delegates: [activityDelegate({ ownerRef: ref, rootRef: ref })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<AgentsTab now={NOW} scope={scope()} />);
  await screen.findByRole("button", { name: /inspect/ });
  const before = delegateReads(client);
  await act(async () => {
    client.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId: "owner",
        ref: activityRef,
        delegate: {
          runGeneration: 1,
          delegateId: "delegate-1",
          ownerSessionId: "owner",
          logicalOwnerSessionId: "owner",
          rootSessionId: "owner",
          childSessionId: "child",
          childRef: "remote:child",
          transcriptRef: "remote:child",
          type: "delegate",
          lifecycle: "running",
          phase: "running",
          status: "failed",
          terminal: false,
          resumable: true,
          needsAttention: false,
          projectionRevision: 2,
          reportPreview: "pushed report",
          description: "pushed row",
        },
      },
    });
  });
  expect(delegateReads(client)).toBe(before);
  expect(sessionActivitySnapshot(client, activityRef, "subtree")?.delegates.rows[0]).toMatchObject({
    status: "failed",
    reportPreview: "pushed report",
    description: "pushed row",
  });
  expect(screen.getByRole("button", { name: /pushed row/ })).toBeTruthy();
});

it("a source without the preview capability keeps reading delegates on an invalidation", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  render(<AgentsTab now={NOW} scope={scope()} />);
  await screen.findByRole("button", { name: /inspect/ });
  const before = delegateReads(client);
  await act(async () => {
    client.emitNotification(activityChangedNotification({ ref: activityRef, threadId: "owner" }, ["delegates"]));
  });
  await waitFor(() => expect(delegateReads(client)).toBe(before + 1));
});

it("reconciles a two-level subtree with collapsible branches, drain-hold suppression, cascade edges, and grouped inactive history", async () => {
  const client = activityClient();
  const coord = activityDelegate({
    delegateId: "coord-1",
    name: "coordinator",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:coord",
    terminal: false,
    drainHeldBySubagents: true,
    runningSubagentCount: 1,
    latestIntent: "STALE_COORD_INTENT",
  });
  const activeChild = activityDelegate({
    delegateId: "worker-live",
    name: "worker-live",
    ownerRef: "remote:coord",
    rootRef: activityRef,
    childRef: "remote:worker-live",
    parentDelegateId: "coord-1",
    terminal: false,
    latestIntent: "LIVE_CHILD_INTENT",
  });
  const settledChild = activityDelegate({
    delegateId: "worker-done",
    name: "worker-done",
    ownerRef: "remote:coord",
    rootRef: activityRef,
    childRef: "remote:worker-done",
    parentDelegateId: "coord-1",
    terminal: true,
    status: "completed",
    outcome: "completed",
    lifecycle: "idle",
  });
  const settledDirect = activityDelegate({
    delegateId: "direct-done",
    name: "direct-done",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:direct-done",
    terminal: true,
    status: "completed",
    outcome: "completed",
    lifecycle: "idle",
  });
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: true, total: 4, active: 2, completed: 2, failed: 0 },
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [coord, activeChild, settledChild, settledDirect],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  const rootPane = workspaceStore.getState().openPane("session", { ref: activityRef });
  render(<AgentsTab now={NOW} scope={scope()} />);

  const coordAnchorSelector = `[data-activity-anchor=${JSON.stringify('delegate:["remote:coord","coord-1"]')}]`;
  const childAnchorSelector = `[data-activity-anchor=${JSON.stringify('delegate:["remote:worker-live","worker-live"]')}]`;
  await waitFor(() => expect(document.querySelector(coordAnchorSelector)).not.toBeNull());
  expect(document.querySelector(childAnchorSelector)).not.toBeNull();

  // Expanded drain-held coordinator omits [data-activity-meta] and never leaks STALE_COORD_INTENT;
  // active child shows LIVE_CHILD_INTENT.
  const coordRow = document.querySelector<HTMLElement>(coordAnchorSelector)!;
  const childRow = document.querySelector<HTMLElement>(childAnchorSelector)!;
  expect(coordRow.querySelector("[data-activity-meta]")).toBeNull();
  expect(coordRow.textContent).not.toContain("STALE_COORD_INTENT");
  expect(childRow.querySelector("[data-activity-meta]")?.textContent).toBe("LIVE_CHILD_INTENT");

  // Collapsing the coordinator hides the active child and reveals the wait meta (still excluding STALE_COORD_INTENT).
  const toggle = screen.getByRole("button", { name: "Collapse coordinator" });
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  fireEvent.click(toggle);
  expect(screen.getByRole("button", { name: "Expand coordinator" }).getAttribute("aria-expanded")).toBe("false");
  expect(document.querySelector(childAnchorSelector)).toBeNull();
  const collapsedCoordRow = document.querySelector<HTMLElement>(coordAnchorSelector)!;
  const collapsedMeta = collapsedCoordRow.querySelector("[data-activity-meta]")?.textContent;
  expect(collapsedMeta).toBeTruthy();
  expect(collapsedMeta).toContain("1");
  expect(collapsedCoordRow.textContent).not.toContain("STALE_COORD_INTENT");

  // Re-expanding restores the nested active child; clicking it drills the full two-hop cascade.
  fireEvent.click(screen.getByRole("button", { name: "Expand coordinator" }));
  const nestedChildButton = document.querySelector<HTMLElement>(childAnchorSelector)!;
  await act(async () => fireEvent.click(nestedChildButton));
  const cascade = workspaceStore.getState().panes.find((pane) => pane.type === "sessionZoom");
  expect(cascade?.slot).toBe("secondary");
  expect(cascade?.params).toEqual({
    ref: "remote:worker-live",
    source: { type: "session", params: { ref: activityRef } },
    edges: [
      { ownerRef: activityRef, childRef: "remote:coord", delegateId: "coord-1" },
      { ownerRef: "remote:coord", childRef: "remote:worker-live", delegateId: "worker-live" },
    ],
    inspection: { origin: { paneId: rootPane, type: "session", ref: activityRef } },
  });

  // Bottom inactive fold reflects total - active (2) and groups settled direct + settled-under-coordinator runs.
  const foldSummary = screen.getByText("2 inactive subagents");
  const foldDetails = foldSummary.closest("details")!;
  expect(foldDetails.open).toBe(false);
  expect(within(foldDetails).queryByRole("button", { name: /direct-done/ })).toBeNull();
  fireEvent.click(foldSummary);
  expect(foldDetails.open).toBe(true);
  expect(within(foldDetails).getByRole("button", { name: /direct-done/ })).toBeTruthy();
  expect(within(foldDetails).queryByRole("button", { name: /worker-done/ })).toBeNull();
  const groupSummary = within(foldDetails).getByText("coordinator (1)");
  fireEvent.click(groupSummary);
  expect(within(foldDetails).getByRole("button", { name: /worker-done/ })).toBeTruthy();
});

it("renders an active child whose parent is on a later page, then nests it once the parent arrives", async () => {
  const client = activityClient();
  let intersect: (() => void) | undefined;
  class Observer {
    constructor(callback: IntersectionObserverCallback) {
      intersect = () =>
        callback([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
    }
    observe() {}
    disconnect() {}
  }
  vi.stubGlobal("IntersectionObserver", Observer);
  const coord = activityDelegate({
    delegateId: "coord-p2",
    name: "parent-on-page-2",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:coord-p2",
    terminal: false,
  });
  const child = activityDelegate({
    delegateId: "child-p1",
    name: "child-on-page-1",
    ownerRef: "remote:coord-p2",
    rootRef: activityRef,
    childRef: "remote:child-p1",
    parentDelegateId: "coord-p2",
    terminal: false,
  });
  client.on("evener/thread/delegates/list", ({ ref, scope, cursor }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: cursor ? [coord] : [child],
    page: cursor ? { complete: true, issues: [] } : { complete: false, nextCursor: "page-2", issues: [] },
  }));
  try {
    connectionStore.getState().connect(client);
    const rootPane = workspaceStore.getState().openPane("session", { ref: activityRef });
    await act(async () => {
      render(<AgentsTab now={NOW} scope={scope()} />);
    });
    const childAnchorSelector = `[data-activity-anchor=${JSON.stringify('delegate:["remote:child-p1","child-p1"]')}]`;
    const coordAnchorSelector = `[data-activity-anchor=${JSON.stringify('delegate:["remote:coord-p2","coord-p2"]')}]`;
    expect(document.querySelector(childAnchorSelector)).not.toBeNull();
    expect(document.querySelector(coordAnchorSelector)).toBeNull();
    expect(screen.queryByRole("button", { name: "Collapse parent-on-page-2" })).toBeNull();

    // Clicking the orphaned page-1 child before page 2 loads binds inspection.origin to the root pane.
    await act(async () => fireEvent.click(document.querySelector<HTMLElement>(childAnchorSelector)!));
    const earlyCascade = workspaceStore.getState().panes.find((pane) => pane.type === "sessionZoom");
    expect(earlyCascade?.params).toMatchObject({
      ref: "remote:child-p1",
      source: { type: "session", params: { ref: activityRef } },
      edges: [{ ownerRef: "remote:coord-p2", childRef: "remote:child-p1", delegateId: "child-p1" }],
      inspection: { origin: { paneId: rootPane, type: "session", ref: activityRef } },
    });

    await act(async () => intersect?.());
    expect(document.querySelector(coordAnchorSelector)).not.toBeNull();
    expect(document.querySelector(childAnchorSelector)).not.toBeNull();
    const collapseParent = screen.getByRole("button", { name: "Collapse parent-on-page-2" });
    expect(collapseParent.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(collapseParent);
    expect(document.querySelector(childAnchorSelector)).toBeNull();
  } finally {
    cleanup();
    vi.unstubAllGlobals();
  }
});

it("collapses settled top-level nodes with settled descendants by default and caps indentation at depth 3", async () => {
  const client = activityClient();
  const settledParent = activityDelegate({
    delegateId: "settled-p",
    name: "settled-parent",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:settled-p",
    terminal: true,
    status: "completed",
    outcome: "completed",
    lifecycle: "idle",
  });
  const settledDescendant = activityDelegate({
    delegateId: "settled-c",
    name: "settled-descendant",
    ownerRef: "remote:settled-p",
    rootRef: activityRef,
    childRef: "remote:settled-c",
    parentDelegateId: "settled-p",
    terminal: true,
    status: "completed",
    outcome: "completed",
    lifecycle: "idle",
  });
  const deepChain = ["d0", "d1", "d2", "d3", "d4"].map((id, idx, arr) =>
    activityDelegate({
      delegateId: id,
      name: id,
      ownerRef: idx === 0 ? activityRef : `remote:${arr[idx - 1]}`,
      rootRef: activityRef,
      childRef: `remote:${id}`,
      parentDelegateId: idx === 0 ? undefined : arr[idx - 1],
      terminal: false,
    }),
  );
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: true, total: 7, active: 5, completed: 2, failed: 0 },
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [...deepChain, settledParent, settledDescendant],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<AgentsTab now={NOW} scope={scope()} />);

  const deepestAnchor = `[data-activity-anchor=${JSON.stringify('delegate:["remote:d4","d4"]')}]`;
  await waitFor(() => expect(document.querySelector(deepestAnchor)).not.toBeNull());
  let branchDepth = 0;
  let cursor: HTMLElement | null = document.querySelector<HTMLElement>(deepestAnchor);
  while (cursor) {
    if (cursor.className.includes("childBranch")) branchDepth += 1;
    cursor = cursor.parentElement;
  }
  expect(branchDepth).toBe(3);

  const foldSummary = screen.getByText("2 inactive subagents");
  const foldDetails = foldSummary.closest("details")!;
  fireEvent.click(foldSummary);
  expect(within(foldDetails).getByRole("button", { name: /^settled-parent/ })).toBeTruthy();
  const expandSettled = within(foldDetails).getByRole("button", { name: "Expand settled-parent" });
  expect(expandSettled.getAttribute("aria-expanded")).toBe("false");
  expect(within(foldDetails).queryByRole("button", { name: /settled-descendant/ })).toBeNull();
  fireEvent.click(expandSettled);
  expect(within(foldDetails).getByRole("button", { name: /settled-descendant/ })).toBeTruthy();
});

it("keeps coordinator wait visible on partial pages until every held descendant is visible", async () => {
  const client = activityClient();
  ActivityBoundaryObserver.reset();
  vi.stubGlobal("IntersectionObserver", ActivityBoundaryObserver);
  const coord = activityDelegate({
    delegateId: "coord-partial",
    name: "coord-partial",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:coord-partial",
    terminal: false,
    drainHeldBySubagents: true,
    runningSubagentCount: 2,
    latestIntent: "STALE_PARTIAL_INTENT",
  });
  const worker1 = activityDelegate({
    delegateId: "worker-1",
    name: "worker-1",
    ownerRef: "remote:coord-partial",
    rootRef: activityRef,
    childRef: "remote:worker-1",
    parentDelegateId: "coord-partial",
    terminal: false,
  });
  const worker2 = activityDelegate({
    delegateId: "worker-2",
    name: "worker-2",
    ownerRef: "remote:coord-partial",
    rootRef: activityRef,
    childRef: "remote:worker-2",
    parentDelegateId: "coord-partial",
    terminal: false,
  });
  client.on("evener/thread/delegates/list", ({ ref, scope, cursor }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: cursor ? [worker2] : [coord, worker1],
    page: cursor ? { complete: true, issues: [] } : { complete: false, nextCursor: "page-2", issues: [] },
  }));
  try {
    connectionStore.getState().connect(client);
    await act(async () => {
      render(<AgentsTab now={NOW} scope={scope()} />);
    });
    const coordAnchorSelector = `[data-activity-anchor=${JSON.stringify('delegate:["remote:coord-partial","coord-partial"]')}]`;
    const coordBefore = document.querySelector<HTMLElement>(coordAnchorSelector)!;
    const metaBefore = coordBefore.querySelector("[data-activity-meta]")?.textContent;
    expect(metaBefore).toBeTruthy();
    expect(metaBefore).toContain("2");
    expect(coordBefore.textContent).not.toContain("STALE_PARTIAL_INTENT");

    await act(async () => ActivityBoundaryObserver.intersect());
    const coordAfter = document.querySelector<HTMLElement>(coordAnchorSelector)!;
    expect(coordAfter.querySelector("[data-activity-meta]")).toBeNull();
    expect(coordAfter.textContent).not.toContain("STALE_PARTIAL_INTENT");
  } finally {
    cleanup();
    vi.unstubAllGlobals();
    ActivityBoundaryObserver.reset();
  }
});

it("reveals ancestor wait when a nested child branch collapses and hides counted descendants", async () => {
  const client = activityClient();
  const nodeA = activityDelegate({
    delegateId: "node-a",
    name: "branch-a",
    ownerRef: activityRef,
    rootRef: activityRef,
    childRef: "remote:node-a",
    terminal: false,
    drainHeldBySubagents: true,
    runningSubagentCount: 2,
    latestIntent: "STALE_A_INTENT",
  });
  const nodeB = activityDelegate({
    delegateId: "node-b",
    name: "branch-b",
    ownerRef: "remote:node-a",
    rootRef: activityRef,
    childRef: "remote:node-b",
    parentDelegateId: "node-a",
    terminal: false,
    drainHeldBySubagents: true,
    runningSubagentCount: 1,
    latestIntent: "STALE_B_INTENT",
  });
  const nodeC = activityDelegate({
    delegateId: "node-c",
    name: "leaf-c",
    ownerRef: "remote:node-b",
    rootRef: activityRef,
    childRef: "remote:node-c",
    parentDelegateId: "node-b",
    terminal: false,
  });
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [nodeA, nodeB, nodeC],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<AgentsTab now={NOW} scope={scope()} />);

  const anchorA = `[data-activity-anchor=${JSON.stringify('delegate:["remote:node-a","node-a"]')}]`;
  const anchorB = `[data-activity-anchor=${JSON.stringify('delegate:["remote:node-b","node-b"]')}]`;
  await waitFor(() => expect(document.querySelector(anchorA)).not.toBeNull());

  // Both A and B expanded: all 2 descendants are visibly rendered, so A omits wait meta.
  expect(document.querySelector<HTMLElement>(anchorA)?.querySelector("[data-activity-meta]")).toBeNull();
  expect(document.querySelector<HTMLElement>(anchorB)?.querySelector("[data-activity-meta]")).toBeNull();

  // Collapsing B leaves only 1 visible descendant row under A (< 2), so A reveals its wait meta.
  fireEvent.click(screen.getByRole("button", { name: "Collapse branch-b" }));
  const rowAAfterCollapse = document.querySelector<HTMLElement>(anchorA)!;
  const metaAAfterCollapse = rowAAfterCollapse.querySelector("[data-activity-meta]")?.textContent;
  expect(metaAAfterCollapse).toBeTruthy();
  expect(metaAAfterCollapse).toContain("2");
  expect(rowAAfterCollapse.textContent).not.toContain("STALE_A_INTENT");

  const rowBAfterCollapse = document.querySelector<HTMLElement>(anchorB)!;
  const metaBAfterCollapse = rowBAfterCollapse.querySelector("[data-activity-meta]")?.textContent;
  expect(metaBAfterCollapse).toBeTruthy();
  expect(metaBAfterCollapse).toContain("1");
});
