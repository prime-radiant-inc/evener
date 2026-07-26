import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import {
  resetSubagentModuleStoreForTests,
  type SubagentRowKind,
  upsertSubagentRow,
  useSubagentRows,
} from "./subagentModuleStore";
import { WatchedChildIndicator } from "./watchedChild";

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
  resetSubagentModuleStoreForTests();
});

// A tiny reactive probe: reads the row's watch-written liveKind overlay out
// of subagentModuleStore so a test can assert the effect-guarded write-back
// (yd16) landed, without reaching for a non-hook store accessor.
function LiveKindProbe({ turnId, rowKey }: { turnId: string; rowKey: string }) {
  const rows = useSubagentRows(turnId);
  const liveKind: SubagentRowKind | "none" = rows.find((r) => r.rowKey === rowKey)?.liveKind ?? "none";
  return <span data-testid="live-kind">{liveKind}</span>;
}

test("calls watchThread with the given ref on mount, using the leaner includeTurns:false read", async () => {
  const fake = connectFakeClient();
  let sawParams: unknown;
  fake.on("thread/read", (params) => {
    sawParams = params;
    return readResponse("child_ref_1");
  });

  render(<WatchedChildIndicator ref="child_ref_1" scopeKey="turn_1" rowKey="dlg:child_ref_1" />);
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
  const { container } = render(<WatchedChildIndicator ref="child_ref_2" scopeKey="turn_1" rowKey="dlg:child_ref_2" />);
  expect(container.textContent).toBe("");
});

test("renders a Cadence indicator once the watched model hydrates", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_ref_3"));

  render(<WatchedChildIndicator ref="child_ref_3" scopeKey="turn_1" rowKey="dlg:child_ref_3" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref_3"));
  await act(async () => {});

  expect(screen.getByTestId("cadence-dot")).toBeTruthy();
});

test("releaseWatchedThread fires on unmount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_ref_4"));

  const { unmount } = render(<WatchedChildIndicator ref="child_ref_4" scopeKey="turn_1" rowKey="dlg:child_ref_4" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref_4"));

  unmount();
  expect(threadsStore.getState().watchedThreads.has("child_ref_4")).toBe(false);
});

test("a live notification updates the rendered Cadence's underlying frameTimes without a remount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_ref_5"));

  render(<WatchedChildIndicator ref="child_ref_5" scopeKey="turn_1" rowKey="dlg:child_ref_5" />);
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_ref_5"));

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_child_ref_5", ref: "child_ref_5", status: { type: "active" } },
    } as never);
  });

  expect(threadsStore.getState().watchedFrameTimes.get("child_ref_5")?.length).toBe(1);
});

// yd16: the pill froze at the settled tool-output kind. WatchedChildIndicator
// now writes the LIVE child status back onto the row as a `liveKind` overlay
// (effect-guarded, keyed on the derived kind - never a render-time store
// write), mapping the WIRE thread-status vocabulary via rowKindFromChildStatus
// (which reuses cadenceStateForStatus, NOT classifyJobStatus).

test("writes back liveKind 'failed' when the watched child reports a systemError status", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_wb_1", { status: { type: "systemError" } }));
  upsertSubagentRow("turn_wb", { rowKey: "dlg:child_wb_1", kind: "running", task: "t", resultPreview: "" });

  render(
    <>
      <WatchedChildIndicator ref="child_wb_1" scopeKey="turn_wb" rowKey="dlg:child_wb_1" />
      <LiveKindProbe turnId="turn_wb" rowKey="dlg:child_wb_1" />
    </>,
  );
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_wb_1"));
  await act(async () => {});

  expect(screen.getByTestId("live-kind").textContent).toBe("failed");
});

test("writes back liveKind 'done' when the watched child status is closed (wire vocabulary, not classifyJobStatus)", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_wb_2", { status: { type: "closed" } }));
  upsertSubagentRow("turn_wb", { rowKey: "dlg:child_wb_2", kind: "running", task: "t", resultPreview: "" });

  render(
    <>
      <WatchedChildIndicator ref="child_wb_2" scopeKey="turn_wb" rowKey="dlg:child_wb_2" />
      <LiveKindProbe turnId="turn_wb" rowKey="dlg:child_wb_2" />
    </>,
  );
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_wb_2"));
  await act(async () => {});

  expect(screen.getByTestId("live-kind").textContent).toBe("done");
});

test("a live status/changed notification updates the written-back liveKind without a remount", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_wb_3", { status: { type: "active" } }));
  upsertSubagentRow("turn_wb", { rowKey: "dlg:child_wb_3", kind: "running", task: "t", resultPreview: "" });

  render(
    <>
      <WatchedChildIndicator ref="child_wb_3" scopeKey="turn_wb" rowKey="dlg:child_wb_3" />
      <LiveKindProbe turnId="turn_wb" rowKey="dlg:child_wb_3" />
    </>,
  );
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_wb_3"));
  await act(async () => {});
  expect(screen.getByTestId("live-kind").textContent).toBe("running"); // active -> running

  await act(async () => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_child_wb_3", ref: "child_wb_3", status: { type: "systemError" } },
    } as never);
  });

  expect(screen.getByTestId("live-kind").textContent).toBe("failed");
});

// g5kf: the honest-clock bug. Once a watched child leaves the daemon's live
// roster (notLoaded - evicted, orphaned, or lost to a hub restart), the row
// must demote to "unknown", never stay (or resurrect back to) "running" -
// the only other liveness signal, the delegate's own frozen tool output, is
// itself frozen at "running" forever whenever a foreground_timeout fired
// (agent/job_delegate.go's mainline path for any non-trivial delegate).
test("g5kf: writes back liveKind 'unknown', never 'running', when the watched child reports notLoaded", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("child_wb_4", { status: { type: "notLoaded" } }));
  upsertSubagentRow("turn_wb", { rowKey: "dlg:child_wb_4", kind: "running", task: "t", resultPreview: "" });

  render(
    <>
      <WatchedChildIndicator ref="child_wb_4" scopeKey="turn_wb" rowKey="dlg:child_wb_4" />
      <LiveKindProbe turnId="turn_wb" rowKey="dlg:child_wb_4" />
    </>,
  );
  await flushUntil(() => threadsStore.getState().watchedThreads.has("child_wb_4"));
  await act(async () => {});

  expect(screen.getByTestId("live-kind").textContent).toBe("unknown");
});

// g5kf: watchThread's own rejection (thread/read failing - hub unreachable,
// the child ref no longer resolves, etc.) is caught with a bare
// `.catch(() => {})` in this file - deliberately best-effort (a live-status
// nicety must never crash the whole subagent module), but that swallow was
// itself untested, so nothing proved it doesn't also leave the ref's
// refcount/tracking state stuck. watchThread's own catch already undoes its
// claim via releaseWatchedThread on a failed hydrate (stores/threads.ts) -
// this asserts that from the caller's side: no crash, and no stale entry left
// behind in watchedThreads for a ref whose hydrate never actually succeeded.
test("a rejected watchThread (thread/read failing) is swallowed silently - no crash, no stale watchedThreads entry", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => {
    throw new Error("boom: hub unreachable");
  });

  const { container } = render(
    <WatchedChildIndicator ref="child_reject_1" scopeKey="turn_1" rowKey="dlg:child_reject_1" />,
  );
  await flushUntil(() => false, 20); // no settle condition to poll for - just drain pending microtasks
  await act(async () => {});

  expect(container.textContent).toBe("");
  expect(threadsStore.getState().watchedThreads.has("child_reject_1")).toBe(false);
});
