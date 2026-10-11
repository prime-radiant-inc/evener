import type { ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { NOW_TICK_MS } from "../../panes/session/liveness";
import Transcript from "../../panes/transcript/testing/CommittedTranscript";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityClient,
  activityDetailsThread,
  activityThread,
  answerActivityRead,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

const ref = "remote:about-owner";
function mountSidebar() {
  return render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
}
beforeEach(() => installLocalStorage(new MemoryStorage()));
afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("About renders the explicit remote session's hydrated accounting", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(screen.getByText("anthropic/claude-sonnet")).toBeTruthy();
  expect(screen.getByText("idle")).toBeTruthy();
  expect(screen.getByTestId("session-details-context").textContent).toContain("42% used");
  expect(screen.getByTestId("session-details-context").textContent).toContain("58K left");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↑100K");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↓20K");
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("4s");
  expect(screen.getByTestId("session-details-cwd").textContent).toContain("/work/session");
  expect(screen.getByTestId("session-details-project").textContent).toContain("/work");
  expect(screen.getByTestId("session-details-branch").textContent).toContain("feature/overview");
  expect(screen.getByTestId("session-details-created")).toBeTruthy();
  expect(screen.getByTestId("session-details-updated")).toBeTruthy();
  expect(screen.getAllByRole("radio").map((el) => el.textContent?.split("\n")[0])).toEqual([
    "Agents",
    "Jobs",
    "Watches",
    "Tasks",
    "About",
  ]);
  expect(screen.getByRole("radio", { name: "About" }).textContent).toBe("About");
  expect(client.calls.some((call) => call.method.endsWith("/list"))).toBe(false);
});

test.each(["ended", "closed"] as const)("About preserves %s accounting without live context", async (status) => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref, { status: { type: status } }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(screen.getByText(status)).toBeTruthy();
  expect(screen.queryByTestId("session-details-context")).toBeNull();
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↑100K");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↓20K");
});

test("About omits unavailable accounting and empty sections", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityThread(ref);
    response.thread.cwd = "";
    return response;
  });
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("owner");
  expect(screen.queryByRole("heading", { name: "Usage" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "Location" })).toBeNull();
  expect(screen.queryByTestId("session-details-context")).toBeNull();
  expect(screen.queryByTestId("session-details-cost")).toBeNull();
  expect(screen.queryByTestId("session-details-tokens")).toBeNull();
  expect(screen.queryByTestId("session-details-work-time")).toBeNull();
});

test("About clears A immediately while B's model read is pending", async () => {
  const pendingB = deferred<ThreadReadResponse>();
  const client = activityClient();
  client.on("thread/read", ({ ref }) =>
    ref === "remote:b" ? pendingB.promise : activityDetailsThread(ref, { id: "identity-A" }),
  );
  connectionStore.getState().connect(client);
  installFocusedScope("remote:a");
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("identity-A");
  act(() => installFocusedScope("remote:b"));
  expect(screen.queryByText("identity-A")).toBeNull();
  expect(screen.getByRole("status").textContent).toContain("Loading session details");
  await act(async () => pendingB.resolve(activityDetailsThread("remote:b", { id: "identity-B" })));
  await screen.findByText("identity-B");
  expect(screen.queryByText("identity-A")).toBeNull();
});

test.each(["close", "switch-tab"] as const)(
  "About fences an abandoned read after %s and reacquisition",
  async (leave) => {
    const oldRead = deferred<ThreadReadResponse>();
    const newRead = deferred<ThreadReadResponse>();
    let richReads = 0;
    const client = activityClient();
    client.on("thread/read", (params) => {
      if (!params.includeTurns) return activityThread(params.ref);
      richReads += 1;
      return richReads === 1 ? oldRead.promise : newRead.promise;
    });
    connectionStore.getState().connect(client);
    installFocusedScope(ref);
    activitySidebarStore.getState().openWith("about");
    mountSidebar();
    await waitFor(() => expect(richReads).toBe(1));
    const sidebar = screen.getByTestId("activity-sidebar");
    act(() => {
      if (leave === "close") activitySidebarStore.getState().close();
      else activitySidebarStore.getState().setTab("jobs");
    });
    if (leave === "close") await waitFor(() => expect(sidebar.isConnected).toBe(false));
    await waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
    if (leave === "switch-tab") await screen.findByText("No jobs at this level.");
    act(() => activitySidebarStore.getState().openWith("about"));
    await waitFor(() => expect(richReads).toBe(2));
    await act(async () => newRead.resolve(activityDetailsThread(ref, { id: "accepted-new" })));
    await screen.findByText("accepted-new");
    await act(async () => oldRead.resolve(activityDetailsThread(ref, { id: "obsolete-old" })));
    expect(screen.queryByText("obsolete-old")).toBeNull();
    expect(screen.getByText("accepted-new")).toBeTruthy();
  },
);

test("rapid reopen during About exit preserves its pending reader", async () => {
  const pending = deferred<ThreadReadResponse>();
  let richReads = 0;
  const client = activityClient();
  client.on("thread/read", (params) => {
    if (!params.includeTurns) return activityThread(params.ref);
    richReads += 1;
    return pending.promise;
  });
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await waitFor(() => expect(richReads).toBe(1));
  const sidebar = screen.getByTestId("activity-sidebar");
  act(() => activitySidebarStore.getState().close());
  expect(sidebar.isConnected).toBe(true);
  act(() => activitySidebarStore.getState().openWith("about"));
  expect(screen.getByTestId("activity-sidebar")).toBe(sidebar);
  await act(async () => pending.resolve(activityDetailsThread(ref, { id: "preserved-read" })));
  expect(screen.getByText("preserved-read")).toBeTruthy();
  expect(richReads).toBe(1);
});

