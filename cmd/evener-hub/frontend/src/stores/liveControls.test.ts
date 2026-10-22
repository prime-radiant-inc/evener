import type { Thread } from "@evener/appwire-client";
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

// The hub bit is the authority. The client offers Send from the hub's own
// admission answer, never from the resumeRequired/send-false/cold-status shape
// it used to infer: applyThreadResumeRequirement stamps that same shape for a
// Stop drain, an unconfirmed force-stop exit, and the connection-recovery
// fence, whose turn/start the hub still refuses.
test("isResumeOnly follows the hub's foldable bit, never the inferred shape", () => {
  const base = { status: { type: "notLoaded" } } as const;
  // resumeRequired set (the hub's fence overlay) but the hub did not stamp the
  // foldable bit: a Stop drain / unconfirmed exit / connection fence, not this.
  const fencedButNotFoldable = { resumeRequired: true, status: { type: "notLoaded" } };
  expect(recoveryFence(fencedButNotFoldable, true).resumeOnly).toBe(false);
  expect(recoveryFence({ ...base, resumeOnlyFoldable: true }, true).resumeOnly).toBe(true);
});

// The client's own signals still narrow the FOLD. Each one moves the session
// under the Send-resumes face instead: Send stays offered and the store's
// driver resumes behind the parked rows, waiting out a Stop this page started.
test("the client signals take a fence off the fold and leave Send as the action", () => {
  const foldable = { resumeOnlyFoldable: true, status: { type: "notLoaded" } } as const;
  expect(recoveryFence(foldable, true).resumeOnly).toBe(true);
  expect(recoveryFence(foldable, true, { uncertainMessages: true }).resumeOnly).toBe(false);
  expect(recoveryFence(foldable, true, { queuedNonSend: true }).resumeOnly).toBe(false);
  expect(recoveryFence(foldable, true, { stopInFlight: true }).resumeOnly).toBe(false);
  // A delivery-uncertain or queued-non-send row is the Send-resumes face: Send
  // is offered and not stillFenced, and the store's driver resumes behind the
  // parked rows.
  const uncertain = recoveryFence(foldable, true, { uncertainMessages: true });
  expect(uncertain.sendResumes).toBe(true);
  expect(uncertain.stillFenced).toBe(false);
  const queued = recoveryFence(foldable, true, { queuedNonSend: true });
  expect(queued.sendResumes).toBe(true);
  expect(queued.stillFenced).toBe(false);
  // A Stop this page started no longer refuses Send: the press parks its row
  // and the driver waits the drain out before resuming.
  const stopped = recoveryFence(foldable, true, { stopInFlight: true });
  expect(stopped.sendResumes).toBe(true);
  expect(stopped.stillFenced).toBe(false);
});

// RoboRev Medium (round 13): the hub's resume-only admission is
// status-independent (sessionActionRecoveryError / resumeOnlyFoldable read no
// status), so a live snapshot the hub legitimately stamped foldable - a daemon
// another controller started under a confirmed force-stop obligation - keeps
// the carve-out and still folds. The stale bit this predicate must not honor is
// defended against where it is written: the off-shut-down transition clears it
// (settleResumeOnlyOffShutdown) and a connection-generation change invalidates
// it (invalidateHeldHistoriesForReconnect), so a bit whose shut-down snapshot
// has ended can no longer mis-route a send. The predicate follows the bit
// alone, never a status.
test("isResumeOnly follows the hub's foldable bit, not the status", () => {
  // A live snapshot the hub legitimately stamped foldable keeps the carve-out:
  // the hub admits the folded send there too.
  const foldableActive = { resumeOnlyFoldable: true, status: { type: "active" } } as const;
  expect(recoveryFence(foldableActive, false).resumeOnly).toBe(true);
  // A bit the off-shut-down transition (or a reconnect) cleared is not this
  // shape, whatever the status - the property the stale-bit defence protects.
  const clearedActive = { resumeOnlyFoldable: false, status: { type: "active" } } as const;
  expect(recoveryFence(clearedActive, false).resumeOnly).toBe(false);
  const clearedStopped = { resumeOnlyFoldable: false, status: { type: "notLoaded" } } as const;
  expect(recoveryFence(clearedStopped, false).resumeOnly).toBe(false);
});

