import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityJob,
  activityThread,
  activityWatch,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

const ref = "remote:owner";
const otherRef = "remote:other";
const mount = () =>
  render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );

function clientWithHistory() {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [activityJob({ ownerRef: ref, description: `History for ${ref}`, terminal: true, outcome: "success" })],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [activityWatch({ note: "Release monitor", outputMatch: "READY_FOR_REVIEW" }, ref)],
    page: { complete: true, issues: [] },
  }));
  return client;
}

function clearLiveState() {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests({ preserveStorage: true });
  resetDisclosureStoreForTests();
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
}

beforeEach(() => installLocalStorage(new MemoryStorage()));
afterEach(clearLiveState);

test("reload restores the selected category and explicit job and watch disclosures from view intent alone", async () => {
  connectionStore.getState().connect(clientWithHistory());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  mount();
  fireEvent.click(await screen.findByText("1 completed job"));
  expect(screen.getByRole("button", { name: /History for remote:owner/ })).toBeTruthy();
  act(() => activitySidebarStore.getState().setTab("watches"));
  fireEvent.click(await screen.findByText("Release monitor"));
  expect(screen.getByTestId("watch-facts").textContent).toContain("READY_FOR_REVIEW");
  act(() => activitySidebarStore.getState().setTab("jobs"));

  clearLiveState();
  const reloaded = clientWithHistory();
  connectionStore.getState().connect(reloaded);
  installFocusedScope(ref);
  mount();
  await screen.findByRole("button", { name: /History for remote:owner/ });
  expect(activitySidebarStore.getState().tab).toBe("jobs");
  expect(reloaded.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(1);
  expect(reloaded.calls.filter((call) => call.method === "evener/thread/watches/list")).toHaveLength(0);
  act(() => activitySidebarStore.getState().setTab("watches"));
  expect((await screen.findByTestId("watch-facts")).textContent).toContain("READY_FOR_REVIEW");
  const stored = Array.from({ length: localStorage.length }, (_, index) => {
    const key = localStorage.key(index);
    return key === null ? "" : localStorage.getItem(key);
  }).join("\n");
  expect(stored).not.toContain("READY_FOR_REVIEW");
  expect(stored).not.toContain("History for remote:owner");
});

test("reload retains task disclosures while the existing task owner refetches completed, current and remaining work", async () => {
  const makeClient = () => {
    const client = activityClient();
    client.on("thread/read", ({ ref }) => {
      const response = activityThread(ref);
      if (response.thread.evener) response.thread.evener.tasks = { total: 6, done: 2, remaining: 4 };
      return response;
    });
    client.on("evener/tasks/list", () => ({
      data: [
        { id: 1, type: "implement", description: "Finished one", prompt: "First finished brief", status: "done" },
        { id: 2, type: "implement", description: "Finished two", prompt: "", status: "done" },
        { id: 3, type: "implement", description: "Current work", prompt: "Current brief", status: "in_progress" },
        ...[4, 5, 6].map((id) => ({
          id,
          type: "implement",
          description: `Remaining ${id}`,
          prompt: "",
          status: "open",
        })),
      ],
    }));
    return client;
  };
  connectionStore.getState().connect(makeClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("tasks");
  mount();
  fireEvent.click(await screen.findByText("2 completed tasks"));
  fireEvent.click(screen.getByText("Finished one"));
  fireEvent.click(screen.getByTestId("task-prompt-summary"));
  expect(screen.getByTestId("task-prompt").getAttribute("open")).not.toBeNull();
  clearLiveState();

  const reloaded = makeClient();
  connectionStore.getState().connect(reloaded);
  installFocusedScope(ref);
  mount();
  await screen.findByText("Finished one");
  expect(screen.getByTestId("task-prompt").getAttribute("open")).not.toBeNull();
  expect(screen.getByText("Current work")).toBeTruthy();
  expect(
    screen.getAllByTestId("task-group-live").map((group) => group.querySelectorAll('[data-testid="task-row"]').length),
  ).toEqual([1, 3]);
  expect(reloaded.calls.filter((call) => call.method === "evener/tasks/list")).toHaveLength(1);
  expect(reloaded.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(0);
});

test("retargeting restores each public session's own category and fold while an unseen scope starts fresh", async () => {
  connectionStore.getState().connect(clientWithHistory());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  mount();
  fireEvent.click(await screen.findByText("1 completed job"));
  act(() => installFocusedScope(otherRef));
  await screen.findByText("1 completed job");
  expect(screen.queryByRole("button", { name: /History for/ })).toBeNull();
  act(() => activitySidebarStore.getState().setTab("watches"));
  fireEvent.click(await screen.findByText("Release monitor"));
  act(() => activitySidebarStore.getState().setTab("about"));
  await screen.findByText("owner");
  act(() => installFocusedScope(ref));
  await screen.findByRole("button", { name: /History for remote:owner/ });
  expect(activitySidebarStore.getState().tab).toBe("jobs");
  act(() => activitySidebarStore.getState().setTab("watches"));
  await screen.findByText("Release monitor");
  expect(screen.queryByTestId("watch-facts")).toBeNull();
  act(() => installFocusedScope(otherRef));
  await screen.findByText("owner");
  expect(activitySidebarStore.getState().tab).toBe("about");
  act(() => installFocusedScope(ref));
  await screen.findByText("Release monitor");
  expect(activitySidebarStore.getState().tab).toBe("watches");
});

test("an explicitly closed saved sidebar starts no activity read on reload and keeps its chosen tab", async () => {
  connectionStore.getState().connect(clientWithHistory());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  mount();
  await screen.findByText("1 completed job");
  fireEvent.click(screen.getByRole("button", { name: "Close the activity sidebar" }));
  clearLiveState();

  const reloaded = clientWithHistory();
  connectionStore.getState().connect(reloaded);
  installFocusedScope(ref);
  mount();
  expect(screen.queryByTestId("activity-sidebar")).toBeNull();
  expect(reloaded.calls).toHaveLength(0);
  expect(activitySidebarStore.getState().tab).toBe("jobs");
});

test("an unseen child keeps the ongoing inspection open after an immediate reload", async () => {
  connectionStore.getState().connect(clientWithHistory());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("jobs");
  mount();
  await screen.findByText("1 completed job");
  act(() => installFocusedScope(otherRef));
  await screen.findByText("1 completed job");
  clearLiveState();
  connectionStore.getState().connect(clientWithHistory());
  installFocusedScope(otherRef);
  mount();
  await screen.findByText("1 completed job");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs", ref: otherRef });
});

test("inactive agent disclosure and locally revealed rows survive reload without copying another session", async () => {
  const makeClient = () => {
    const client = activityClient();
    client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
      context: activityContext(ref),
      scope: scope ?? "session",
      delegates: Array.from({ length: 21 }, (_, index) =>
        activityDelegate({
          ownerRef: ref,
          childRef: `${ref}:child-${index}`,
          delegateId: `done-${index}`,
          description: `Finished check ${index}`,
          terminal: true,
          status: "completed",
          outcome: "success",
        }),
      ),
      page: { complete: true, issues: [] },
    }));
    return client;
  };
  connectionStore.getState().connect(makeClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  fireEvent.click(await screen.findByRole("button", { name: /Inactive subagents/ }));
  fireEvent.click(screen.getByRole("button", { name: /Show 1 more/ }));
  expect(screen.getByRole("button", { name: /Finished check 20/ })).toBeTruthy();
  fireEvent.click(screen.getByRole("radio", { name: "About" }));
  await screen.findByText("owner");
  fireEvent.click(screen.getByRole("radio", { name: /Agents/ }));
  await screen.findByRole("button", { name: /Finished check 20/ });
  clearLiveState();

  connectionStore.getState().connect(makeClient());
  installFocusedScope(ref);
  mount();
  await screen.findByRole("button", { name: /Finished check 20/ });
  act(() => installFocusedScope(otherRef));
  await waitFor(() => expect(screen.queryByRole("button", { name: /Finished check 0/ })).toBeNull());
});
