import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { resetThreadsStoreForTests } from "../../stores/threads";
import Transcript from "./Transcript";

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
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
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
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
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
