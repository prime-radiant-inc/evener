// The composer's three controls are derived from two things that used to
// refresh on different clocks: the thread's STATUS, which every live frame
// updates, and its CAPABILITIES, which only a thread/read ever set.
//
//   showStop  = busy && capabilities.interrupt
//   showSteer = busy && capabilities.steer
//   Send      = ... && deriveSendQueueAvailability(status, capabilities)
//
// Send is the hub capability defined by whether a turn is in flight
// (server/appwire_runtime.go's appCapabilities: Send is !active), while Steer,
// Interrupt and Queue advertise harness support and the composer applies the
// status itself. A snapshot cut before the turn therefore says send=true about
// a session that is running by the time the status frame lands. Reading the
// stale set back once
// the status has moved on produced kata 06t8's report exactly: submit a reply,
// and the session it KNOWS is running shows no Steer, no Stop, and a Send that
// stays grey however much you type — until a reload re-reads the snapshot from
// the now-active daemon.
//
// thread/status/changed carries the matching set now, so this file drives the
// real frame sequence a resumed session produces and asserts the controls
// follow it. Absent capabilities still mean "no update" for a source that
// omits the capability, which is its own case below.
//
// The close frame is the same defect at the other end of a session's life
// (kata pk2d) and the last cases here are its own: a daemon cannot describe
// what the thread it is leaving can still be asked to do, so the HUB stamps
// that frame on the way past (cmd/evener-hub/app_relay.go's
// stampClosedThreadCapabilities). Without it a session that shut down mid-turn
// keeps send=false, and an ended composer is a follow-up card gated on exactly
// that bit — so the whole composer disappears.

import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore } from "../../../stores/connection";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import "../testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { Composer as ComposerView } from "./Composer";

function Composer(props: React.ComponentProps<typeof ComposerView>) {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("Composer capability test rendered without a connected client");
  return (
    <ClientProvider client={client}>
      <ComposerView {...props} />
    </ClientProvider>
  );
}

import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { resetStoplessComposerSightingsForTests, stoplessComposerSightings } from "./stoplessComposer";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

const REF = "ref_a";

// What the hub advertises for a COLD exited session, verbatim from
// cmd/evener-hub/app_threadread.go's pastEntryThread: it can be sent to (the
// send resumes it), and Steer/Interrupt/Queue are false because a session
// with no daemon has no turn to act on.
const COLD_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: false,
  interrupt: false,
  compact: true,
  clear: false,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: false,
  goal: true,
  sharedNotes: true,
  rename: true,
};

// What a LIVE daemon with every callback wired advertises, verbatim from
// server/appwire_runtime.go's appCapabilities. `active` moves Send alone;
// Steer, Interrupt and Queue are harness support and do not move with it. The
// difference is still why a snapshot cannot be reused across a status change.
function daemonCapabilities(active: boolean): ThreadCapabilities {
  return {
    send: !active,
    steer: true,
    interrupt: true,
    compact: true,
    clear: false,
    forkFromTurn: false,
    shutdown: true,
    changeModel: true,
    changeVisionModel: true,
    queue: true,
    goal: true,
    sharedNotes: true,
    rename: true,
  };
}

