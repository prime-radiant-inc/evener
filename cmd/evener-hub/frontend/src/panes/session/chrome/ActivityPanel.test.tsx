import { hydrateThread, type SessionJobsResponse, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { JobsTab } from "../../../shell/activitybar/JobsTab";
import { deriveScope } from "../../../shell/statusbar/statusScope";
import { activityPanelStore } from "../../../stores/activityPanel";
import { connectionStore } from "../../../stores/connection";
import { navigationStore } from "../../../stores/navigation/store";
import { sessionActivitySnapshot } from "../../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityJob,
  activitySummary,
  activityThread,
  activityWatch,
} from "../../../stores/sessionActivityTestUtils";
import { NOW_TICK_MS } from "../liveness";
import { ActivityPanel, ActivityPanelBody } from "./ActivityPanel";

const ref = "remote:owner";
const model = (r = ref) => hydrateThread(activityThread(r), r, 0);
afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  activityPanelStore.getState().resetForTests();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

test("closed trigger shares session counts while an open recursive tree owns only visible subtree demand", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref),
    scope: scope ?? "session",
    delegates: {
      known: true,
      total: scope === "subtree" ? 7 : 1,
      active: scope === "subtree" ? 7 : 1,
      failed: 0,
      completed: 0,
    },
  }));
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ description: scope === "subtree" ? "subtree work" : "exact selected work" })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  const mounted = render(
    <>
      <ActivityPanel sessionRef={ref} model={model()} />
      <JobsTab scope={deriveScope(navigationStore.getState(), ref)} />
    </>,
  );
  await screen.findByRole("button", { name: "Activity · 3 active" });
  await screen.findByRole("button", { name: /exact selected work/ });
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(0);
  expect(client.calls.filter((c) => c.method === "evener/thread/watches/list")).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Activity · 3 active" }));
  await screen.findByText("subtree work");
  expect(sessionActivitySnapshot(client, ref, "subtree")?.summary?.delegates.active).toBe(7);
  expect(
    client.calls.filter((c) => c.method === "thread/read" && (c.params as { subscribe?: boolean }).subscribe),
  ).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  await waitFor(() => expect(sessionActivitySnapshot(client, ref, "subtree")).toBeNull());
  expect(sessionActivitySnapshot(client, ref, "session")?.jobs.complete).toBe(true);
  const before = client.calls.length;
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["jobs", "delegates", "watches"] },
    }),
  );
  await waitFor(() => expect(client.calls.length).toBeGreaterThan(before));
  expect(
    client.calls.filter(
      (c) => c.method === "evener/thread/jobs/list" && (c.params as { scope?: string }).scope === "subtree",
    ),
  ).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "thread/unsubscribe")).toHaveLength(0);
  mounted.unmount();
  await waitFor(() => expect(client.calls.filter((c) => c.method === "thread/unsubscribe")).toHaveLength(1));
});

test("empty bounded progress remains loading and advances to useful activity without a repair click", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  const entered = deferred<void>(),
    finish = deferred<SessionJobsResponse>();
  client.on("evener/thread/jobs/list", ({ cursor, ref, scope }) => {
    if (!cursor)
      return {
        context: activityContext(ref),
        scope: scope ?? "session",
        jobs: [],
        page: { complete: false, nextCursor: "scan", issues: [] },
      };
    entered.resolve();
    return finish.promise;
  });
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  await act(async () => await entered.promise);
  expect(screen.getByText("Loading activity…")).toBeTruthy();
  expect(screen.queryByText("No retained activity yet")).toBeNull();
  await act(async () =>
    finish.resolve({
      context: activityContext(),
      scope: "subtree",
      jobs: [activityJob({ description: "bounded recovered work" })],
      page: { complete: true, issues: [] },
    }),
  );
  expect(await screen.findByText("bounded recovered work")).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(2);
});

test("visible rows and disclosure survive same-session invalidation failures", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ description: "retained completed", terminal: true, status: "completed" })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  const fold = await screen.findByRole("treeitem", { name: "1 inactive" });
  fireEvent.click(fold.querySelector("button") as HTMLButtonElement);
  expect(screen.getByText("retained completed")).toBeTruthy();
  client.on("evener/thread/jobs/list", () => {
    throw new Error("temporary source");
  });
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["jobs"] },
    }),
  );
  expect(await screen.findByText("Activity is updating…")).toBeTruthy();
  expect(screen.getByText("retained completed")).toBeTruthy();
  expect(activityPanelStore.getState().entries.get(ref)?.expandedFoldIDs).toEqual([`session:${ref}:inactive-fold`]);
});

