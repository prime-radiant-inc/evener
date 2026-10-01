import { hydrateThread } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
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
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { SessionActivityAction } from "../composer/SessionActivityAction";
import { installMobileViewport } from "../testing/mobileViewport";
import { ActivityPanel, ActivityPanelBody } from "./ActivityPanel";
import { SessionChrome } from "./SessionChrome";

const ref = "remote:owner";
const model = (sessionRef = ref) => hydrateThread(activityThread(sessionRef), sessionRef, 0);
let restoreViewport: () => void;

beforeEach(() => {
  restoreViewport = installMobileViewport();
  resetWorkspaceStoreForTests();
  resetChromeStoreForTests();
  resetActivityPanelStoreForTests();
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetActivityPanelStoreForTests();
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
  client.on("evener/jobs/output", () => ({ data: { tail: "build output", totalBytes: 12, retainedStart: 0 } }));
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

test("mobile overview exposes failed work while successful completed work stays folded", async () => {
  connectActivity();
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  const failed = await screen.findByRole("treeitem", { name: "Failed checks" });
  expect(within(failed).getByRole("img", { name: "Failed" })).toBeTruthy();
  expect(screen.getByRole("treeitem", { name: "2 inactive" })).toBeTruthy();
  expect(screen.queryByRole("treeitem", { name: "Completed checks" })).toBeNull();
  fireEvent.click(screen.getByRole("treeitem", { name: "2 inactive" }));
  expect(screen.getByRole("treeitem", { name: "Completed checks" })).toBeTruthy();
  expect(screen.getAllByRole("treeitem", { name: "Failed checks" })).toHaveLength(1);
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
    fireEvent.click(
      within(screen.getByRole("treeitem", { name: "Observer" })).getByRole("button", { name: "Open transcript" }),
    );
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

test("narrow Activity action opens the single chrome sheet and labels only known active counts", async () => {
  const client = connectActivity();
  let known = false;
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref),
    scope: scope ?? "session",
    delegates: { known, total: 8, active: 0, failed: 0, completed: 8 },
  }));
  await threadsStore.getState().ensureThread(ref);
  render(
    <ClientProvider client={client}>
      <SessionActivityAction sessionRef={ref} />
      <SessionChrome ref={ref} placement="composer" />
    </ClientProvider>,
  );
  expect(await screen.findByRole("button", { name: "Activity" })).toBeTruthy();
  known = true;
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["summary"] },
    }),
  );
  fireEvent.click(await screen.findByRole("button", { name: "Activity · 2 active" }));
  expect(await screen.findByRole("dialog", { name: "Activity" })).toBeTruthy();
  expect(screen.queryByRole("menu")).toBeNull();
});
