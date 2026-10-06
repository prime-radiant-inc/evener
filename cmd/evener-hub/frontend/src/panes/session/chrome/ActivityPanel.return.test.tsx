import { hydrateThread, type SessionJobsResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { registerPaneForTests } from "../../../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { activityPanelStore, resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { connectionStore } from "../../../stores/connection";
import { sessionActivitySnapshot } from "../../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityJob,
  activitySummary,
  activityThread,
  activityWatch,
} from "../../../stores/sessionActivityTestUtils";
import { installMobileViewport } from "../testing/mobileViewport";
import { ActivityPanel } from "./ActivityPanel";

const ref = "remote:owner";
const model = hydrateThread(activityThread(ref), ref, 0);
let restoreViewport: () => void;
let restorePane: () => void;
beforeEach(() => {
  restoreViewport = installMobileViewport();
  resetWorkspaceStoreForTests();
  resetActivityPanelStoreForTests();
  restorePane = registerPaneForTests({
    id: "session",
    title: () => "Owner",
    component: lazy(() => Promise.resolve({ default: () => null })),
  });
  workspaceStore.getState().openPane("session", { ref });
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetActivityPanelStoreForTests();
  resetWorkspaceStoreForTests();
  restorePane();
  restoreViewport();
  vi.useRealTimers();
});

const panel = () => <ActivityPanel sessionRef={ref} model={model} />;
const pageReads = (client: ReturnType<typeof activityClient>) =>
  client.calls.filter(({ params }) => typeof params === "object" && params !== null && "cursor" in params);

test("return restores each resource independently using qualified job and receiver-watch identities", async () => {
  const client = activityClient();
  let returning = false;
  const job = activityJob({ jobId: "same-id", description: "Saved job", hasOutput: false });
  const watch = activityWatch({ id: "same-id", note: "Saved watch" });
  client.on("evener/thread/delegates/list", () => ({
    context: activityContext(),
    scope: "subtree",
    delegates: [activityDelegate({ name: "Saved delegate" })],
    page: { complete: !returning, issues: [], ...(returning ? { nextCursor: "uninspected-delegates" } : {}) },
  }));
  client.on("evener/thread/jobs/list", ({ cursor }) => ({
    context: activityContext(),
    scope: "subtree",
    jobs:
      returning && !cursor
        ? [activityJob({ ...job, ownerRef: "remote:other", description: "Other owner's equal job ID" })]
        : [job],
    page: {
      complete: !returning || !!cursor,
      issues: [],
      ...(returning && !cursor ? { nextCursor: "saved-jobs" } : {}),
    },
  }));
  client.on("evener/thread/watches/list", ({ cursor }) => ({
    context: activityContext(),
    scope: "subtree",
    watches:
      returning && !cursor
        ? [activityWatch({ id: "same-id", note: "Other receiver's equal watch ID" }, "remote:other")]
        : [watch],
    page: {
      complete: !returning || !!cursor,
      issues: [],
      ...(returning && !cursor ? { nextCursor: "saved-watches" } : {}),
    },
  }));
  connectionStore.getState().connect(client);
  const first = render(panel());
  fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
  await screen.findByRole("treeitem", { name: "Saved job" });
  await screen.findByRole("treeitem", { name: "Watch: Saved watch" });
  await screen.findByRole("treeitem", { name: "Saved delegate" });
  first.unmount();
  expect(sessionActivitySnapshot(client, ref, "subtree")).toBeNull();
  returning = true;
  render(panel());
  await screen.findByRole("treeitem", { name: "Saved job" });
  await screen.findByRole("treeitem", { name: "Watch: Saved watch" });
  expect(screen.getByRole("treeitem", { name: "Other owner's equal job ID" })).toBeTruthy();
  expect(screen.getByRole("treeitem", { name: "Watch: Other receiver's equal watch ID" })).toBeTruthy();
  const pages = pageReads(client).map((call) => [call.method, (call.params as { cursor?: string }).cursor]);
  expect(pages).toHaveLength(2);
  expect(pages).toEqual(
    expect.arrayContaining([
      ["evener/thread/jobs/list", "saved-jobs"],
      ["evener/thread/watches/list", "saved-watches"],
    ]),
  );
});

