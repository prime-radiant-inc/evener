// The composer's three controls are derived from two things that used to
// refresh on different clocks: the thread's STATUS, which every live frame
// updates, and its CAPABILITIES, which only a thread/read ever set.
//
//   showStop  = busy && capabilities.interrupt
//   showSteer = busy && capabilities.steer
//   Send      = ... && deriveSendQueueAvailability(status, capabilities)
//
// Three of the hub's capabilities are themselves defined by whether a turn is
// in flight (server/appwire_runtime.go's appCapabilities: Send is !active,
// Steer and Queue are active), so a snapshot cut before the turn says
// steer=false/queue=false about the turn that follows. Reading it back once
// the status has moved on produced kata 06t8's report exactly: submit a reply,
// and the session it KNOWS is running shows no Steer, no Stop, and a Send that
// stays grey however much you type — until a reload re-reads the snapshot from
// the now-active daemon.
//
// thread/status/changed carries the matching set now, so this file drives the
// real frame sequence a resumed session produces and asserts the controls
// follow it. Absent capabilities still mean "no update" (the Codex bridge
// state-gates nothing and sends none), which is its own case below.
//
// The close frame is the same defect at the other end of a session's life
// (kata pk2d) and the last cases here are its own: a daemon cannot describe
// what the thread it is leaving can still be asked to do, so the HUB stamps
// that frame on the way past (cmd/serf-hub/app_relay.go's
// stampClosedThreadCapabilities). Without it a session that shut down mid-turn
// keeps send=false, and an ended composer is a follow-up card gated on exactly
// that bit — so the whole composer disappears.
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { Composer } from "./Composer";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

const REF = "ref_a";

// What the hub advertises for a COLD exited session, verbatim from
// cmd/serf-hub/app_threadread.go's pastEntryThread: it can be sent to (the
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
  queue: false,
  goal: true,
  rename: true,
};

// What a LIVE daemon with every callback wired advertises, verbatim from
// server/appwire_runtime.go's appCapabilities. `active` is the whole
// difference, and it is the reason a snapshot cannot be reused across a
// status change.
function daemonCapabilities(active: boolean): ThreadCapabilities {
  return {
    send: !active,
    steer: active,
    interrupt: true,
    compact: true,
    clear: false,
    forkFromTurn: false,
    shutdown: true,
    changeModel: true,
    queue: active,
    goal: true,
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
    source: "serf",
    serf: { ref: REF, capabilities, queue: { revision: 0 } },
  };
}

async function mountComposer(status: string, capabilities: ThreadCapabilities): Promise<FakeClient> {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("thread/read", () => ({ thread: thread(status, capabilities) }) as ThreadReadResponse);
  await threadsStore.getState().ensureThread(REF);
  render(
    <>
      <Toast />
      <Composer ref={REF} />
    </>,
  );
  return fake;
}

// The frames a turn opening produces, in the projector's own order
// (internal/appprojector/appwire_projection.go, EventUserInput): the turn, the
// user's message, then the status — with the capability set the daemon stamps
// on it at its notification egress. `capabilities: undefined` is the same
// sequence from a source that state-gates nothing.
function emitTurnStart(fake: FakeClient, turnId: string, capabilities?: ThreadCapabilities): void {
  act(() => {
    fake.emitNotification({
      method: "turn/started",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        turn: { id: turnId, status: "inProgress", itemsView: "full", startedAt: 5000 },
      },
    });
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: `thr_${REF}`,
        ref: REF,
        turnId,
        item: { type: "userMessage", id: "item_user_1", turnId, text: "another thought", status: "completed" },
      },
    });
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${REF}`, ref: REF, status: { type: "active" }, capabilities },
    });
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
  await type("another thought");

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

  emitTurnStart(fake, "turn_5", daemonCapabilities(true));
  await type("another thought");

  expect(screen.queryByTestId("composer-steer")).not.toBeNull();
  expect(screen.queryByTestId("composer-stop")).not.toBeNull();
  expect(submitButton().disabled).toBe(false);
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
        turnId: "turn_5",
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
  await type("another thought");

  expect(threadsStore.getState().threads.get(REF)?.capabilities).toEqual(daemonCapabilities(false));
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(submitButton().disabled).toBe(false);
});

// Absent means "no update", exactly like the failure count riding on the same
// notification: a source that does not state-gate its capabilities (the Codex
// bridge) sends none, and clearing the set on absence would strip a session of
// every action its hydrate advertised.
test("a status change with no capabilities leaves the advertised set alone", async () => {
  const fake = await mountComposer("idle", daemonCapabilities(true));

  emitTurnStart(fake, "turn_5", undefined);
  await type("another thought");

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
        turnId: "turn_5",
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