function thread(status: string, capabilities: ThreadCapabilities): Thread {
  return {
    id: `thr_${REF}`,
    sessionId: `sess_${REF}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: status },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref: REF, capabilities, queue: { revision: 0 } },
  };
}

async function mountComposer(status: string, capabilities: ThreadCapabilities): Promise<FakeClient> {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => ({ thread: thread(status, capabilities) }) as ThreadReadResponse);
  await threadsStore.getState().ensureThread(REF);
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref={REF} focused={false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery(REF);
  return fake;
}

// The frames a turn opening produces, in the projector's own order
// (internal/appprojector/appwire_projection.go, EventUserInput): the turn, the
// user's message, then the status — with the capability set the daemon stamps
// on it at its notification egress. `capabilities: undefined` is the same
// sequence from a source that state-gates nothing.
function turnStartedFrame(turnId: string): AnyNotification {
  return {
    method: "turn/started",
    params: {
      threadId: `thr_${REF}`,
      ref: REF,
      turn: { id: turnId, status: "inProgress", itemsView: "full", startedAt: 5000 },
    },
  };
}

function turnCompletedFrame(turnId: string): AnyNotification {
  return {
    method: "turn/completed",
    params: { threadId: `thr_${REF}`, ref: REF, turn: { id: turnId, status: "completed", itemsView: "" } },
  };
}

function statusActiveFrame(capabilities?: ThreadCapabilities): AnyNotification {
  return {
    method: "thread/status/changed",
    params: { threadId: `thr_${REF}`, ref: REF, status: { type: "active" }, capabilities },
  };
}

function emitTurnStart(fake: FakeClient, turnId: string, capabilities?: ThreadCapabilities): void {
  act(() => {
    fake.emitNotification(turnStartedFrame(turnId));
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        turnId,
        item: { type: "userMessage", id: "item_user_1", turnId, text: "hi", status: "completed" },
      },
    });
    fake.emitNotification(statusActiveFrame(capabilities));
  });
}

function submitButton(): HTMLButtonElement {
  return screen.getByTestId("composer-submit") as HTMLButtonElement;
}

async function type(text: string): Promise<void> {
  await userEvent.type(screen.getByRole("textbox", { name: /^message$/i }), text);
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  // Every test here calls ensureThread(ref) directly for setup - Composer
  // takes its ref as a prop and never calls ensureThread/releaseThread
  // itself, so cleanup()'s unmount leaves that ref refcounted after the LAST
  // test. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
});

// Kata 06t8's report, end to end: a cold exited session opened from the rail,
// a follow-up sent into it, and the daemon the hub relaunches to answer it.
// Every control was gone until a reload — and the model knew the whole time
// that a turn was running.
test("a resumed cold session's controls follow the turn it is running", async () => {
  const fake = await mountComposer("notLoaded", COLD_CAPABILITIES);

  emitTurnStart(fake, "turn_5", daemonCapabilities(true));
  await type("hi");

  const model = threadsStore.getState().threads.get(REF);
  expect({ status: model?.status.type, activeTurnId: model?.activeTurnId }).toEqual({
    status: "active",
    activeTurnId: "turn_5",
  });
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  expect(submitButton().disabled).toBe(false);
});

// The same wedge without any relaunch: a session that was simply idle when
// this pane hydrated. Stop survives here (a live daemon's interrupt does not
// gate on the turn), which is why the report named Steer and Send first.
test("a live idle session's controls follow the turn its own send starts", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(false));

  // Queue advertises harness support, so an idle snapshot on a queue-capable
  // harness reads true (#1375) and the composer is still a plain send: the
  // status, not the bit, decides which control that is.
  expect(threadsStore.getState().threads.get(REF)?.capabilities.queue).toBe(true);
  await type("hi");
  expect(submitButton().disabled).toBe(false);

  emitTurnStart(fake, "turn_5", daemonCapabilities(true));

  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();
  expect(submitButton().disabled).toBe(false);
});

// Both verbs are SESSION-scoped, so neither gate can be "a turn has told us
// its name".
//
// turn/interrupt and turn/steer carry no turn id (appwire v3 dropped
// expectedTurnId from every control mutation) and the daemon decides each on
// the session's own state. The composer once gated both on an activeTurnId the
// REQUEST does not carry, which can only ever withhold a control the daemon
// would have accepted. The status alone gates either verb now (isTurnActive:
// `statusType === "active"`), and the capability gates them independently, so
// the pair drawn is the pair the daemon advertised beside that status.
//
// Active-with-no-id is a state the wire really reaches: a session holding queued
// work reports active with no turn running, and the id is cleared between the
// turn/completed and turn/started of an inline turn boundary (issue #1330).
// (An earlier version of this comment blamed a turn reservation taken at
// turn/start. That reservation has no production callers; it is not how this
// state is reached, and the gate was wrong for the reason above regardless.)
test("a working session offers Stop and Steer before its turn has announced a name", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(false));

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${REF}`, ref: REF, status: { type: "active" }, capabilities: daemonCapabilities(true) },
    });
  });

  const model = threadsStore.getState().threads.get(REF);
  expect({ status: model?.status.type, activeTurnId: model?.activeTurnId }).toEqual({
    status: "active",
    activeTurnId: undefined,
  });
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
});