test.each([false, true])(
  "a missing saved boundary is retired only at authoritative completion: %s",
  async (complete) => {
    const client = activityClient();
    let returning = false;
    client.on("evener/thread/jobs/list", () => ({
      context: activityContext(),
      scope: "subtree",
      jobs: [activityJob({ jobId: returning ? "new" : "saved", description: returning ? "New job" : "Saved job" })],
      page: { complete: !returning || complete, issues: returning && !complete ? [{ ref, code: "unavailable" }] : [] },
    }));
    connectionStore.getState().connect(client);
    const first = render(panel());
    fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
    await screen.findByRole("treeitem", { name: "Saved job" });
    const saved = activityPanelStore.getState().entries.get(ref)?.loadedExtent.jobs;
    first.unmount();
    returning = true;
    render(panel());
    await screen.findByRole("treeitem", { name: "New job" });
    const current = activityPanelStore.getState().entries.get(ref)?.loadedExtent.jobs;
    if (complete) expect(current).not.toBe(saved);
    else expect(current).toBe(saved);
  },
);

test("a replacement resolved session cannot inherit the old extent or disclosures", async () => {
  const client = activityClient();
  let sessionId = "owner";
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...activitySummary(undefined, scope),
    context: { ...activityContext(), sessionId },
  }));
  client.on("evener/thread/jobs/list", () => ({
    context: { ...activityContext(), sessionId },
    scope: "subtree",
    jobs: [
      activityJob({
        jobId: sessionId,
        description: sessionId === "owner" ? "Old session job" : "New session job",
        hasOutput: false,
      }),
    ],
    page: {
      complete: sessionId === "owner",
      issues: [],
      ...(sessionId !== "owner" ? { nextCursor: "not-demanded" } : {}),
    },
  }));
  for (const method of ["evener/thread/delegates/list", "evener/thread/watches/list"] as const) {
    client.on(method, () => ({
      context: { ...activityContext(), sessionId },
      scope: "subtree",
      delegates: [],
      watches: [],
      page: { complete: true, issues: [] },
    }));
  }
  connectionStore.getState().connect(client);
  const first = render(panel());
  fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
  fireEvent.click(await screen.findByRole("button", { name: "Show details for Old session job" }));
  first.unmount();
  sessionId = "replacement";
  vi.useFakeTimers();
  await act(async () => render(panel()));
  expect(screen.getByRole("treeitem", { name: "New session job" })).toBeTruthy();
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  expect(pageReads(client)).toHaveLength(0);
  expect(activityPanelStore.getState().entries.get(ref)?.resolvedSessionId).toBe("replacement");
  expect(activityPanelStore.getState().entries.get(ref)?.detailOverrides.size).toBe(0);
});

test.each([false, true])("closing cancels extent restoration and rejects late pages; admitted=%s", async (admitted) => {
  const client = activityClient();
  const late = deferred<SessionJobsResponse>();
  let returning = false;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (cursor) return late.promise;
    return {
      context: activityContext(),
      scope: "subtree",
      jobs: [
        activityJob({ jobId: returning ? "first" : "saved", description: returning ? "First page" : "Saved extent" }),
      ],
      page: { complete: !returning, issues: [], ...(returning ? { nextCursor: "later" } : {}) },
    };
  });
  connectionStore.getState().connect(client);
  const first = render(panel());
  fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
  await screen.findByRole("treeitem", { name: "Saved extent" });
  first.unmount();
  returning = true;
  vi.useFakeTimers();
  await act(async () => render(panel()));
  expect(screen.getByRole("treeitem", { name: "First page" })).toBeTruthy();
  if (admitted) await act(async () => vi.advanceTimersByTimeAsync(100));
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(sessionActivitySnapshot(client, ref, "subtree")).toBeNull();
  expect(activityPanelStore.getState().entries.get(ref)?.loadedExtent).toEqual({});
  await act(async () => {
    late.resolve({
      context: activityContext(),
      scope: "subtree",
      jobs: [activityJob({ jobId: "saved", description: "Late cancelled extent" })],
      page: { complete: true, issues: [] },
    });
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(pageReads(client)).toHaveLength(admitted ? 1 : 0);
  expect(activityPanelStore.getState().entries.get(ref)?.loadedExtent).toEqual({});
  await act(async () => fireEvent.click(screen.getByRole("button", { name: /^Activity/ })));
  expect(screen.getByRole("treeitem", { name: "First page" })).toBeTruthy();
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  expect(screen.queryByRole("treeitem", { name: "Late cancelled extent" })).toBeNull();
  expect(pageReads(client)).toHaveLength(admitted ? 1 : 0);
});

