import { createNavigationStore } from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import "../../panes/transcript";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityContext, activityJob, activityWatch } from "../../stores/sessionActivityTestUtils";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { deriveScope } from "../statusbar/statusScope";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivityViewport } from "./ActivityViewport";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";
import { JobsTab } from "./JobsTab";
import { WatchesTab } from "./WatchesTab";

const scope = () =>
  deriveScope(
    createNavigationStore({ persistence: memoryNavigationPersistence() }).getState(),
    "remote:owner",
    null,
    null,
  );
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
  connectionStore.setState({ client: null, state: "idle" });
});
test("jobs show supplied terminal status and have no invented transcript action", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [
      activityJob({
        description: "",
        transcriptRef: undefined,
        command: "real status",
        status: "command_exited_nonzero",
        terminal: true,
        reason: "exit 2",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<JobsTab scope={scope()} />);
  fireEvent.click(await screen.findByText("1 completed job"));
  expect(screen.getByText("real status")).toBeTruthy();
  expect(screen.getByText(/Command failed/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: /real status/ })).toBeNull();
});
test("watch tab reads typed receiver state and preserves cadence/delivery vocabulary", async () => {
  const client = activityClient();
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [
      activityWatch({ note: "heartbeat", cadence: [{ kind: "every", seconds: 300 }] }),
      activityWatch({
        id: "fired",
        note: "one shot",
        active: false,
        deliveries: 1,
        cadence: [{ kind: "after", seconds: 600 }],
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<WatchesTab scope={scope()} />);
  expect(await screen.findByText("heartbeat")).toBeTruthy();
  expect(screen.getByText("every 5m · armed")).toBeTruthy();
  expect(screen.getByText("after 10m · 1 delivery · not armed")).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
});

test("watch disclosure reveals its complete condition and keeps equal IDs in different receivers separate", async () => {
  const client = activityClient();
  const note = "Follow the release monitor until the readiness marker appears in its output";
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [activityWatch({ note, outputMatch: "READY_FOR_REVIEW", target: "release-monitor" }, ref)],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  const view = render(<WatchesTab scope={scope()} />);
  const summary = await screen.findByText(note);
  expect(screen.queryByTestId("watch-facts")).toBeNull();
  fireEvent.click(summary);
  expect(screen.getByTestId("watch-note").textContent).toBe(note);
  expect(screen.getByTestId("watch-facts").textContent).toContain("READY_FOR_REVIEW");
  expect(screen.getByTestId("watch-facts").textContent).toContain("release-monitor");
  expect(client.calls.filter((call) => call.method === "evener/thread/watches/list")).toHaveLength(1);
  view.rerender(
    <WatchesTab
      scope={deriveScope(
        createNavigationStore({ persistence: memoryNavigationPersistence() }).getState(),
        "other:receiver",
        null,
        null,
      )}
    />,
  );
  await screen.findByText(note);
  expect(screen.queryByTestId("watch-facts")).toBeNull();
});

test("all terminal jobs fold, and revealed output opens its authoritative owner pane", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [
      activityJob({
        description: "",
        jobId: "ok",
        command: "successful command",
        status: "completed",
        terminal: true,
        outcome: "success",
        ownerRef: "source:owner",
        transcriptRef: "job:raw-output",
      }),
      activityJob({
        description: "",
        jobId: "ok-no-output",
        command: "successful no output",
        status: "completed",
        terminal: true,
        outcome: "success",
        transcriptRef: undefined,
      }),
      ...["running", "command_exited_nonzero", "killed", "cancelled", "stopped", "unknown"].map((status) =>
        activityJob({
          description: "",
          jobId: status,
          command: `command ${status}`,
          status,
          terminal: status !== "running",
          outcome: status === "running" ? "success" : ["cancelled", "stopped"].includes(status) ? "stopped" : "failed",
        }),
      ),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  resetWorkspaceStoreForTests();
  render(<JobsTab scope={scope()} />);
  await screen.findByText("command running");
  expect(screen.queryByText("successful command")).toBeNull();
  for (const status of ["command_exited_nonzero", "killed", "cancelled", "stopped", "unknown"])
    expect(screen.queryByText(`command ${status}`)).toBeNull();
  fireEvent.click(screen.getByText("7 completed jobs"));
  for (const status of ["command_exited_nonzero", "killed", "cancelled", "stopped", "unknown"])
    expect(screen.getByText(`command ${status}`)).toBeTruthy();
  for (const text of ["completed", "Command failed", "killed", "cancelled", "stopped", "unknown"])
    expect(screen.getAllByText(text).length).toBeGreaterThan(0);
  fireEvent.click(screen.getByRole("button", { name: /successful command/ }));
  const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
  expect(opened).toMatchObject({
    slot: "secondary",
    params: { ref: "job:raw-output", parentRef: "source:owner" },
  });
  expect(workspaceStore.getState().focusedPaneId).toBe(opened?.id);
  expect(screen.getByText("successful no output")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /successful no output/ })).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(1);
});

