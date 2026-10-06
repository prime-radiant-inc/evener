import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { LivenessLine } from "../../panes/session/transcript/flow/LivenessLine";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityContext, activityJob, activitySummary } from "../../stores/sessionActivityTestUtils";
import { ActivitySidebar } from "../activitybar/ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../activitybar/activitySidebarStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { StatusBar } from "./StatusBar";
import { installFocusedScope, summaryOf } from "./scopeTestUtils";

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
});

test("four footer categories keep their owning pane and About has no chip", async () => {
  const ref = "remote:pane";
  installFocusedScope(ref, summaryOf({ ref, title: "Pane owner", tasks: { total: 5, done: 2 } }));
  workspaceStore.setState({
    panes: [
      { id: "selected", type: "transcript", params: { ref: "remote:focused" }, slot: "main" },
      { id: "pane-secondary", type: "transcript", params: { ref }, slot: "secondary" },
    ],
    focusedPaneId: "selected",
  });
  connectionStore.getState().connect(activityClient());
  render(<StatusBar sessionRef={ref} paneId="pane-secondary" leading={null} />);
  await screen.findByRole("button", { name: /Jobs, 2 of 201 running/ });
  const chips = screen.getAllByRole("button");
  expect(chips.map((chip) => chip.dataset.activityTab)).toEqual(["agents", "jobs", "watches", "tasks"]);
  expect(chips.every((chip) => chip.dataset.sessionRef === ref)).toBe(true);
  expect(chips.every((chip) => chip.getAttribute("aria-label")?.endsWith(" - open Overview"))).toBe(true);
  expect(screen.queryByRole("button", { name: /About/ })).toBeNull();
  for (const tab of ["agents", "jobs", "watches", "tasks"] as const) {
    act(() => workspaceStore.getState().focusPane("selected"));
    const chip = chips.find((chip) => chip.dataset.activityTab === tab);
    if (!chip) throw new Error(`Missing ${tab} chip`);
    fireEvent.click(chip);
    expect(workspaceStore.getState().focusedPaneId).toBe("pane-secondary");
    expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab });
  }
});

test("uses its owning pane's session and focuses that pane before opening activity", async () => {
  const user = userEvent.setup();
  const client = activityClient();
  installFocusedScope("remote:focused");
  workspaceStore.setState({
    panes: [
      { id: "selected", type: "transcript", params: { ref: "remote:focused" }, slot: "main" },
      { id: "pane-secondary", type: "transcript", params: { ref: "remote:pane" }, slot: "secondary" },
    ],
    focusedPaneId: "selected",
  });
  connectionStore.getState().connect(client);

  render(<StatusBar sessionRef="remote:pane" paneId="pane-secondary" leading={<span>/work/pane</span>} />);

  expect(await screen.findByText("/work/pane")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: /Jobs, 2 of 201 running/ }));
  expect(workspaceStore.getState().focusedPaneId).toBe("pane-secondary");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
  expect(
    client.calls
      .filter((call) => call.method.includes("activity"))
      .every((call) => (call.params as { ref?: string }).ref === "remote:pane"),
  ).toBe(true);
});

test.each(["button", "Escape"])("closing activity with %s restores its keyboard opener", async (gesture) => {
  const user = userEvent.setup();
  installFocusedScope("remote:keyboard");
  connectionStore.getState().connect(activityClient());
  render(
    <MotionProvider>
      <StatusBar sessionRef="remote:keyboard" paneId="selected" leading={null} />
      <ActivitySidebar />
    </MotionProvider>,
  );
  const opener = await screen.findByRole("button", { name: /Jobs, 2 of 201 running/ });
  opener.focus();
  await user.keyboard("{Enter}");
  const close = await screen.findByRole("button", { name: "Close Overview" });
  close.focus();
  await user.keyboard(gesture === "button" ? "{Enter}" : "{Escape}");
  expect(activitySidebarStore.getState().open).toBe(false);
  expect(document.activeElement).toBe(opener);
});

test("an open activity sidebar returns focus to the latest pane's replacement chip", async ({ onTestFinished }) => {
  const user = userEvent.setup();
  installFocusedScope("remote:first");
  workspaceStore.setState({
    panes: [
      { id: "pane-first", type: "transcript", params: { ref: "remote:first" }, slot: "main" },
      { id: "pane-second", type: "transcript", params: { ref: "remote:second" }, slot: "secondary" },
    ],
    focusedPaneId: "pane-first",
  });
  connectionStore.getState().connect(activityClient());
  const first = render(<StatusBar sessionRef="remote:first" paneId="pane-first" leading={null} />);
  const second = render(<StatusBar sessionRef="remote:second" paneId="pane-second" leading={null} />);
  render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
  let focusedPaneWhenSidebarChanged: string | null = null;
  const stopObserving = activitySidebarStore.subscribe((state, previous) => {
    if (state.tab !== previous.tab) focusedPaneWhenSidebarChanged = workspaceStore.getState().focusedPaneId;
  });
  onTestFinished(stopObserving);

  await user.click(await within(first.container).findByRole("button", { name: /Agents, counts unknown/ }));
  await user.click(await within(second.container).findByRole("button", { name: /Jobs, 2 of 201 running/ }));

  expect(focusedPaneWhenSidebarChanged).toBe("pane-second");
  const close = await screen.findByRole("button", { name: "Close Overview" });
  close.focus();
  second.unmount();
  const replacement = render(<StatusBar sessionRef="remote:second" paneId="pane-second" leading={null} />);
  const replacementChip = await within(replacement.container).findByRole("button", {
    name: /Jobs, 2 of 201 running/,
  });
  await user.click(close);

  expect(document.activeElement).toBe(replacementChip);
});