// The breadcrumb for kata 5gdv, wired end to end rather than unit-tested in
// isolation: drive the composer into the exact reported shape -- a working
// session advertising steer and not interrupt -- and check that the sighting it
// leaves behind names the frame that put it there.
//
// This state is not reachable from a correct daemon any more, which is why the
// frame is hand-built here. That is the point of the breadcrumb: it exists for
// the trigger nobody has found yet.
test("a working session drawn with no Stop leaves a sighting naming the frame that did it", async () => {
  resetStoplessComposerSightingsForTests();
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  try {
    const fake = new FakeClient("ready");
    connectionStore.getState().connect(fake);
    fake.on("thread/read", () => ({ thread: thread("idle", daemonCapabilities(false)) }) as ThreadReadResponse);
    await threadsStore
      .getState()
      .ensureThread(REF)
      .then(() => {
        render(
          <ClientProvider client={fake}>
            <Composer ref={REF} focused={false} />
          </ClientProvider>,
        );
        act(() => {
          fake.emitNotification({
            method: "thread/status/changed",
            params: {
              threadId: `thr_${REF}`,
              ref: REF,
              status: { type: "active" },
              capabilities: { ...daemonCapabilities(true), interrupt: false },
            },
          });
        });

        expect(screen.queryByTestId("composer-stop")).toBeNull();
        const sightings = stoplessComposerSightings();
        expect(sightings).toHaveLength(1);
        expect(warn).toHaveBeenCalledTimes(1);
        expect(warn.mock.calls).toStrictEqual([
          [
            "[evener 5gdv] composer is showing a working session with no Stop. Please attach this to kata 5gdv:",
            {
              ref: REF,
              status: "active",
              activeTurnId: undefined,
              capabilities: { ...daemonCapabilities(true), interrupt: false },
              capabilitySource: "statusFrame",
              // The frame advertised steer for this status, and the composer
              // draws what the status says (isTurnActive): Steer present with
              // Stop gone is the shape the report named.
              showSteer: true,
              ended: false,
            },
          ],
        ]);
        expect(warn).toHaveBeenCalledWith(expect.any(String), sightings[0]);
        expect(sightings[0]).toMatchObject({
          ref: REF,
          status: "active",
          capabilitySource: "statusFrame",
          capabilities: { interrupt: false, steer: true },
        });
        return settleActivityDiscovery(REF);
      });
  } finally {
    warn.mockRestore();
  }
});

// Both directions, or the fix is just a latch that turns everything on. The
// set itself is what this asserts: with no turn in flight, `busy` alone
// already hides Steer and Stop and the availability table already reports a
// plain send, so the buttons would read correctly here even if the model were
// still holding the active turn's capabilities.
test("the turn ending puts the controls back to a plain send", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(false));
  emitTurnStart(fake, "turn_5", daemonCapabilities(true));

  act(() => {
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        turn: { id: "turn_5", status: "completed", itemsView: "" },
      },
    });
    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        status: { type: "idle" },
        capabilities: daemonCapabilities(false),
      },
    });
  });
  await type("hi");

  expect(threadsStore.getState().threads.get(REF)?.capabilities).toEqual(daemonCapabilities(false));
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(false);
});

// Absent means "no update", exactly like the failure count riding on the same
// notification: a source that omits the capability sends none, and clearing the
// set on absence would strip a session of every action its hydrate advertised.
test("a status change with no capabilities leaves the advertised set alone", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(true));

  emitTurnStart(fake, "turn_5", undefined);
  await type("hi");

  expect(threadsStore.getState().threads.get(REF)?.capabilities).toEqual(daemonCapabilities(true));
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();
  expect(submitButton().disabled).toBe(false);
});

