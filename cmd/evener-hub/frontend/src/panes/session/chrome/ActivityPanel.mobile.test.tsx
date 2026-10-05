import { hydrateThread } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../../shell/activitybar/activitySidebarStore";
import { resetChromeStoreForTests } from "../../../shell/chromeStore";
import { ClientProvider } from "../../../shell/clientContext";
import { StackHost } from "../../../shell/mobile/StackHost";
import { type PaneProps, registerPaneForTests } from "../../../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../../stores/activityPanel";
import { connectionStore } from "../../../stores/connection";
import { resetNavigationStoreForTests } from "../../../stores/navigation/store";
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
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { installMobileViewport } from "../testing/mobileViewport";
import { ActivityPanel, ActivityPanelBody } from "./ActivityPanel";

const ref = "remote:owner";
const model = (sessionRef = ref) => hydrateThread(activityThread(sessionRef), sessionRef, 0);
let restoreViewport: () => void;

beforeEach(() => {
  restoreViewport = installMobileViewport();
  resetWorkspaceStoreForTests();
  resetChromeStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  restoreViewport();
  window.history.pushState({}, "", "/");
});

function connectActivity() {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref),
    scope: scope ?? "session",
    delegates: { known: true, total: 1, active: 0, failed: 0, completed: 1 },
    jobs: { known: true, total: 4, active: 2, failed: 1, completed: 1 },
    watches: { known: true, total: 3, active: 3, failed: 0, completed: 0 },
  }));
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [
      activityJob({ jobId: "build", description: "Build app", command: "npm run build" }),
      activityJob({ jobId: "tests", description: "Run tests", command: "npm test" }),
      activityJob({ jobId: "failed", description: "Failed checks", terminal: true, outcome: "failure" }),
      activityJob({ jobId: "done", description: "Completed checks", terminal: true, outcome: "success" }),
    ],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [activityDelegate({ task: "Observer", terminal: true, outcome: "completed", lifecycle: "idle" })],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: ["heartbeat", "follow-up", "progress"].map((note) =>
      activityWatch({ id: note, note, cadence: [{ kind: "every", seconds: 60 }] }, ref),
    ),
    page: { complete: true, issues: [] },
  }));
  client.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: 12,
      totalBytes: 12,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: "build output",
    },
  }));
  connectionStore.getState().connect(client);
  return client;
}

test("mobile Activity starts with compact details and each watch or job opens in one disclosure", async () => {
  const client = connectActivity();
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  await screen.findByRole("treeitem", { name: "Run tests" });

  for (const name of ["Watch: heartbeat", "Watch: follow-up", "Watch: progress", "Build app", "Run tests"]) {
    expect(screen.getByRole("button", { name: `Show details for ${name}` })).toBeTruthy();
  }
  expect(screen.queryByTestId("watch-facts")).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/jobs/output")).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Show details for Watch: heartbeat" }));
  expect(screen.getByTestId("watch-facts").textContent).toContain("every 1m");
  fireEvent.click(screen.getByRole("button", { name: "Show details for Build app" }));
  expect(await screen.findByText("build output")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Show details for Run tests" })).toBeTruthy();
});

test("mobile recursive Activity folds all settled outcomes with quiet truthful rows", async () => {
  connectActivity();
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  const fold = await screen.findByRole("treeitem", { name: "3 inactive" });
  expect(screen.queryByRole("treeitem", { name: "Failed checks" })).toBeNull();
  expect(screen.queryByRole("treeitem", { name: "Completed checks" })).toBeNull();
  fireEvent.click(fold);
  const failed = screen.getByRole("treeitem", { name: "Failed checks" });
  const glyph = within(failed).getByRole("img", { name: "Failed" });
  expect(glyph.className).not.toContain("kindDanger");
  expect(screen.getByRole("treeitem", { name: "Completed checks" })).toBeTruthy();
  expect(screen.getAllByRole("treeitem", { name: "Failed checks" })).toHaveLength(1);
  expect(failed.getAttribute("aria-expanded")).toBe("false");
  expect(screen.getAllByTestId("watch-glyph")).toHaveLength(3);
  fireEvent.click(fold);
  expect(screen.queryByRole("treeitem", { name: "Failed checks" })).toBeNull();
});

