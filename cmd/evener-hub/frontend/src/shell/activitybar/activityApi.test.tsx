import { WireError } from "@evener/appwire-client";
import { createNavigationStore } from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityJob,
  activityWatch,
} from "../../stores/sessionActivityTestUtils";
import { deriveScope } from "../statusbar/statusScope";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { AgentsTab } from "./AgentsTab";
import { JobsTab } from "./JobsTab";
import { WatchesTab } from "./WatchesTab";

beforeAll(async () => {
  await import("../../panes/session");
  await import("../../panes/transcript");
});
beforeEach(() => resetWorkspaceStoreForTests());

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
});
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
  render(<AgentsTab scope={scope()} />);
  expect(screen.getByText(/Loading subagents/)).toBeTruthy();
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
    source: { type: "transcript", params: { ref: "remote:owner" } },
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
  render(<JobsTab scope={scope()} />);
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
      view = render(<AgentsTab scope={scope()} />);
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
      client.emitNotification({
        method: "evener/thread/activity/changed",
        params: { ref: "remote:owner", threadId: "owner", sessionId: "owner", resources: ["delegates"] },
      }),
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
  render(<JobsTab scope={scope()} />);
  expect(await screen.findByText("Jobs unavailable for this session.")).toBeTruthy();
  expect(screen.queryByText("No jobs at this level.")).toBeNull();
  expect(screen.queryByText("Loading jobs…")).toBeNull();
  expect(screen.queryByRole("button", { name: /retry|repair/i })).toBeNull();
});

it("closed inactive history does not scan, and disclosed rows page only near the visible boundary", async () => {
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
  try {
    connectionStore.getState().connect(client);
    await act(async () => {
      render(<AgentsTab scope={scope()} />);
    });
    expect(observations).toBe(0);
    expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Inactive subagents (1)" }));
    expect(observations).toBe(1);
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
    render(<Body scope={scope()} />);
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
      client.emitNotification({
        method: "evener/thread/activity/changed",
        params: { ref: "remote:owner", threadId: "owner", sessionId: "owner", resources: [resource] },
      }),
    );
    await waitFor(() =>
      expect(sessionActivitySnapshot(client, "remote:owner", "session")?.[resource].permanent).toBe(true),
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
      render(<JobsTab scope={scope()} />);
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
