import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activitySummary,
} from "../../stores/sessionActivityTestUtils";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { currentSessionRef, resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

beforeAll(async () => {
  await import("../../panes/transcript");
});

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetDisclosureStoreForTests();
});

const ref = "remote:owner";
const mount = () =>
  render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
});

test("closed sidebar owns no read and an open tab observes only its own collection", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  mount();
  expect(screen.queryByTestId("activity-sidebar")).toBeNull();
  expect(client.calls).toHaveLength(0);
  act(() => activitySidebarStore.getState().openWith("agents"));
  await screen.findByRole("button", { name: /inspect/ });
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
  fireEvent.click(screen.getByRole("radio", { name: "Jobs, 2 of 201 running" }));
  await screen.findByText("No jobs at this level.");
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Close the activity sidebar" }));
  await waitFor(() => expect(sessionActivitySnapshot(client, ref, "session")).toBeNull());
});

test("inactive fold and explicit keyset pages preserve useful loaded rows", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    delegates: cursor
      ? [activityDelegate({ delegateId: "second", childRef: "remote:second", description: "second bounded page" })]
      : [
          activityDelegate(),
          ...Array.from({ length: 21 }, (_, i) =>
            activityDelegate({
              delegateId: `done-${i}`,
              childRef: `remote:done-${i}`,
              description: `done ${i}`,
              terminal: true,
              status: "completed",
              phase: "done",
              lifecycle: "idle",
            }),
          ),
        ],
    page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "page-two" } : {}) },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /Inactive subagents \(21\)/ });
  expect(screen.queryByRole("button", { name: /done 0/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Inactive subagents/ }));
  expect(screen.getByRole("button", { name: /done 0/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /done 20/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Show 1 more/ }));
  expect(screen.getByRole("button", { name: /done 20/ })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Load more subagents" }));
  await screen.findByRole("button", { name: /second bounded page/ });
  expect(screen.getByRole("button", { name: /inspect/ })).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list").map((c) => c.params)).toEqual([
    { ref, scope: "session" },
    { ref, scope: "session", cursor: "page-two" },
  ]);
});

test("a fresh sidebar case starts with inactive delegates folded after a prior reader expanded them", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", () => ({
    context: activityContext(),
    scope: "session",
    delegates: [
      activityDelegate({
        delegateId: "done-fresh",
        childRef: "remote:done-fresh",
        description: "Fresh completed delegate",
        terminal: true,
        status: "completed",
        phase: "done",
        lifecycle: "idle",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /Inactive subagents/ });
  expect(screen.queryByRole("button", { name: /Fresh completed delegate/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Inactive subagents/ }));
  expect(screen.getByRole("button", { name: /Fresh completed delegate/ })).toBeTruthy();
});

test("exact child focus switches demand without sibling leakage or a navigation activity reader", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [
      activityDelegate({
        ownerRef: ref,
        description: ref === "remote:child" ? "child-owned delegate" : "root-only sibling",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /root-only sibling/ });
  act(() => installFocusedScope("remote:child"));
  await screen.findByRole("button", { name: /child-owned delegate/ });
  expect(screen.queryByRole("button", { name: /root-only sibling/ })).toBeNull();
  expect(sessionActivitySnapshot(client, ref, "session")).toBeNull();
  expect(client.calls.filter((c) => c.method === "evener/navigation/read")).toHaveLength(0);
});

test("Escape dismisses only an unclaimed sidebar gesture", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  const claim = (event: KeyboardEvent) => event.preventDefault();
  window.addEventListener("keydown", claim);
  try {
    mount();
    await screen.findByRole("button", { name: /inspect/ });
    fireEvent.keyDown(window, { key: "Escape" });
    expect(activitySidebarStore.getState().open).toBe(true);
  } finally {
    window.removeEventListener("keydown", claim);
  }
  fireEvent.keyDown(window, { key: "Escape" });
  expect(activitySidebarStore.getState().open).toBe(false);
});

test("delegate drill and proven parent links restore exact scope at the unchanged root URL", async () => {
  const root = "remote:root",
    child = "remote:child",
    grandchild = "remote:grandchild";
  const client = activityClient();
  const context = (ref: string) => ({
    ...activityContext(ref),
    rootRef: root,
    ancestors:
      ref === root
        ? []
        : [
            { ref: root, sessionId: "root", title: "Root work" },
            ...(ref === grandchild ? [{ ref: child, sessionId: "child", title: "Child work" }] : []),
          ],
  });
  client.on("evener/thread/activity/read", ({ ref }) => ({ ...activitySummary(ref), context: context(ref) }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    delegates:
      ref === grandchild
        ? []
        : [
            activityDelegate({
              ownerRef: ref,
              childRef: ref === root ? child : grandchild,
              description: ref === root ? "Open child" : "Open grandchild",
            }),
          ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(root);
  workspaceStore.setState({
    panes: [{ id: "root", type: "session", params: { ref: root }, slot: "main" }],
    focusedPaneId: "root",
  });
  window.history.replaceState({}, "", "/s/remote%3Aroot");
  activitySidebarStore.getState().openWith("agents");
  mount();
  fireEvent.click(await screen.findByRole("button", { name: /Open child/ }));
  fireEvent.click(await screen.findByRole("button", { name: /Open grandchild/ }));
  await screen.findByRole("button", { name: "Child work" });
  expect(currentSessionRef(workspaceStore.getState())).toBe(grandchild);
  fireEvent.click(screen.getByRole("button", { name: "Child work" }));
  await screen.findByRole("button", { name: /Open grandchild/ });
  expect(currentSessionRef(workspaceStore.getState())).toBe(child);
  fireEvent.click(screen.getByRole("button", { name: "Root work" }));
  await screen.findByRole("button", { name: /Open child/ });
  expect(currentSessionRef(workspaceStore.getState())).toBe(root);
  expect(window.location.pathname).toBe("/s/remote%3Aroot");
});

test("activity tabs keep a named keyboard radio group without a visible heading", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /inspect/ });
  expect(screen.getByRole("radiogroup", { name: "Activity kind" })).toBeTruthy();
  expect(screen.queryByText("Activity kind")).toBeNull();
  await act(async () => fireEvent.keyDown(screen.getByRole("radio", { name: /Agents/ }), { key: "End" }));
  expect(activitySidebarStore.getState().tab).toBe("about");
  await act(async () => fireEvent.keyDown(screen.getByRole("radio", { name: "About" }), { key: "Home" }));
  expect(activitySidebarStore.getState().tab).toBe("agents");
  await act(async () => fireEvent.keyDown(screen.getByRole("radio", { name: /Agents/ }), { key: "ArrowRight" }));
  expect(activitySidebarStore.getState().tab).toBe("jobs");
});

test("pending ancestry becomes useful parent navigation only after the domain proves it", async () => {
  let known = false;
  const client = activityClient();
  const context = () => ({
    ...activityContext(ref),
    ancestryKnown: known,
    rootRef: "remote:root",
    ancestors: [{ ref: "remote:root", sessionId: "root", title: "Proven root" }],
  });
  client.on("evener/thread/activity/read", () => ({ ...activitySummary(ref), context: context() }));
  client.on("evener/thread/delegates/list", () => ({
    context: context(),
    scope: "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByText("No subagents at this level.");
  expect(screen.getByText("Finding session context…")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Proven root" })).toBeNull();
  known = true;
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["summary"] },
    }),
  );
  await screen.findByRole("button", { name: "Proven root" });
  expect(screen.queryByText("Finding session context…")).toBeNull();
});
