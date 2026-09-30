import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { MotionProvider } from "../../motion";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { activityClient, activityContext, activityDelegate } from "../../stores/sessionActivityTestUtils";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

const ref = "remote:owner";
const mount = () =>
  render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
afterEach(() => {
  cleanup();
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
  fireEvent.click(screen.getByRole("radio", { name: "Jobs 2" }));
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
