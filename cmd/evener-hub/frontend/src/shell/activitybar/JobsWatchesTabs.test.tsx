import { createNavigationStore } from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityContext, activityJob, activityWatch } from "../../stores/sessionActivityTestUtils";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { deriveScope } from "../statusbar/statusScope";
import { workspaceStore } from "../workspace";
import { JobsTab } from "./JobsTab";
import { WatchesTab } from "./WatchesTab";

const scope = () =>
  deriveScope(createNavigationStore({ persistence: memoryNavigationPersistence() }).getState(), "remote:owner");
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
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
  expect(await screen.findByText("real status")).toBeTruthy();
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
      )}
    />,
  );
  await screen.findByText(note);
  expect(screen.queryByTestId("watch-facts")).toBeNull();
});

test("only successful terminal jobs fold, and revealed output retains its authoritative identity", async () => {
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
  const open = vi.spyOn(workspaceStore.getState(), "openPane").mockImplementation(() => "test-pane");
  render(<JobsTab scope={scope()} />);
  await screen.findByText("command running");
  expect(screen.queryByText("successful command")).toBeNull();
  for (const status of ["command_exited_nonzero", "killed", "cancelled", "stopped", "unknown"])
    expect(screen.getByText(`command ${status}`)).toBeTruthy();
  fireEvent.click(screen.getByText("2 completed jobs"));
  fireEvent.click(screen.getByRole("button", { name: /successful command/ }));
  expect(open).toHaveBeenCalledWith(
    "transcript",
    { ref: "job:raw-output", parentRef: "source:owner" },
    { slot: "secondary" },
  );
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
      )}
    />,
  );
  await screen.findByText("1 completed job");
  expect(screen.queryByText("other:owner")).toBeNull();
});

test("closed successful history preserves automatic discovery of older active and failed jobs", async () => {
  const client = activityClient();
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
    jobs: cursor
      ? [
          activityJob({ description: "", jobId: "older-active", command: "older active" }),
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
    page: cursor ? { complete: true, issues: [] } : { complete: false, nextCursor: "next", issues: [] },
  }));
  try {
    connectionStore.getState().connect(client);
    await act(async () => {
      render(<JobsTab scope={scope()} />);
    });
    expect(screen.queryByText("hidden success")).toBeNull();
    await act(async () => intersect?.());
    expect(screen.getByText("older active")).toBeTruthy();
    expect(screen.getByText("older failure")).toBeTruthy();
    expect(screen.queryByText("hidden success")).toBeNull();
    expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list")).toHaveLength(2);
  } finally {
    vi.unstubAllGlobals();
  }
});