test("a folded failed job opens actual output through its authoritative owner, not an equal ID", async () => {
  const client = activityClient();
  const jobId = "equal-job-id";
  const ownerRef = "source:opaque-owner";
  const failed = activityJob({
    jobId,
    ownerRef,
    description: "Failed owned output",
    terminal: true,
    status: "command_exited_nonzero",
    outcome: "failure",
    transcriptRef: `job:${jobId}`,
  });
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [failed, activityJob({ ...failed, ownerRef: "other:opaque-owner", description: "Other owner's equal ID" })],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/jobs/output", ({ ref, jobId: requestedJobId }) => {
    expect(ref).toBe(ownerRef);
    expect(requestedJobId).toBe(jobId);
    return {
      data: {
        offsetBytes: 0,
        bytesReturned: 27,
        totalBytes: 27,
        retainedStartBytes: 0,
        encoding: "utf8",
        data: "Authoritative failed output",
      },
    };
  });
  client.on("evener/jobs/get", ({ ref, jobId: requestedJobId }) => {
    expect(ref).toBe(ownerRef);
    expect(requestedJobId).toBe(jobId);
    return { data: failed };
  });
  connectionStore.getState().connect(client);
  const restoreSessionPane = registerPaneForTests<{ ref: string }>({
    id: "session",
    title: () => "Activity owner",
    component: lazy(() =>
      Promise.resolve({
        default: ({ params }: PaneProps<{ ref: string }>) => (
          <ActivityPanel sessionRef={params.ref} model={model(params.ref)} />
        ),
      }),
    ),
  });
  try {
    const ownerPane = workspaceStore.getState().openPane("session", { ref });
    render(
      <ClientProvider client={client}>
        <StackHost />
      </ClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
    const fold = await screen.findByRole("treeitem", { name: "2 inactive" });
    expect(screen.queryByRole("treeitem", { name: "Failed owned output" })).toBeNull();
    fireEvent.click(fold);
    const row = screen.getByRole("treeitem", { name: "Failed owned output" });
    expect(within(row).getByRole("img", { name: "Failed" }).className).not.toContain("kindDanger");
    fireEvent.click(within(row).getByRole("button", { name: "Open transcript" }));
    await act(async () => await vi.dynamicImportSettled());
    expect(await screen.findByTestId("joblog-content")).toHaveProperty("textContent", "Authoritative failed output");
    const workspace = workspaceStore.getState();
    expect(workspace.panes.find((pane) => pane.id === ownerPane)?.params).toEqual({ ref });
    expect(workspace.panes.find((pane) => pane.id === workspace.focusedPaneId)?.params).toEqual({
      ref: `job:${jobId}`,
      parentRef: ownerRef,
    });
    expect(client.calls.filter((call) => call.method === "evener/jobs/output").map((call) => call.params)).toEqual([
      { ref: ownerRef, jobId },
    ]);
    expect(
      client.calls.some(
        (call) => call.method === "thread/read" && (call.params as { ref: string }).ref === `job:${jobId}`,
      ),
    ).toBe(false);
  } finally {
    restoreSessionPane();
  }
});

