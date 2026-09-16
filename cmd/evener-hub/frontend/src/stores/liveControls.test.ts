import type { Thread } from "@evener/appwire-client";
import { NO_ACTIVE_TURN, STEER_UNAVAILABLE } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { controlsFor, pressRefusal } from "./liveControls";
import { resetThreadsStoreForTests, threadsStore } from "./threads";

const CAPABILITIES = { send: true, steer: true, interrupt: true, queue: true } as Thread["evener"]["capabilities"];

function thread(status: string, steer = true): Thread {
  return {
    id: "thr_ref_a",
    sessionId: "sess_ref_a",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: status },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: { ...CAPABILITIES, steer },
      queue: { revision: 0, depth: 1 },
    },
    turns: [],
  } as Thread;
}

beforeEach(() => {
  resetThreadsStoreForTests();
});
afterEach(() => {
  resetThreadsStoreForTests();
});

async function hydrate(status: string, steer = true): Promise<FakeClient> {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => ({ thread: thread(status, steer) }));
  await threadsStore.getState().ensureThread("ref_a");
  return fake;
}

// The press reads the store as it is at the press, so a status frame folded
// after the render that offered the control decides the press.
test("pressRefusal follows the store's live status, not the render's", async () => {
  const fake = await hydrate("active");
  expect(pressRefusal("ref_a", "steer")).toBeUndefined();
  expect(pressRefusal("ref_a", "stop")).toBeUndefined();
  fake.emitNotification({
    method: "thread/status/changed",
    params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
  });
  expect(pressRefusal("ref_a", "steer")).toBe(NO_ACTIVE_TURN);
  expect(pressRefusal("ref_a", "stop")).toBe(NO_ACTIVE_TURN);
  expect(pressRefusal("ref_a", "send")).toBeUndefined();
});

test("pressRefusal carries the control's own reason", async () => {
  await hydrate("active", false);
  expect(pressRefusal("ref_a", "steer")).toBe(STEER_UNAVAILABLE);
});

test("a session the store no longer holds has no turn to act on", () => {
  expect(pressRefusal("ref_gone", "steer")).toBe(NO_ACTIVE_TURN);
});

test("controlsFor is sessionControls over the model's status, capabilities and queue depth", async () => {
  await hydrate("idle");
  const model = threadsStore.getState().threads.get("ref_a")!;
  const controls = controlsFor(model);
  expect(controls.send).toBe(true);
  expect(controls.steer).toBe(false);
  // Idle with a parked queue drains.
  expect(controls.drain).toBe(true);
});
