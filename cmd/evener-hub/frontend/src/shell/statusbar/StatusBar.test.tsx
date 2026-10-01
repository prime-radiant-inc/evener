import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityContext, activityJob, activitySummary } from "../../stores/sessionActivityTestUtils";
import { ActivitySidebar } from "../activitybar/ActivitySidebar";
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

test.each(["button", "Escape"])("closing activity with %s restores its keyboard opener", async (gesture) => {
  const user = userEvent.setup();
  installFocusedScope("remote:keyboard");
  connectionStore.getState().connect(activityClient());
  render(
    <MotionProvider>
      <StatusBar />
      <ActivitySidebar />
    </MotionProvider>,
  );
  const opener = await screen.findByRole("button", { name: /Jobs, 2 of 201 running/ });
  opener.focus();
  await user.keyboard("{Enter}");
  const close = await screen.findByRole("button", { name: "Close the activity sidebar" });
  close.focus();
  await user.keyboard(gesture === "button" ? "{Enter}" : "{Escape}");
  expect(activitySidebarStore.getState().open).toBe(false);
  expect(document.activeElement).toBe(opener);
});

test("dismissing activity while focus is outside keeps the user's current focus", async () => {
  const user = userEvent.setup();
  installFocusedScope("remote:keyboard");
  connectionStore.getState().connect(activityClient());
  render(
    <MotionProvider>
      <StatusBar />
      <ActivitySidebar />
      <input aria-label="Message recipient" />
    </MotionProvider>,
  );
  const opener = await screen.findByRole("button", { name: /Jobs, 2 of 201 running/ });
  await user.click(opener);
  const input = screen.getByRole("textbox", { name: "Message recipient" });
  input.focus();
  await user.keyboard("{Escape}");
  expect(activitySidebarStore.getState().open).toBe(false);
  expect(document.activeElement).toBe(input);
});

test("closing after a transient opener disappears returns to the selected activity chip", async () => {
  const user = userEvent.setup();
  installFocusedScope("remote:keyboard");
  connectionStore.getState().connect(activityClient());
  render(
    <MotionProvider>
      <StatusBar />
      <ActivitySidebar />
    </MotionProvider>,
  );
  const trigger = render(
    <button type="button" onClick={() => activitySidebarStore.getState().openWith("jobs")}>
      Inspect job activity
    </button>,
  );
  await user.click(screen.getByRole("button", { name: "Inspect job activity" }));
  trigger.unmount();
  await user.click(await screen.findByRole("button", { name: "Close the activity sidebar" }));
  expect(document.activeElement).toBe(screen.getByRole("button", { name: /Jobs, 2 of 201 running/ }));
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
  expect(await screen.findByRole("button", { name: /Agents, 3 of 20 active/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Root" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Parent" })).toBeTruthy();
  expect(screen.queryByText("Finding session context…")).toBeNull();
  expect(client.calls.map((c) => c.method)).toEqual(["thread/read", "evener/thread/activity/read"]);
  expect(client.calls.every((c) => (c.params as { ref?: string }).ref === ref)).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: /Jobs, 2 of 201 running/ }));
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
});

test("unknown counts remain unknown and pending ancestry is explicit", async () => {
  const ref = "remote:pending",
    client = activityClient();
  client.on("evener/thread/activity/read", () => ({
    ...activitySummary(ref),
    context: { ...activityContext(ref), ancestryKnown: false },
    delegates: { known: false, active: 17, total: 99, completed: 0, failed: 0 },
  }));
  client.on("evener/thread/delegates/list", () => ({
    context: { ...activityContext(ref), ancestryKnown: false },
    scope: "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  installFocusedScope(ref);
  connectionStore.getState().connect(client);
  activitySidebarStore.getState().openWith("agents");
  render(
    <MotionProvider>
      <StatusBar />
      <ActivitySidebar />
    </MotionProvider>,
  );
  await waitFor(() => expect(screen.getByRole("button", { name: /Agents, counts unknown/ })).toBeTruthy());
  expect(screen.getByRole("radio", { name: "Agents, counts unknown" }).textContent).toContain("—");
  expect(screen.getByRole("button", { name: /Agents, counts unknown/ }).textContent).toContain("—");
  expect(screen.getAllByText("Finding session context…")).toHaveLength(2);
  expect(screen.queryByRole("button", { name: /Agents, 0 of/ })).toBeNull();
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
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

test("footer and tabs explain summary counts through paging and activity changes", async () => {
  const ref = "remote:counts";
  const client = activityClient();
  let active = 2;
  let total = 6;
  const summary = () => ({
    ...activitySummary(ref),
    delegates: { known: true, active: 0, total: 2, completed: 2, failed: 0 },
    jobs: { known: true, active, total, completed: total - active, failed: 0 },
    watches: { known: true, active: 2, total: 3, completed: 1, failed: 0 },
  });
  const page = deferred<{
    context: ReturnType<typeof activityContext>;
    scope: "session";
    jobs: ReturnType<typeof activityJob>[];
    page: { complete: boolean; issues: [] };
  }>();
  client.on("evener/thread/activity/read", summary);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    cursor
      ? page.promise
      : {
          context: activityContext(ref),
          scope: "session",
          jobs: [activityJob({ ownerRef: ref })],
          page: { complete: false, issues: [], nextCursor: "next" },
        },
  );
  installFocusedScope(ref, summaryOf({ ref, title: "Count owner", tasks: { done: 1, total: 4 } }));
  connectionStore.getState().connect(client);
  activitySidebarStore.getState().openWith("jobs");
  render(
    <MotionProvider>
      <StatusBar />
      <ActivitySidebar />
    </MotionProvider>,
  );
  const footer = within(screen.getByTestId("statusbar"));
  await screen.findByRole("radio", { name: "Jobs, 2 of 6 running" });
  const expectCounts = (running: number, retained: number) => {
    expect(
      footer.getByRole("button", { name: `Jobs, ${running} of ${retained} running - open the activity sidebar` })
        .textContent,
    ).toContain(`${running}/${retained}`);
    expect(screen.getByRole("radio", { name: `Jobs, ${running} of ${retained} running` }).textContent).toContain(
      `${running}/${retained}`,
    );
  };
  expectCounts(2, 6);
  expect(footer.getByText("Jobs")).toBeTruthy();
  expect(footer.getByRole("button", { name: /Jobs, 2 of 6 running/ }).title).toBe("Jobs, 2 of 6 running");
  expect(screen.getByRole("radio", { name: "Watches, 2 of 3 armed" }).textContent).toBe("Watches\n2/3");
  expect(footer.getByRole("button", { name: /Agents, 0 of 2 active/ }).textContent).toContain("0/2");
  expect(screen.getByRole("radio", { name: "Agents, 0 of 2 active" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Watches, 2 of 3 armed" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Tasks, 1 of 4 done" })).toBeTruthy();
  expect(screen.queryByText("Activity kind")).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: "Load more jobs" }));
  expectCounts(2, 6);
  await act(async () => {
    page.resolve({
      context: activityContext(ref),
      scope: "session",
      jobs: [activityJob({ jobId: "second", ownerRef: ref, description: "Second job", command: "Second job" })],
      page: { complete: true, issues: [] },
    });
    await page.promise;
  });
  await screen.findByRole("button", { name: /Second job/ });
  expectCounts(2, 6);
  active = 1;
  total = 7;
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["summary"] },
    }),
  );
  await screen.findByRole("radio", { name: "Jobs, 1 of 7 running" });
  expectCounts(1, 7);
});