// Kata pk2d, the close frame's own case. A session watched MID-TURN holds the
// set cut for that turn — send:false, because the hub gates Send on "no turn in
// flight" — and then the session shuts down. "closed" is an ENDED status, and
// an ended composer is a follow-up card gated on capabilities.send, so a set
// that means "a turn is running" gets read as "this thread cannot be written
// to" and the whole composer unmounts: no card, no textarea, no Send, until the
// page is reloaded.
//
// A reload heals it because the daemon is gone by then and the read is answered
// by the HUB from the past index, where a cold thread advertises Send (it
// resumes the session on the next message). That is the set the hub now stamps
// onto the close frame it relays, so what the client holds after a close is
// already what the reload would have fetched.
test("a session that shuts down mid-turn keeps a way to reply", async () => {
  const fake = await mountComposer("active", daemonCapabilities(true));

  act(() => {
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        turn: { id: "turn_5", status: "interrupted", itemsView: "" },
      },
    });
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${REF}`, ref: REF, status: { type: "closed" }, capabilities: COLD_CAPABILITIES },
    });
  });

  const model = threadsStore.getState().threads.get(REF);
  expect({ status: model?.status.type, send: model?.capabilities.send }).toEqual({ status: "closed", send: true });
  expect(screen.queryByTestId("composer-input-card")).not.toBeNull();
  expect(screen.queryByRole("textbox", { name: /^message$/i })).not.toBeNull();
});

// The follow-up a resumable ended session can actually be sent: the card is
// only half the affordance if its Send stays grey. Steer and Stop stay gone —
// there is no turn to act on — which is the set saying the right thing in both
// directions rather than a latch that turns everything on.
test("the follow-up to a session that ended mid-turn can be sent", async () => {
  const fake = await mountComposer("active", daemonCapabilities(true));

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${REF}`, ref: REF, status: { type: "closed" }, capabilities: COLD_CAPABILITIES },
    });
  });
  await type("one more thing");

  expect(submitButton().disabled).toBe(false);
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// The frames the projector's openTurn emits when the daemon runs the next
// turn inline behind the one that just ended (a queued message, a notification
// turn, a goal continuation, a drained steering carrier): the previous turn
// closes, the next opens, the status is republished active. The thread status
// never leaves active, and the hub relays each frame as its own WebSocket
// message, so the composer renders between them. Issue #1330: Steer must not
// blink out at that boundary, because the skill guard's turn-end barrier reads
// exactly that button and the daemon is still mid-input.
function emitInlineTurnBoundary(fake: FakeClient, endedTurnId: string, nextTurnId: string): void {
  const frames: Array<[string, AnyNotification]> = [
    ["turn/completed of the previous turn", turnCompletedFrame(endedTurnId)],
    ["turn/started of the next turn", turnStartedFrame(nextTurnId)],
    ["the status frame", statusActiveFrame(daemonCapabilities(true))],
  ];
  for (const [step, frame] of frames) {
    act(() => {
      fake.emitNotification(frame);
    });
    expect(screen.queryByTestId("composer-steer"), `Steer after ${step}`).not.toBeNull();
    expect(screen.queryByTestId("composer-stop"), `Stop after ${step}`).not.toBeNull();
  }
}

// The click follows the same rule as the button. Between the two turn frames
// the model has no activeTurnId, and the handler used to refuse there with a
// "no active turn" toast (issue #1341); the daemon is mid-input and its v3
// turn/steer names no turn, so the steer is sent.
test("a Steer clicked between turn/completed and turn/started sends turn/steer, with no toast", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(false));
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: `thr_${REF}`,
      projectionState: "reflected",
    },
  }));
  emitTurnStart(fake, "turn_5", daemonCapabilities(true));
  await type("go left");

  act(() => {
    fake.emitNotification(turnCompletedFrame("turn_5"));
  });
  expect(threadsStore.getState().threads.get(REF)?.activeTurnId).toBeUndefined();

  await userEvent.click(screen.getByTestId("composer-steer"));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(1));
  expect(fake.calls.find((c) => c.method === "turn/steer")?.params).toMatchObject({
    ref: REF,
    input: [{ type: "text", text: "go left" }],
  });
  expect(screen.queryByText(/no active turn/i)).toBeNull();
});

// A genuine turn failure ends with turn/completed{status: "failed"} followed
// by its own thread/status/changed(idle) frame, capabilities inline (the
// agent's failure exit, agent/session_lifecycle.go endInputAtTurnFailure, kata
// hen0). The status frame turns Stop and Steer off and gives Send back; the
// failed stamp alone leaves the controls alone.
test("a failed turn takes Stop and Steer off and gives Send back", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(false));
  emitTurnStart(fake, "turn_5", daemonCapabilities(true));
  await type("hi");
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();

  act(() => {
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        turn: { id: "turn_5", status: "failed", itemsView: "", error: { message: "rate limited" } },
      },
    });
  });

  // The turn ended; its status frame has not arrived.
  expect(threadsStore.getState().threads.get(REF)?.status.type).toBe("active");

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${REF}`, ref: REF, status: { type: "idle" }, capabilities: daemonCapabilities(false) },
    });
  });

  expect(threadsStore.getState().threads.get(REF)?.status.type).toBe("idle");
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(false);
});

test("Steer and Stop stay on screen across an inline turn boundary delivered one frame at a time", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(false));
  emitTurnStart(fake, "turn_5", daemonCapabilities(true));
  await type("hi");
  expect(screen.queryByTestId("composer-steer")).not.toBeNull();

  emitInlineTurnBoundary(fake, "turn_5", "turn_6");

  expect(screen.queryByTestId("composer-stop")).not.toBeNull();
  expect(threadsStore.getState().threads.get(REF)?.activeTurnId).toBe("turn_6");
});
