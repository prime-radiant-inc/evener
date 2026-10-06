import type { SessionActivitySummary } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { acquireSessionActivity, sessionActivitySnapshot, useSessionActivity } from "./sessionActivity";
import { activityClient, activityRef, activitySummary, activityThread } from "./sessionActivityTestUtils";

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null });
});

test("reading an absent binding owns no network work; committed summary consumers share one owner", async () => {
  const client = activityClient();
  expect(sessionActivitySnapshot(client, activityRef, "session")).toBeNull();
  expect(client.calls).toHaveLength(0);
  const first = acquireSessionActivity(client, activityRef),
    second = acquireSessionActivity(client, activityRef);
  expect(first.store).toBe(second.store);
  await waitFor(() => expect(first.store.getSnapshot().summary).not.toBeNull());
  expect(client.calls.map((call) => call.method)).toEqual(["thread/read", "evener/thread/activity/read"]);
  first.release();
  first.release();
  expect(sessionActivitySnapshot(client, activityRef, "session")).not.toBeNull();
  second.release();
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1));
  expect(sessionActivitySnapshot(client, activityRef, "session")).toBeNull();
});

test("summary-only hooks share qualified runtime and lose status trust on disconnect", async () => {
  const client = activityClient();
  client.on("thread/read", () => ({
    thread: { ...activityThread().thread, id: "wire-root", sessionId: "root-session" },
  }));
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...activitySummary(undefined, scope),
    context: { ...activitySummary().context, sessionId: "root-session" },
  }));
  connectionStore.getState().connect(client);
  const first = renderHook(() => useSessionActivity(activityRef)),
    second = renderHook(() => useSessionActivity(activityRef));
  await waitFor(() => expect(second.result.current.snapshot?.runtime?.sessionId).toBe("root-session"));
  expect(first.result.current.snapshot).toBe(second.result.current.snapshot);
  expect(client.calls.map(({ method }) => method)).toEqual(["thread/read", "evener/thread/activity/read"]);
  act(() =>
    client.emitNotification({
      method: "thread/status/changed",
      params: { ref: activityRef, threadId: "wire-root", status: { type: "active" } },
    }),
  );
  expect(first.result.current.snapshot?.runtime?.status.type).toBe("active");
  expect(second.result.current.snapshot?.runtime?.status.type).toBe("active");
  expect(client.calls).toHaveLength(2);
  first.unmount();
  expect(client.calls.filter(({ method }) => method === "thread/unsubscribe")).toHaveLength(0);
  act(() => client.emitStateChange("reconnecting"));
  expect(second.result.current.snapshot?.runtime).toBeNull();
  act(() =>
    client.emitNotification({
      method: "thread/status/changed",
      params: { ref: activityRef, threadId: "wire-root", status: { type: "active" } },
    }),
  );
  expect(second.result.current.snapshot?.runtime).toBeNull();
  act(() => client.emitReady());
  await waitFor(() => expect(second.result.current.snapshot?.runtime?.status.type).toBe("idle"));
  expect(client.calls.filter(({ method }) => method === "thread/read")).toHaveLength(2);
  expect(client.calls.filter(({ method }) => method === "evener/thread/jobs/list")).toHaveLength(0);
  second.unmount();
  await waitFor(() => expect(client.calls.filter(({ method }) => method === "thread/unsubscribe")).toHaveLength(1));
  expect(sessionActivitySnapshot(client, activityRef, "session")).toBeNull();
});

test("only the visible collection is observed, and tab changes keep the shared owner", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  const status = renderHook(() => useSessionActivity(activityRef));
  const tab = renderHook(
    ({ resource }: { resource: "jobs" | "watches" }) => useSessionActivity(activityRef, "session", resource),
    { initialProps: { resource: "jobs" } },
  );
  await waitFor(() => expect(tab.result.current.snapshot?.jobs.complete).toBe(true));
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "evener/thread/delegates/list")).toHaveLength(0);
  tab.rerender({ resource: "watches" });
  await waitFor(() => expect(tab.result.current.snapshot?.watches.complete).toBe(true));
  expect(client.calls.filter((call) => call.method === "evener/thread/activity/read")).toHaveLength(1);
  tab.unmount();
  expect(status.result.current.snapshot?.summary?.jobs.total).toBe(201);
  status.unmount();
});

test("client/ref/scope switches fence pending results and cleanup/remount keeps additive ownership", async () => {
  const oldClient = activityClient(),
    nextClient = activityClient(),
    pending = deferred<SessionActivitySummary>();
  oldClient.on("evener/thread/activity/read", ({ scope }) => pending.promise);
  connectionStore.getState().connect(oldClient);
  const view = renderHook(
    ({ ref, scope }: { ref: string; scope: "session" | "subtree" }) => useSessionActivity(ref, scope),
    { initialProps: { ref: activityRef, scope: "session" }, wrapper: StrictMode },
  );
  await waitFor(() => expect(oldClient.calls.some((call) => call.method === "evener/thread/activity/read")).toBe(true));
  act(() => connectionStore.getState().connect(nextClient));
  view.rerender({ ref: "remote:child", scope: "subtree" });
  await waitFor(() => expect(view.result.current.snapshot?.context?.ref).toBe("remote:child"));
  await act(async () => pending.resolve(activitySummary()));
  expect(view.result.current.snapshot?.ref).toBe("remote:child");
  expect(view.result.current.snapshot?.scope).toBe("subtree");
  view.unmount();
  await waitFor(() => expect(nextClient.calls.some((call) => call.method === "thread/unsubscribe")).toBe(true));
  const remount = renderHook(() => useSessionActivity("remote:child"));
  await waitFor(() => expect(remount.result.current.snapshot?.summary).not.toBeNull());
  expect(
    nextClient.calls
      .filter((call) => call.method === "thread/read")
      .every((call) => (call.params as { replaceSubscription: boolean }).replaceSubscription === false),
  ).toBe(true);
});