test("dismissing activity while focus is outside keeps the user's current focus", async () => {
  const user = userEvent.setup();
  installFocusedScope("remote:keyboard");
  connectionStore.getState().connect(activityClient());
  render(
    <MotionProvider>
      <StatusBar sessionRef="remote:keyboard" paneId="selected" leading={null} />
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
      <StatusBar sessionRef="remote:keyboard" paneId="selected" leading={null} />
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
  await user.click(await screen.findByRole("button", { name: "Close Overview" }));
  expect(document.activeElement).toBe(screen.getByRole("button", { name: /Jobs, 2 of 201 running/ }));
});

test("selected child gets authoritative counts before its navigation location exists", async () => {
  const ref = "remote:deep-child",
    client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...activitySummary(ref, scope),
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
  render(<StatusBar sessionRef={ref} paneId="selected" leading={null} />);
  expect(await screen.findByRole("button", { name: /Agents, 3 of 20 active/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Root" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Parent" })).toBeNull();
  expect(screen.queryByText("Finding session context…")).toBeNull();
  // The session's summary and its subtree's (the subagent count).
  expect(client.calls.map((c) => c.method)).toEqual([
    "thread/read",
    "evener/thread/activity/read",
    "evener/thread/activity/read",
  ]);
  expect(client.calls.every((c) => (c.params as { ref?: string }).ref === ref)).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: /Jobs, 2 of 201 running/ }));
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
});

test("unknown counts remain unknown and pending ancestry is explicit", async () => {
  const ref = "remote:pending",
    client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...activitySummary(ref, scope),
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
      <StatusBar sessionRef={ref} paneId="selected" leading={null} />
      <ActivitySidebar />
    </MotionProvider>,
  );
  await waitFor(() => expect(screen.getByRole("button", { name: /Agents, counts unknown/ })).toBeTruthy());
  expect(screen.getByRole("radio", { name: "Agents, counts unknown" }).textContent).toContain("—");
  expect(screen.getByRole("button", { name: /Agents, counts unknown/ }).textContent).toContain("—");
  expect(screen.getAllByText("Finding session context…")).toHaveLength(1);
  expect(screen.queryByRole("button", { name: /Agents, 0 of/ })).toBeNull();
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
});

test("embedded Tasks counts still use the exact selected navigation row", async () => {
  const ref = "remote:tasks",
    client = activityClient();
  installFocusedScope(ref, summaryOf({ ref, title: "Tasks owner", tasks: { total: 5, done: 2 } }));
  connectionStore.getState().connect(client);
  render(<StatusBar sessionRef={ref} paneId="selected" leading={null} />);
  expect(await screen.findByRole("button", { name: /Tasks, 2 of 5 done/ })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: /Tasks, 2 of 5 done/ }));
  expect(activitySidebarStore.getState().tab).toBe("tasks");
});

test("footer and tabs explain summary counts through paging and activity changes", async () => {
  const ref = "remote:counts";
  const client = activityClient();
  let active = 2;
  let total = 6;
  const summary = ({ scope }: { scope?: "session" | "subtree" }) => ({
    ...activitySummary(ref, scope),
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
      <StatusBar sessionRef={ref} paneId="selected" leading={null} />
      <ActivitySidebar />
    </MotionProvider>,
  );
  const footer = within(screen.getByTestId("statusbar"));
  await screen.findByRole("radio", { name: "Jobs, 2 of 6 running" });
  const expectCounts = (running: number, retained: number) => {
    expect(
      footer.getByRole("button", { name: `Jobs, ${running} of ${retained} running - open Overview` }).textContent,
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
  expect(screen.queryByText("Overview kind")).toBeNull();
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

// "Running subagents" means every open subagent at every depth, on every web
// surface that counts them. In this tree the session has one child of its
// own, still running, and that child runs two more: the chip, the sidebar's
// Agents tab and the liveness line all say 3 of 4, never the session's own 1.
test("the chip, the sidebar and the liveness line count subagents at every depth", async () => {
  const ref = "remote:nested";
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates:
      scope === "subtree"
        ? { known: true, total: 4, active: 3, failed: 0, completed: 1 }
        : { known: true, total: 1, active: 1, failed: 0, completed: 0 },
  }));
  installFocusedScope(ref);
  connectionStore.getState().connect(client);
  render(
    <MotionProvider>
      <StatusBar sessionRef={ref} paneId="selected" leading={null} />
      <ActivitySidebar />
      <LivenessLine lastFrameAt={0} now={60_000} active={true} sessionRef={ref} turnId="turn_0" />
    </MotionProvider>,
  );
  const chip = await screen.findByRole("button", { name: /Agents, 3 of 4 active/ });
  await waitFor(() => expect(screen.getByTestId("liveness-line").textContent).toBe("Waiting on 3 subagents"));
  fireEvent.click(chip);
  expect(await screen.findByRole("radio", { name: "Agents, 3 of 4 active" })).toBeTruthy();
});