test("mobile child transcript Back restores Activity and disclosures until explicitly closed", async () => {
  const client = connectActivity();
  const restoreSessionPane = registerPaneForTests<{ ref: string }>({
    id: "session",
    title: () => "Activity owner",
    component: lazy(() =>
      Promise.resolve({
        default: ({ params }: PaneProps<{ ref: string }>) => (
          <ActivityPanel sessionRef={params.ref} model={model(params.ref)} />
        ),
      }),
    ),
  });
  try {
    const ownerPane = workspaceStore.getState().openPane("session", { ref });
    await act(async () => {
      render(
        <ClientProvider client={client}>
          <StackHost />
        </ClientProvider>,
      );
    });
    fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
    const inactive = await screen.findByRole("treeitem", { name: /inactive/ });
    fireEvent.click(inactive);
    fireEvent.click(screen.getByRole("button", { name: "Show details for Observer" }));
    await act(async () => {
      fireEvent.click(
        within(screen.getByRole("treeitem", { name: "Observer" })).getByRole("button", { name: "Open transcript" }),
      );
      await vi.dynamicImportSettled();
    });
    await screen.findByText("No turns yet");
    const childPane = workspaceStore.getState().focusedPaneId;
    if (childPane === null) throw new Error("expected the child transcript to own focus");
    expect(workspaceStore.getState().panes.find((pane) => pane.id === childPane)?.params).toEqual({
      ref: "remote:child",
      parentRef: ref,
    });
    expect(screen.queryByRole("dialog", { name: "Activity" })).toBeNull();
    await waitFor(() => expect(sessionActivitySnapshot(client, ref, "subtree")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(await screen.findByRole("dialog", { name: "Activity" })).toBeTruthy();
    expect(workspaceStore.getState().focusedPaneId).toBe(ownerPane);
    expect(await screen.findByRole("button", { name: "Hide details for Observer" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Activity" })).toBeNull());
    act(() => workspaceStore.getState().focusPane(childPane));
    await screen.findByText("No turns yet");
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    await screen.findByRole("button", { name: /^Activity/ });
    expect(screen.queryByRole("dialog", { name: "Activity" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /^Activity/ }));
    expect(await screen.findByRole("button", { name: "Hide details for Observer" })).toBeTruthy();
    act(() => workspaceStore.getState().openPane("session", { ref: "remote:other" }));
    await screen.findByRole("button", { name: /^Activity/ });
    expect(screen.queryByRole("dialog", { name: "Activity" })).toBeNull();
    act(() => workspaceStore.getState().focusPane(ownerPane));
    expect(await screen.findByRole("dialog", { name: "Activity" })).toBeTruthy();
  } finally {
    restoreSessionPane();
  }
});

test("mobile child Back restores the loaded job extent and expanded older failure with fresh cursors", async () => {
  const client = connectActivity();
  const completed = Array.from({ length: 60 }, (_, index) =>
    activityJob({
      jobId: `history-${index}`,
      description: `Completed history ${index}`,
      terminal: true,
      outcome: "success",
    }),
  );
  const firstPage = [
    activityJob({ jobId: "build", description: "Current build" }),
    activityJob({ jobId: "monitor", description: "Current monitor" }),
    ...completed.slice(0, 48),
  ];
  const olderPage = [
    ...completed.slice(48),
    activityJob({ jobId: "failed", description: "Older failed checks", terminal: true, outcome: "failure" }),
  ];
  let walk = 0;
  client.on("evener/thread/jobs/list", ({ cursor, ref, scope }) => {
    if (!cursor) walk += 1;
    else expect(cursor).toBe(`older-${walk}`);
    return {
      context: activityContext(ref),
      scope: scope ?? "session",
      jobs: cursor ? olderPage : firstPage,
      page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: `older-${walk}` } : {}) },
    };
  });
  client.on("evener/jobs/output", () => ({
    data: {
      offsetBytes: 0,
      bytesReturned: 20,
      totalBytes: 20,
      retainedStartBytes: 0,
      encoding: "utf8",
      data: "Older failure output",
    },
  }));
  const restoreSessionPane = registerPaneForTests<{ ref: string }>({
    id: "session",
    title: () => "Activity owner",
    component: lazy(() =>
      Promise.resolve({
        default: ({ params }: PaneProps<{ ref: string }>) => (
          <ActivityPanel sessionRef={params.ref} model={model(params.ref)} />
        ),
      }),
    ),
  });
  try {
    workspaceStore.getState().openPane("session", { ref });
    render(
      <ClientProvider client={client}>
        <StackHost />
      </ClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: /^Activity/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Load more jobs" }));
    fireEvent.click(await screen.findByRole("treeitem", { name: "62 inactive" }));
    fireEvent.click(await screen.findByRole("button", { name: "Show details for Older failed checks" }));
    expect(await screen.findByText("Older failure output")).toBeTruthy();

    await act(async () => {
      fireEvent.click(
        within(screen.getByRole("treeitem", { name: "Observer" })).getByRole("button", { name: "Open transcript" }),
      );
      await vi.dynamicImportSettled();
    });
    await screen.findByText("No turns yet");
    await waitFor(() => expect(sessionActivitySnapshot(client, ref, "subtree")).toBeNull());
    const jobReadsBeforeBack = client.calls.filter(
      (call) => call.method === "evener/thread/jobs/list" && (call.params as { scope?: string }).scope === "subtree",
    ).length;
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    const dialog = await screen.findByRole("dialog", { name: "Activity" });
    expect(await within(dialog).findByRole("button", { name: "Hide details for Older failed checks" })).toBeTruthy();
    expect(await within(dialog).findByText("Older failure output")).toBeTruthy();
    expect(within(dialog).getByRole("treeitem", { name: "62 inactive" })).toBeTruthy();
    expect(within(dialog).getByRole("treeitem", { name: "Completed history 59" })).toBeTruthy();
    expect(
      client.calls
        .filter(
          (call) =>
            call.method === "evener/thread/jobs/list" && (call.params as { scope?: string }).scope === "subtree",
        )
        .slice(jobReadsBeforeBack)
        .map((call) => call.params),
    ).toEqual([
      { ref, scope: "subtree" },
      { ref, scope: "subtree", cursor: `older-${walk}` },
    ]);
  } finally {
    restoreSessionPane();
  }
});
