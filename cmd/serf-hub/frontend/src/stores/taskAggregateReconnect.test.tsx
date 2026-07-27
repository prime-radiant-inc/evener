import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import { FakeClient } from "../protocol/testing/fakeClient";
import { connectionStore } from "./connection";
import { resetThreadsStoreForTests, threadsStore, useThreadsStore } from "./threads";
import { TasksPanel } from "../panes/session/chrome/TasksPanel";

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

function readResponse(ref: string, tasks: { total: number; done: number }): ThreadReadResponse {
  const thread: Thread = {
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
    serf: { ref, capabilities: CAPABILITIES, queue: {}, tasks },
  };
  return { thread };
}

function TaskBadgeFromStore({ ref }: { ref: string }) {
  const model = useThreadsStore((state) => state.threads.get(ref));
  return model ? <TasksPanel sessionRef={ref} model={model} /> : null;
}

async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(cleanup);

test("store reconnect preserves task aggregate and Tasks badge across notification and fresh thread/read", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  let readCount = 0;
  fake.on("thread/read", (params) => {
    readCount += 1;
    const ref = (params as { ref: string }).ref;
    return readResponse(ref, readCount === 1 ? { total: 2, done: 1 } : { total: 2, done: 2 });
  });

  await threadsStore.getState().ensureThread("ref_tasks");
  render(<TaskBadgeFromStore ref="ref_tasks" />);
  expect(screen.getByRole("button", { name: "Tasks 1/2" })).toBeTruthy();

  act(() => {
    fake.emitNotification({
      method: "serf/task/updated",
      params: { threadId: "thr_ref_tasks", ref: "ref_tasks", total: 2, done: 2 },
    });
  });
  expect(threadsStore.getState().threads.get("ref_tasks")?.tasks).toEqual({ total: 2, done: 2 });
  expect(screen.getByRole("button", { name: "Tasks 2/2" })).toBeTruthy();

  await act(async () => {
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(
      () =>
        fake.calls.filter((call) => call.method === "thread/read").length === 2 &&
        threadsStore.getState().threads.get("ref_tasks")?.tasks?.done === 2,
    );
  });

  expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
  expect(threadsStore.getState().threads.get("ref_tasks")?.tasks).toEqual({ total: 2, done: 2 });
  expect(screen.getByRole("button", { name: "Tasks 2/2" })).toBeTruthy();
});
