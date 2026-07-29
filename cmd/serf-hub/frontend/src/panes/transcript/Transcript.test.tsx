import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { registerPane } from "../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import Transcript from "./Transcript";

// A minimal, test-only "session" pane registration - mirrors
// subagentModule.test.tsx's own precedent: real registerPane/paneFor/openPane
// machinery, without pulling in the actual (heavier) panes/session module.
registerPane({
  id: "session",
  title: () => "test session",
  component: lazy(() => Promise.resolve({ default: () => null })),
});

// Full capability set: the read-only pane must ignore all of it (no composer/
// controls), so the fixture is deliberately permissive - a read-only render
// that leaked a control would still have every capability enabled to leak.
const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// jsdom performs no real layout (offsetHeight is 0, no ResizeObserver), so
// @tanstack/react-virtual sees a 0px viewport and renders no rows - the same
// stub Session.test.tsx / the VirtualList suite use to exercise real rows.
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  registerDockviewApi(null); // never leak a fake dockview host to another test
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

test("shows a loading placeholder before the thread hydrates", async () => {
  const fake = connectFakeClient();
  const box: { resolve: ((r: ThreadReadResponse) => void) | null } = { resolve: null };
  fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolve = resolve)));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  expect(screen.getByText(/loading transcript/i)).toBeTruthy();
  for (let i = 0; i < 20 && box.resolve === null; i += 1) await Promise.resolve();
  box.resolve?.(readResponse("ref_a"));
  await waitFor(() => expect(screen.queryByText(/loading transcript/i)).toBeNull());
});

test('shows "no turns yet" for a thread with an empty transcript', async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText(/no turns yet/i)).toBeTruthy());
});

test("renders the thread's turns through the shared VirtualList/TurnBlock engine", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_1",
              turnId: "turn_1",
              type: "userMessage",
              text: "hi from the observed thread",
              status: "completed",
            },
          ],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByTestId("turn-block")).toBeTruthy());
  expect(screen.getByText("hi from the observed thread")).toBeTruthy();
});

test("is read-only: renders no composer and no session-chrome footer, even for a fully capable thread", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "read me", status: "completed" }],
        },
      ],
    }),
  );

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("read me")).toBeTruthy());
  // No composer input to type into...
  expect(screen.queryByRole("textbox")).toBeNull();
  // ...and no PaneScaffold footer (the session pane's only footer is its
  // SessionChrome; the read-only pane passes none).
  expect(screen.queryByTestId("pane-footer")).toBeNull();
});

test("falls back to the raw ref as the pane title when the thread has no name", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("ref_a")).toBeTruthy());
});

// --- kata 0pzz: "Back to parent" — a subagent transcript is a child of a
// specific parent session; the pane must say so and offer an explicit,
// durable way back, regardless of where dockview/StackHost happened to
// place it (a plain openPane() call on the parent ref works identically on
// every layout: refocuses an already-open parent tab, or reopens it fresh
// if the reader closed it - see workspace.ts's own same-params dedup). -----

test("with no parentRef, no back-to-parent action renders at all", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_a" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByText("ref_a")).toBeTruthy());
  expect(screen.queryByRole("button", { name: /back to/i })).toBeNull();
  // Stronger than the label check above: PaneScaffold only renders its
  // actions wrapper at all when passed a defined `actions` prop, so this
  // catches a mutation that renders BackToParentAction unconditionally with
  // an empty/placeholder ref (which would still produce a "Back to " button
  // whose label loosely matches /back to/i).
  expect(screen.queryByTestId("pane-actions")).toBeNull();
});

test("with a parentRef, shows a 'Back to <parent name>' action naming the live parent thread", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_child"));
  // Seed the parent's cached name the same way an already-open parent
  // session pane would have (ensureThread's own hydration) - this test
  // asserts the label reads it, not how it got there.
  threadsStore.setState((s) => {
    const threads = new Map(s.threads);
    threads.set("ref_parent", { ref: "ref_parent", name: "fix the flaky test" } as unknown as ThreadModel);
    return { ...s, threads };
  });

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_child", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByRole("button", { name: /back to fix the flaky test/i })).toBeTruthy());
});

test("with a parentRef but no cached name yet, falls back to the raw parent ref", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_child"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_child", parentRef: "ref_parent_unknown" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByRole("button", { name: /back to ref_parent_unknown/i })).toBeTruthy());
});

test("with a parentRef cached but its name is still the empty-string un-hydrated state, falls back to the raw ref (not a blank label)", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_child"));
  // A ThreadModel's `name` is a plain (non-optional) string that starts as ""
  // before the wire ever supplies one - the SAME un-hydrated state the "falls
  // back to the raw ref as the pane title" test above covers for the pane's
  // own title. The label must degrade the same way here, not render a blank
  // "Back to " button.
  threadsStore.setState((s) => {
    const threads = new Map(s.threads);
    threads.set("ref_parent_empty", { ref: "ref_parent_empty", name: "" } as unknown as ThreadModel);
    return { ...s, threads };
  });

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_child", parentRef: "ref_parent_empty" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  await waitFor(() => expect(screen.getByRole("button", { name: /back to ref_parent_empty/i })).toBeTruthy());
});

test("clicking 'Back to parent' focuses (or reopens) the parent session pane", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_child"));

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_child", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  const back = await waitFor(() => screen.getByRole("button", { name: /back to/i }));
  fireEvent.click(back);

  const panes = workspaceStore.getState().panes;
  const parentPane = panes.find((p) => p.type === "session");
  expect(parentPane?.params).toEqual({ ref: "ref_parent" });
  expect(workspaceStore.getState().focusedPaneId).toBe(parentPane?.id);
});

test("clicking 'Back to parent' re-focuses an ALREADY-OPEN parent pane rather than opening a duplicate", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_child"));

  const existingId = workspaceStore.getState().openPane("session", { ref: "ref_parent" });
  // Focus something else first, so clicking Back has to move focus back.
  workspaceStore.getState().openPane("transcript", { ref: "ref_child" });

  render(
    <ClientProvider client={fake}>
      <Transcript params={{ ref: "ref_child", parentRef: "ref_parent" }} paneId="p1" focused={false} />
    </ClientProvider>,
  );

  const back = await waitFor(() => screen.getByRole("button", { name: /back to/i }));
  fireEvent.click(back);

  const panes = workspaceStore.getState().panes;
  expect(panes.filter((p) => p.type === "session")).toHaveLength(1);
  expect(workspaceStore.getState().focusedPaneId).toBe(existingId);
});
