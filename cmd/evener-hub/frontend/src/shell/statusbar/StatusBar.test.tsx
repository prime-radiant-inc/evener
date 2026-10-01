import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityContext, activitySummary } from "../../stores/sessionActivityTestUtils";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { resetWorkspaceStoreForTests } from "../workspace";
import { StatusBar } from "./StatusBar";
import { installFocusedScope, summaryOf } from "./scopeTestUtils";

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
});

test("selected child gets authoritative context/counts before its navigation location exists", async () => {
  const ref = "remote:deep-child",
    client = activityClient();
  client.on("evener/thread/activity/read", () => ({
    ...activitySummary(ref),
    context: {
      ...activityContext(ref),
      sessionId: "child",
      rootRef: "remote:root",
      parentRef: "remote:parent",
      ancestors: [
        { ref: "remote:root", sessionId: "root", title: "Root" },
        { ref: "remote:parent", sessionId: "parent", title: "Parent" },
      ],
    },
    delegates: { known: true, total: 20, active: 3, failed: 0, completed: 17 },
  }));
  installFocusedScope(ref);
  connectionStore.getState().connect(client);
  render(<StatusBar />);
  expect(await screen.findByRole("button", { name: /Agents, 3 active/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Root" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Parent" })).toBeTruthy();
  expect(screen.queryByText("Finding session context…")).toBeNull();
  expect(client.calls.map((c) => c.method)).toEqual(["thread/read", "evener/thread/activity/read"]);
  expect(client.calls.every((c) => (c.params as { ref?: string }).ref === ref)).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: /Jobs, 2 running/ }));
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
});

test("unknown counts remain unknown and pending ancestry is explicit", async () => {
  const ref = "remote:pending",
    client = activityClient();
  client.on("evener/thread/activity/read", () => ({
    ...activitySummary(ref),
    context: { ...activityContext(ref), ancestryKnown: false },
  }));
  installFocusedScope(ref);
  connectionStore.getState().connect(client);
  render(<StatusBar />);
  await waitFor(() => expect(screen.getByRole("button", { name: /Agents, … active/ })).toBeTruthy());
  expect(screen.getByText("Finding session context…")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /Agents, 0 active/ })).toBeNull();
  expect(client.calls.filter((c) => c.method.endsWith("/list"))).toHaveLength(0);
});

test("embedded Tasks counts still use the exact selected navigation row", async () => {
  const ref = "remote:tasks",
    client = activityClient();
  installFocusedScope(ref, summaryOf({ ref, title: "Tasks owner", tasks: { total: 5, done: 2 } }));
  connectionStore.getState().connect(client);
  render(<StatusBar />);
  expect(await screen.findByRole("button", { name: /Tasks, 2 of 5 done/ })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: /Tasks, 2 of 5 done/ }));
  expect(activitySidebarStore.getState().tab).toBe("tasks");
});