test("a switched routing ref retires old visible demand and fences delayed rows", async () => {
  const client = activityClient(),
    old = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => {
    if (ref === "remote:old") {
      entered.resolve();
      return old.promise;
    }
    return {
      context: activityContext(ref),
      scope: scope ?? "session",
      jobs: [activityJob({ ownerRef: ref, description: "current owner" })],
      page: { complete: true, issues: [] },
    };
  });
  connectionStore.getState().connect(client);
  const view = render(<ActivityPanelBody sessionRef="remote:old" model={model("remote:old")} />);
  await act(async () => await entered.promise);
  view.rerender(<ActivityPanelBody sessionRef="remote:new" model={model("remote:new")} />);
  await screen.findByText("current owner");
  await act(async () =>
    old.resolve({
      context: activityContext("remote:old"),
      scope: "subtree",
      jobs: [activityJob({ description: "stale owner" })],
      page: { complete: true, issues: [] },
    }),
  );
  expect(screen.queryByText("stale owner")).toBeNull();
  expect(sessionActivitySnapshot(client, "remote:old", "subtree")).toBeNull();
});

test("permanent refusal keeps useful rows without claiming ongoing recovery", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ description: "useful retained work" })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  await screen.findByText("useful retained work");
  client.on("evener/thread/jobs/list", () => {
    throw new WireError("unsupported source", -32014, { evenerErrorInfo: "actionUnavailable" });
  });
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["jobs"] },
    }),
  );
  await waitFor(() => expect(sessionActivitySnapshot(client, ref, "subtree")?.jobs.permanent).toBe(true));
  expect(screen.getByText("useful retained work")).toBeTruthy();
  expect(screen.queryByText("Activity is updating…")).toBeNull();
});

test("loaded subtree job output uses the supplied owner ref and raw logical job ID", async () => {
  const client = activityClient(),
    jobId = "job_02wMz5TxvEMoJEDTDGOTil_000000000123",
    ownerRef = "source:opaque-owner";
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ ownerRef, jobId, description: "output target" })],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/jobs/output", () => ({ data: { tail: "supplied output tail", totalBytes: 20, retainedStart: 0 } }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  fireEvent.click(await screen.findByRole("button", { name: "Hide details for output target" }));
  fireEvent.click(screen.getByRole("button", { name: "Show details for output target" }));
  expect(await screen.findByText("supplied output tail")).toBeTruthy();
  expect(client.calls.find((call) => call.method === "evener/jobs/output")?.params).toEqual({
    ref: ownerRef,
    jobId,
    maxBytes: 256,
  });
});

test("recursive activity shows the same proven parent hierarchy above its content", async () => {
  const client = activityClient();
  const context = {
    ...activityContext(ref),
    rootRef: "remote:root",
    ancestors: [{ ref: "remote:root", sessionId: "root", title: "Parent session" }],
  };
  client.on("evener/thread/activity/read", () => ({ ...activitySummary(ref), scope: "subtree", context }));
  client.on("evener/thread/delegates/list", () => ({
    context,
    scope: "subtree",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", () => ({
    context,
    scope: "subtree",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/watches/list", () => ({
    context,
    scope: "subtree",
    watches: [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  expect(await screen.findByRole("button", { name: "Parent session" })).toBeTruthy();
  expect(screen.getByRole("navigation", { name: "Scope" })).toBeTruthy();
});

test("recursive Activity body rebuilds typed rows on remount while retaining ref-qualified fold disclosure", async () => {
  const fake = activityClient();
  connectionStore.getState().connect(fake);
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
  const first = render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  const fold = await screen.findByRole("treeitem", { name: "1 inactive" });
  fireEvent.click(within(fold).getByRole("button"));
  expect(screen.getByText("completed retained row")).toBeTruthy();
  first.unmount();
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  await screen.findByText("completed retained row");
  expect(screen.getByRole("treeitem", { name: "1 inactive" }).getAttribute("aria-expanded")).toBe("true");
});

test("recursive Activity body preserves typed watch countdown through the tree clock", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(new Date("2026-08-05T15:00:12Z"));
  const fake = activityClient();
  connectionStore.getState().connect(fake);
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
  await act(async () => {
    render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  });
  const row = await screen.findByRole("treeitem", { name: "Watch: clock" });
  expect(row.textContent).toContain("next ~4m");
  await act(async () => vi.advanceTimersByTimeAsync(60000));
  expect(row.textContent).toContain("next ~3m");
});

test("recursive Activity body uses its row ticker without installing the Details clock", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  const interval = vi.spyOn(globalThis, "setInterval");
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  await screen.findByRole("tree");
  expect(interval).not.toHaveBeenCalledWith(expect.any(Function), NOW_TICK_MS);
});