// A Stop this page started is its OWN fence, not only a blocker of the
// resume-only carve-out: a stale thread refresh can clear
// restartBlockingObligations while the forceStop RPC still drains. The stop
// therefore keeps the recovery obligation, and Send is the action that rides
// it out.
test("an in-flight Stop arms the fence without the restart obligation", () => {
  const foldable = { resumeOnlyFoldable: true, status: { type: "notLoaded" } } as const;
  const reading = recoveryFence(foldable, false, { stopInFlight: true });
  expect(reading.resumeOnly).toBe(false);
  expect(reading.stillFenced).toBe(true);
  expect(reading.fencedLocal).toBe(true);
});

// The press fence is method-agnostic, and deliberately so: none of its callers
// presses the send (the composer's submit runs decideSubmitRoute over
// availabilityFor, which is where the resume-only carve-out lives - asserted
// through pressRefusal below), and every verb it does guard - steer, the
// queue-strip actions, the recovery-fenced built-ins - is one the hub refuses
// for the obligation's whole window. So a merely-resumable session keeps the
// fence here.
test("the press fence keeps a merely-resumable local session fenced", async () => {
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
        resumeOnlyFoldable: true,
        capabilities: { ...CAPABILITIES, send: false },
        queue: { revision: 0, depth: 0 },
      },
    } as Thread,
  }));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  // Send is offered, so its own press-time reading agrees and does not refuse.
  expect(pressRefusal(ref, "send")).toBeUndefined();
  expect(pressLocalRecoveryFenced(ref)).toBe(true);
});

// R09 widens pressRefusal's send carve-out from the merely-resumable fold to the
// whole Send-resumes face: an uncertain/queued/unconfirmed shape whose press
// must agree with the offered Send, while every other control keeps its reason.
test("pressRefusal's send carve-out covers the Send-resumes face", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  const ref = "local:send-resumes-face";
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
        queue: { revision: 0 },
      },
    } as Thread,
  }));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  // Send is offered, so its press-time reading agrees instead of refusing.
  expect(pressRefusal(ref, "send")).toBeUndefined();
  // Every other control still refuses: only turn/start is carved out.
  expect(pressRefusal(ref, "steer")).toBeDefined();
  expect(pressRefusal(ref, "queue")).toBeDefined();
});

// RoboRev Medium (this round): the press-time fence reads the store's own
// in-flight Stop (stoppingRefs) as its OWN clause, never only the restart
// obligation. A stale thread refresh can clear restartBlockingObligations while
// a local forceStop is still draining - the hub holds Stopping > 0 and refuses
// every verb this helper guards for exactly that window - so the press fence
// must hold on stoppingRefs alone. turn/interrupt is deliberately NOT routed
// through this helper (the composer's Stop press reads pressRefusal(ref,
// "stop") directly), so the drain window never blocks the interrupt that ends
// it.
test("pressLocalRecoveryFenced keeps its fence while a Stop drains, with no obligation", () => {
  const ref = "local:draining";
  // The obligation is already clear; only the in-flight Stop is left.
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  expect(pressLocalRecoveryFenced(ref)).toBe(false);
  threadsStore.setState((s) => ({ stoppingRefs: new Set(s.stoppingRefs).add(ref) }));
  expect(pressLocalRecoveryFenced(ref)).toBe(true);
  // The Stop settles: the RPC resolved and cleared stoppingRefs.
  threadsStore.setState((s) => {
    const stoppingRefs = new Set(s.stoppingRefs);
    stoppingRefs.delete(ref);
    return { stoppingRefs };
  });
  expect(pressLocalRecoveryFenced(ref)).toBe(false);
});
