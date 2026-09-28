import type { Thread, ThreadCapabilities } from "@evener/appwire-client";
import { NO_ACTIVE_TURN, STEER_UNAVAILABLE } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { connectionStore } from "./connection";
import { controlsFor, pressLocalRecoveryFenced, pressRefusal, recoveryFence } from "./liveControls";
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
  // Installs (via this module's own load) the pending-turns projection the
  // store-wide resume-only predicate reads, and clears any rows a prior test
  // left, so the press reads a real, empty projection rather than the
  // fail-closed default.
  resetPendingTurnsStoreForTests();
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

// The resume-only carve-out as the predicate reads it: only the hub's exact
// resume-fenced shape (local, notLoaded, resumeRequired, send exactly false) is
// the foldable send. A snapshot that never advertised send is NOT that shape -
// the trigger `send !== true` treated a missing capability as the fence, which
// the reviewer's Low finding flagged.
test("isResumeOnlyLocal requires send === false, not merely missing", () => {
  const base = { resumeRequired: true, status: { type: "notLoaded" } } as const;
  expect(recoveryFence("local:s", { ...base, capabilities: {} as ThreadCapabilities }, true).resumeOnly).toBe(false);
  expect(
    recoveryFence("local:s", { ...base, capabilities: { send: false } as ThreadCapabilities }, true).resumeOnly,
  ).toBe(true);
});

// Certainty: the carve-out spans only the cold exited status and excludes a
// stopped snapshot whose outbox still holds delivery-uncertain rows, which the
// hub's explicit Resume reconciles. A closed/ended status is not this shape.
test("isResumeOnlyLocal is the notLoaded shape with no uncertain messages", () => {
  const capabilities = { send: false } as ThreadCapabilities;
  expect(
    recoveryFence("local:s", { resumeRequired: true, status: { type: "closed" }, capabilities }, true).resumeOnly,
  ).toBe(false);
  expect(
    recoveryFence("local:s", { resumeRequired: true, status: { type: "notLoaded" }, capabilities }, true).resumeOnly,
  ).toBe(true);
  expect(
    recoveryFence("local:s", { resumeRequired: true, status: { type: "notLoaded" }, capabilities }, true, {
      uncertainMessages: true,
    }).resumeOnly,
  ).toBe(false);
  // A Force stop this page started is still draining: the hub refuses even
  // turn/start for the window, so the shape is not foldable either.
  expect(
    recoveryFence("local:s", { resumeRequired: true, status: { type: "notLoaded" }, capabilities }, true, {
      stopInFlight: true,
    }).resumeOnly,
  ).toBe(false);
  // A fenced-but-not-resume-only shape is still fenced, and still reached by
  // the stopped-local card.
  const uncertain = recoveryFence(
    "local:s",
    { resumeRequired: true, status: { type: "notLoaded" }, capabilities },
    true,
    { uncertainMessages: true },
  );
  expect(uncertain.stillFenced).toBe(true);
  expect(uncertain.fencedLocal).toBe(true);
});

// The press-time fence is method-aware: turn/start is the one method the hub
// admits for a merely-resumable session, so a Send press is not refused for
// that shape; every other action and every other session keeps the fence.
test("the press fence exempts turn/start only for a merely-resumable local session", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  const ref = "local:resume-only";
  fake.on("thread/read", () => ({
    thread: {
      ...thread("notLoaded"),
      id: ref,
      sessionId: ref,
      evener: {
        ref,
        mutationStateAuthoritative: false,
        resumeRequired: true,
        capabilities: { ...CAPABILITIES, send: false },
        queue: { revision: 0, depth: 0 },
      },
    } as Thread,
  }));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  // Send is offered, so its press-time reading agrees and does not refuse.
  expect(pressRefusal(ref, "send")).toBeUndefined();
  // turn/start alone is exempt from the press fence; the bare call - every
  // other action and state - still reads the fence.
  expect(pressLocalRecoveryFenced(ref, "turn/start")).toBe(false);
  expect(pressLocalRecoveryFenced(ref)).toBe(true);
});
