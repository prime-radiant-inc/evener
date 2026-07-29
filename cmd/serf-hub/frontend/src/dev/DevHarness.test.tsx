import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { resetThreadsStoreForTests } from "../stores/threads";
import { DevHarness } from "./DevHarness";

// Mirrors the fixture helpers in ../stores/threads.test.ts (duplicated
// rather than imported: the two test files share no test-utils module).
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

// flushUntil drains microtask turns until `done()` reports true (or a
// bounded number of turns elapse, so a genuine hang fails fast instead of
// silently). Same contract/name as protocol/client.test.ts's and
// stores/threads.test.ts's own copies; duplicated because these test files
// share no test-utils module.
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: `preview for ${ref}`,
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

// connectFakeClient wires a fresh FakeClient through useConnectionStore's
// locked connect(client) entry point, exactly like DevHarness's own
// bootstrap effect will find it already wired and skip creating a real
// AppwireClient (see DevHarness.tsx: bootstrapClient's no-op guard).
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

describe("DevHarness", () => {
  // Go's encoding/json renders a nil slice with no `omitempty` as JSON null,
  // not `[]` — app_threadlist.go's `var threads []appwire.Thread` is exactly
  // that: nil until something is appended, so a live hub with zero matching
  // threads genuinely sends {"data":null}, not {"data":[]}. The legacy
  // client already guards this (assets/appwire.js: `resp.data || []`); this
  // component must too, or a real "no threads yet" hub response crashes the
  // render (`null.map is not a function`) instead of showing an empty list.
  test("renders an empty list without crashing when thread/list responds with data: null", async () => {
    connectFakeClient();
    const fake = connectionStore.getState().client as FakeClient;
    fake.on("thread/list", () => ({ data: null }) as unknown as { data: Thread[] });

    render(<DevHarness />);
    // Wait for the thread/list round trip (and the render it triggers) to
    // actually settle before asserting — a render crash happens on THIS
    // render, not the component's first (empty-state) one.
    await act(async () => {
      await flushUntil(() => fake.calls.some((c) => c.method === "thread/list"));
    });

    expect(screen.getByText(/connection:\s*ready/i)).toBeTruthy();
    expect(screen.queryAllByRole("button")).toHaveLength(0);
  });

  test("shows the connection state and lists threads from a scripted thread/list", async () => {
    connectFakeClient();
    const fake = connectionStore.getState().client as FakeClient;
    fake.on("thread/list", () => ({ data: [testThread("ref_a"), testThread("ref_b")] }));

    render(<DevHarness />);

    expect(screen.getByText(/connection:\s*ready/i)).toBeTruthy();
    expect(await screen.findByRole("button", { name: /ref_a/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /ref_b/ })).toBeTruthy();
  });

  test("clicking a thread row calls ensureThread and shows the model JSON", async () => {
    connectFakeClient();
    const fake = connectionStore.getState().client as FakeClient;
    fake.on("thread/list", () => ({ data: [testThread("ref_a")] }));
    fake.on("thread/read", () => ({ thread: testThread("ref_a") }) satisfies ThreadReadResponse);

    const user = userEvent.setup();
    render(<DevHarness />);
    const row = await screen.findByRole("button", { name: /ref_a/ });
    await user.click(row);

    expect(await screen.findByText(/"threadId":\s*"thr_ref_a"/)).toBeTruthy();
    expect(fake.calls.some((c) => c.method === "thread/read")).toBe(true);
  });

  test("an injected item/agentMessage/delta updates the live-updating JSON view", async () => {
    connectFakeClient();
    const fake = connectionStore.getState().client as FakeClient;
    fake.on("thread/list", () => ({ data: [testThread("ref_a")] }));
    fake.on(
      "thread/read",
      () =>
        ({
          thread: testThread("ref_a", {
            turns: [
              {
                id: "turn_1",
                status: "inProgress",
                itemsView: "full",
                items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" }],
              },
            ],
            serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
          }),
        }) satisfies ThreadReadResponse,
    );

    const user = userEvent.setup();
    render(<DevHarness />);
    const row = await screen.findByRole("button", { name: /ref_a/ });
    await user.click(row);
    await screen.findByText(/"id":\s*"item_1"/);

    act(() => {
      fake.emitNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", itemId: "item_1", delta: "hello websockets" },
      });
    });

    expect(screen.getByText(/"pendingText"/)).toBeTruthy();
    expect(screen.getByText(/hello websockets/)).toBeTruthy();
  });
});