test("job history disclosure survives remount only for its selected session", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [
      activityJob({
        description: "",
        ownerRef: ref,
        command: ref,
        terminal: true,
        outcome: "success",
        status: "completed",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  const view = render(<JobsTab scope={scope()} />);
  fireEvent.click(await screen.findByText("1 completed job"));
  expect(screen.getByText("remote:owner")).toBeTruthy();
  view.unmount();
  const again = render(<JobsTab scope={scope()} />);
  expect(await screen.findByText("remote:owner")).toBeTruthy();
  again.rerender(
    <JobsTab
      scope={deriveScope(
        createNavigationStore({ persistence: memoryNavigationPersistence() }).getState(),
        "other:owner",
        null,
        null,
      )}
    />,
  );
  await screen.findByText("1 completed job");
  expect(screen.queryByText("other:owner")).toBeNull();
});

test("closed terminal history discovers page-three live work and keeps its settled anchor and output", async () => {
  const client = activityClient();
  let finished = false;
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
  client.on("evener/thread/jobs/list", ({ ref, scope, cursor }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs:
      cursor === "third"
        ? [
            activityJob({
              description: "",
              jobId: "older-active",
              command: "older active",
              ownerRef: "source:owner",
              transcriptRef: "job:later-output",
              terminal: finished,
              status: finished ? "completed" : "running",
              outcome: finished ? "success" : undefined,
            }),
          ]
        : cursor
          ? [
              activityJob({
                description: "",
                jobId: "older-failed",
                command: "older failure",
                status: "command_exited_nonzero",
                terminal: true,
                outcome: "failed",
              }),
            ]
          : [
              activityJob({
                description: "",
                jobId: "ok",
                command: "hidden success",
                status: "completed",
                terminal: true,
                outcome: "success",
              }),
            ],
    page:
      cursor === "third"
        ? { complete: true, issues: [] }
        : { complete: false, nextCursor: cursor ? "third" : "next", issues: [] },
  }));
  try {
    connectionStore.getState().connect(client);
    activitySidebarStore.getState().openFor("remote:owner", "jobs");
    await act(async () => {
      render(
        <ActivityViewport sessionRef="remote:owner" tab="jobs" className="activity-viewport">
          <JobsTab scope={scope()} />
        </ActivityViewport>,
      );
    });
    expect(screen.queryByText("hidden success")).toBeNull();
    await act(async () => intersect?.());
    expect(screen.queryByText("older active")).toBeNull();
    expect(screen.queryByText("older failure")).toBeNull();
    await act(async () => intersect?.());
    expect(screen.getByText("older active")).toBeTruthy();
    expect(screen.queryByText("older failure")).toBeNull();
    expect(screen.queryByText("hidden success")).toBeNull();
    expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(3);
    finished = true;
    act(() =>
      client.emitNotification(activityChangedNotification({ ref: "remote:owner", threadId: "owner" }, ["jobs"])),
    );
    await screen.findByText("3 completed jobs");
    await waitFor(() => expect(screen.queryByText("older active")).toBeNull());
    fireEvent.click(screen.getByText("3 completed jobs"));
    expect(screen.getByText("older failure")).toBeTruthy();
    const late = screen.getByRole("button", { name: /older active/ });
    expect(late.dataset.activityAnchor).toBe('job:["source:owner","older-active"]');
    expect(document.querySelectorAll('[data-activity-anchor=\'job:["source:owner","older-active"]\']')).toHaveLength(1);
    fireEvent.click(late);
    const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
    expect(opened).toMatchObject({ slot: "secondary", params: { ref: "job:later-output", parentRef: "source:owner" } });
    expect(workspaceStore.getState().focusedPaneId).toBe(opened?.id);
  } finally {
    vi.unstubAllGlobals();
  }
});