test("About accepts a replacement instance at the same public ref", async () => {
  let instance = "instance-first";
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityDetailsThread(ref, { id: instance });
    if (response.thread.evener) response.thread.evener.instanceId = instance;
    return response;
  });
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("instance-first");
  instance = "instance-replacement";
  act(() => client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: "instance-first" } }));
  await screen.findByText("instance-replacement");
  expect(screen.queryByText("instance-first")).toBeNull();
  expect(threadsStore.getState().threads.get(ref)?.instanceId).toBe("instance-replacement");
});

test("About releases prior collection demand through invalidation and reconnect", async () => {
  const client = activityClient();
  let summaries = 0;
  client.on("evener/thread/activity/read", (params) => {
    summaries += 1;
    return answerActivityRead(params);
  });
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mountSidebar();
  await screen.findByRole("button", { name: /inspect/ });
  fireEvent.click(screen.getByRole("radio", { name: "About" }));
  await screen.findByText("about-owner");
  const delegateReads = client.calls.filter((call) => call.method === "evener/thread/delegates/list").length;
  const summaryReads = summaries;
  act(() => client.emitNotification(activityChangedNotification({ ref, threadId: "owner" }, ["summary", "delegates"])));
  await waitFor(() => {
    expect(summaries).toBeGreaterThan(summaryReads);
    expect(sessionActivitySnapshot(client, ref, "session")?.summaryState.loading).toBe(false);
  });
  expect(client.calls.filter((call) => call.method === "evener/thread/delegates/list")).toHaveLength(delegateReads);
  const beforeReconnect = summaries;
  const hydration = threadsStore.getState().hydrations.get(ref) ?? 0;
  act(() => client.emitStateChange("reconnecting"));
  act(() => client.emitReady());
  await waitFor(() => {
    expect(summaries).toBeGreaterThan(beforeReconnect);
    expect(sessionActivitySnapshot(client, ref, "session")?.summaryState.loading).toBe(false);
    expect(threadsStore.getState().hydrations.get(ref)).toBeGreaterThan(hydration);
  });
  expect(screen.getByText("about-owner")).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "evener/thread/delegates/list")).toHaveLength(delegateReads);
  expect(
    client.calls.filter(
      (call) =>
        call.method === "evener/thread/jobs/list" ||
        call.method === "evener/thread/watches/list" ||
        call.method === "evener/tasks/list",
    ),
  ).toHaveLength(0);
});

test("leaving About releases its rich model while the sidebar keeps its summary", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(
    client.calls.some(
      (call) => call.method === "thread/read" && (call.params as { includeTurns: boolean }).includeTurns,
    ),
  ).toBe(true);
  fireEvent.click(screen.getByRole("radio", { name: /Jobs/ }));
  await screen.findByText("No jobs at this level.");
  await waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
  expect(sessionActivitySnapshot(client, ref, "session")?.summary?.context.ref).toBe(ref);
});

test("closing About preserves the independent transcript's updating model", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  const transcript = render(
    <MotionProvider>
      <Transcript params={{ ref }} paneId="transcript-proof" focused={false} />
    </MotionProvider>,
  );
  await screen.findByText("No turns yet");
  const sidebar = screen.getByTestId("activity-sidebar");
  act(() => activitySidebarStore.getState().close());
  await waitFor(() => expect(sidebar.isConnected).toBe(false));
  expect(threadsStore.getState().threads.has(ref)).toBe(true);
  const unsubscribes = client.calls.filter((call) => call.method === "thread/unsubscribe").length;
  act(() =>
    client.emitNotification({
      method: "thread/status/changed",
      params: { ref, threadId: "about-owner", status: { type: "active" } },
    }),
  );
  await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("active"));
  expect(screen.getByText("No turns yet")).toBeTruthy();
  expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(unsubscribes);
  transcript.unmount();
  await waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
});

test("visible About work time advances during an active turn", async () => {
  const now = 1_780_000_000_000;
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityDetailsThread(ref, { status: { type: "active" } });
    if (response.thread.evener) response.thread.evener.activeTurnStartedAt = now - 10_000;
    return response;
  });
  connectionStore.getState().connect(client);
  await threadsStore.getState().ensureThread(ref);
  installFocusedScope(ref);
  vi.useFakeTimers({ now });
  activitySidebarStore.getState().openWith("about");
  await act(async () => {
    mountSidebar();
  });
  act(() => threadsStore.getState().releaseThread(ref));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("14s");
  act(() => vi.advanceTimersByTime(NOW_TICK_MS * 2));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("20s");
  expect(activitySidebarStore.getState().tab).toBe("about");
  act(() =>
    client.emitNotification({
      method: "thread/status/changed",
      params: { ref, threadId: "about-owner", status: { type: "ended" } },
    }),
  );
  expect(screen.getByText("ended")).toBeTruthy();
  expect(screen.queryByTestId("session-details-context")).toBeNull();
});

test("About keeps accepted accounting during disconnection and refreshes on ready", async () => {
  let cost = "~$1.00";
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityDetailsThread(ref);
    if (response.thread.evener) response.thread.evener.cost = cost;
    return response;
  });
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  act(() => client.emitStateChange("reconnecting"));
  expect(screen.getByText("about-owner")).toBeTruthy();
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  cost = "~$2.00";
  act(() => client.emitReady());
  await waitFor(() => expect(screen.getByTestId("session-details-cost").textContent).toContain("~$2.00"));
});
