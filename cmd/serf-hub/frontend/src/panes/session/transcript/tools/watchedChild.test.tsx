import { afterEach, beforeEach, test, expect } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { WatchedChildIndicator } from "./watchedChild";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";

// WatchedChildIndicator is the subagent module's live view into a running
// child (subagentModule.tsx wires it into a "running" row that has a
// transcriptRef): watchThread(ref)/releaseWatchedThread(ref) on mount/
// unmount (stores/threads.ts's own sanctioned addition), rendering Cadence
// off the watched model's own frameTimes once hydrated. No ClientProvider
// needed in these tests - watchThread rides connectionStore directly
// (stores/threads.ts's requireClient()), same as Session.test.tsx's own
// ensureThread-driven tests never need one either.

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
    status: { type: "active" },
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

async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("calls watchThread with the given ref on mount, using the leaner includeTurns:false read", async () => {
  const fake = connectFakeClient();
  let sawParams: unknown;
  fake.on("thread/read", (params) => {
    sawParams = params;
    return readResponse("child_ref_1");
  });

  render(<WatchedChildIndicator ref="child_ref_1" />);
  await flushUntil(() => sawParams !== undefined);

  expect(sawParams).toEqual({
    ref: "child_ref_1",
    includeTurns: false,
    itemsView: "full",
    subscribe: true,
    replaceSubscription: false,
    turnLimit: 40,
  });
});

test("renders nothing before the watched model has hydrated", () => {
  connectFakeClient().on("thread/read", () => new Promise(() => {})); // never resolves
  const { container } = render(<WatchedChildIndicator ref="child_ref_2" />);
  expect(container.textContent).toBe("");
});

test("renders a Cadence indicator once the watched model hydrates", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_ref_3"));

  render(<WatchedChildIndicator ref="child_ref_3" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref_3"));
  await act(async () => {});

  expect(screen.getByTestId("cadence-dot")).toBeTruthy();
});

test("releaseWatchedThread fires on unmount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_ref_4"));

  const { unmount } = render(<WatchedChildIndicator ref="child_ref_4" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref_4"));

  unmount();
  expect(threadsStore.getState().watchedThreads.has("child_ref_4")).toBe(false);
});

test("a live notification updates the rendered Cadence's underlying frameTimes without a remount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_ref_5"));

  render(<WatchedChildIndicator ref="child_ref_5" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref_5"));

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_child_ref_5", ref: "child_ref_5", status: { type: "active" } },
    } as never);
  });

  expect(threadsStore.getState().watchedFrameTimes.get("child_ref_5")?.length).toBe(1);
});