test.each([false, true])(
  "restoration stops at the saved identity or a clean end; identity remains=%s",
  async (found) => {
    const client = activityClient();
    let returning = false;
    client.on("evener/thread/jobs/list", ({ cursor }) => {
      const jobId = !returning ? "saved" : !cursor ? "first" : cursor === "second" && found ? "saved" : cursor;
      const complete = !returning || cursor === "last";
      return {
        context: activityContext(),
        scope: "subtree",
        jobs: [activityJob({ jobId, description: `Job ${jobId}` })],
        page: { complete, issues: [], ...(!complete ? { nextCursor: cursor ? "last" : "second" } : {}) },
      };
    });
    connectionStore.getState().connect(client);
    const first = render(panel());
    fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
    await screen.findByRole("treeitem", { name: "Job saved" });
    first.unmount();
    returning = true;
    vi.useFakeTimers();
    await act(async () => render(panel()));
    expect(screen.getByRole("treeitem", { name: "Job first" })).toBeTruthy();
    expect(pageReads(client)).toHaveLength(0);
    await act(async () => vi.advanceTimersByTimeAsync(100));
    expect(pageReads(client)).toHaveLength(1);
    await act(async () => vi.advanceTimersByTimeAsync(1000));
    expect(pageReads(client).map((call) => call.params)).toEqual(
      found
        ? [{ ref, scope: "subtree", cursor: "second" }]
        : [
            { ref, scope: "subtree", cursor: "second" },
            { ref, scope: "subtree", cursor: "last" },
          ],
    );
    expect(screen.getByRole("treeitem", { name: found ? "Job saved" : "Job last" })).toBeTruthy();
  },
);

test("a failed restoration page waits for the shared store retry and then recovers", async () => {
  const client = activityClient();
  let returning = false;
  let pageAttempts = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (cursor && ++pageAttempts === 1) throw new Error("temporary source interruption");
    const saved = !returning || !!cursor;
    return {
      context: activityContext(),
      scope: "subtree",
      jobs: [activityJob({ jobId: saved ? "saved" : "first", description: saved ? "Saved result" : "First page" })],
      page: { complete: saved, issues: [], ...(!saved ? { nextCursor: "later" } : {}) },
    };
  });
  connectionStore.getState().connect(client);
  const first = render(panel());
  fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
  await screen.findByRole("treeitem", { name: "Saved result" });
  first.unmount();
  returning = true;
  vi.useFakeTimers();
  await act(async () => render(panel()));
  await act(async () => vi.advanceTimersByTimeAsync(100));
  expect(pageAttempts).toBe(1);
  expect(screen.getByRole("treeitem", { name: "First page" })).toBeTruthy();
  expect(screen.queryByRole("treeitem", { name: "Saved result" })).toBeNull();
  await act(async () => vi.advanceTimersByTimeAsync(999));
  expect(pageAttempts).toBe(1);
  await act(async () => vi.advanceTimersByTimeAsync(1));
  expect(screen.getByRole("treeitem", { name: "Saved result" })).toBeTruthy();
  expect(pageAttempts).toBe(2);
});
