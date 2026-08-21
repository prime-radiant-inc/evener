import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { resetSubagentModuleStoreForTests, upsertSubagentRow, useSubagentRow } from "./subagentModuleStore";
import { WatchedChildIndicator } from "./watchedChild";

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

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return {
    thread: {
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
      source: "evener",
      evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      ...overrides,
    },
  };
}

async function flushUntil(done: () => boolean): Promise<void> {
  for (let i = 0; i < 20 && !done(); i += 1) await Promise.resolve();
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  resetSubagentModuleStoreForTests();
});

test("mounts one lean child watch", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  let params: unknown;
  fake.on("thread/read", (value) => {
    params = value;
    return readResponse("child_ref");
  });

  render(<WatchedChildIndicator ref="child_ref" scopeKey="turn_1" rowKey="dlg:child_ref" />);
  await flushUntil(() => params !== undefined);

  expect(params).toEqual({
    ref: "child_ref",
    includeTurns: false,
    itemsView: "full",
    subscribe: true,
    replaceSubscription: false,
    turnLimit: 40,
  });
});

test("releases the child watch on unmount", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => readResponse("child_ref"));

  const { unmount } = render(<WatchedChildIndicator ref="child_ref" scopeKey="turn_1" rowKey="dlg:child_ref" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref"));
  unmount();

  expect(threadsStore.getState().watchedThreads.has("child_ref")).toBe(false);
});

test("writes changed child status back to the delegate row", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => readResponse("child_ref"));
  upsertSubagentRow("turn_1", { rowKey: "dlg:child_ref", kind: "running", resultPreview: "" });

  function LiveKind() {
    return <span data-testid="live-kind">{useSubagentRow("turn_1", "dlg:child_ref")?.liveKind}</span>;
  }

  render(
    <>
      <WatchedChildIndicator ref="child_ref" scopeKey="turn_1" rowKey="dlg:child_ref" />
      <LiveKind />
    </>,
  );
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref"));
  await act(async () => {});
  expect(screen.getByTestId("live-kind").textContent).toBe("running");

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_child_ref", ref: "child_ref", status: { type: "systemError" } },
    } as never);
  });
  expect(screen.getByTestId("live-kind").textContent).toBe("failed");
});
