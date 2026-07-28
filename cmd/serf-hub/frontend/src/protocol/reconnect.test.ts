// Heartbeat, reconnect-with-backoff, and onReady-refire behavior added on top
// of the Task 4 state machine (see client.ts / client.test.ts). Split into
// its own file per the task manifest; client.test.ts keeps the general
// connect/request/notification/close coverage and owns the one test that
// Task 5 supersedes (the server-close state assertion).
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  AppwireClient,
  type ConnectionState,
  HEARTBEAT_INTERVAL_MS,
  HEARTBEAT_TIMEOUT_MS,
  RECONNECT_BASE_MS,
  RECONNECT_MAX_MS,
} from "./client";
import { ConnectionClosedError } from "./errors";
import { FakeSocket } from "./testing/fakeSocket";

function sentFrames(fake: FakeSocket): Array<Record<string, unknown>> {
  return fake.sent.map((raw) => JSON.parse(raw) as Record<string, unknown>);
}

// flushUntil drains microtask turns until `done()` reports true (or a
// bounded number of turns elapse, so a genuine hang fails fast instead of
// silently). Same contract as client.test.ts's helper of the same name;
// duplicated here because the two test files share no test-utils module.
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

// dialer hands out a socketFactory that mints a fresh FakeSocket on every
// call, the way a real reconnect dials a new transport each attempt, plus
// the list of every socket it has minted so far, in dial order.
function dialer(options: { autoInitialize?: boolean; emitCloseEventOnClientClose?: boolean } = {}): {
  factory: (url: string) => FakeSocket;
  sockets: FakeSocket[];
} {
  const sockets: FakeSocket[] = [];
  const factory = () => {
    const fake = new FakeSocket({
      autoInitialize: options.autoInitialize ?? true,
      emitCloseEventOnClientClose: options.emitCloseEventOnClientClose,
    });
    sockets.push(fake);
    return fake;
  };
  return { factory, sockets };
}

function socketAt(sockets: FakeSocket[], index: number): FakeSocket {
  const s = sockets[index];
  if (!s) throw new Error(`expected a dialed socket at index ${index}`);
  return s;
}

function latestSocket(sockets: FakeSocket[]): FakeSocket {
  return socketAt(sockets, sockets.length - 1);
}

// connectReady drives the first-dialed socket through open() and returns the
// pending connect() promise so callers can await the full initialize
// handshake. Mirrors client.test.ts's helper of the same name.
function connectReady(sockets: FakeSocket[], client: AppwireClient) {
  const connecting = client.connect();
  socketAt(sockets, 0).open();
  return connecting;
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("AppwireClient heartbeat", () => {
  test("sends a ping every 20s while ready, and again 20s later", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(sentFrames(socketAt(sockets, 0)).filter((f) => f.method === "ping")).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(sentFrames(socketAt(sockets, 0)).filter((f) => f.method === "ping")).toHaveLength(2);

    expect(client.state).toBe("ready");
  });

  test("no pong within 10s force-closes the socket and enters reconnecting", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    const states: ConnectionState[] = [];
    client.onStateChange((s) => states.push(s));

    // Stop auto-replying so the next heartbeat ping goes unanswered, like a
    // silently dropped connection whose readyState never reports closed.
    socketAt(sockets, 0).autoInitialize = false;

    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(sentFrames(socketAt(sockets, 0)).some((f) => f.method === "ping")).toBe(true);
    expect(client.state).toBe("ready"); // still within the 10s grace window

    await vi.advanceTimersByTimeAsync(HEARTBEAT_TIMEOUT_MS);
    expect(client.state).toBe("reconnecting");
    expect(states).toContain("reconnecting");
  });

  test("a heartbeat timeout reconnects when client-side close emits no close event", async () => {
    const { factory, sockets } = dialer({ emitCloseEventOnClientClose: false });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).autoInitialize = false;
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS + HEARTBEAT_TIMEOUT_MS);

    expect(client.state).toBe("reconnecting");
    expect(socketAt(sockets, 0).closeRequests).toEqual([undefined]);

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    expect(sockets).toHaveLength(2);
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    expect(client.state).toBe("ready");
  });

  test("a delayed close event from the retired socket leaves its ready replacement connected", async () => {
    const { factory, sockets } = dialer({ emitCloseEventOnClientClose: false });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).autoInitialize = false;
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS + HEARTBEAT_TIMEOUT_MS + RECONNECT_BASE_MS);
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    socketAt(sockets, 0).finishClientClose();

    expect(client.state).toBe("ready");
    expect(sockets).toHaveLength(2);
  });

  test("a retired socket cannot deliver notifications after its replacement is ready", async () => {
    const { factory, sockets } = dialer({ emitCloseEventOnClientClose: false });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    let notifications = 0;
    client.onNotification(() => {
      notifications += 1;
    });

    socketAt(sockets, 0).autoInitialize = false;
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS + HEARTBEAT_TIMEOUT_MS + RECONNECT_BASE_MS);
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    socketAt(sockets, 0).receive({ method: "thread/started", params: {} });

    expect(notifications).toBe(0);
  });
});

describe("AppwireClient reconnect", () => {
  test("backs off 250ms, 500ms, 1000ms, 2000ms, 4000ms, then caps at 5000ms, re-dialing every attempt", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    expect(client.state).toBe("reconnecting");

    const expectedDelays = [250, 500, 1000, 2000, 4000, 5000, 5000];
    expect(expectedDelays[0]).toBe(RECONNECT_BASE_MS);
    expect(expectedDelays[expectedDelays.length - 1]).toBe(RECONNECT_MAX_MS);

    for (const delay of expectedDelays) {
      const before = sockets.length;
      await vi.advanceTimersByTimeAsync(delay);
      expect(sockets.length).toBe(before + 1); // exactly one new dial, exactly at this delay

      // Fail this attempt too, before it ever opens, so the sequence can
      // continue to the next backoff step.
      latestSocket(sockets).closeFromServer(1006);
      await flushUntil(() => vi.getTimerCount() > 0); // let the next backoff timer arm
      expect(client.state).toBe("reconnecting"); // never gives up on its own
    }
  });

  test("a successful attempt re-initializes, reaches ready, refires onReady, and resumes heartbeat", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    let readyCount = 0;
    client.onReady(() => {
      readyCount += 1;
    });
    const states: ConnectionState[] = [];
    client.onStateChange((s) => states.push(s));

    socketAt(sockets, 0).closeFromServer(1006);
    expect(client.state).toBe("reconnecting");

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    expect(sockets).toHaveLength(2);
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    expect(client.state).toBe("ready");
    expect(readyCount).toBe(1); // subscribed after the first ready; this is the refire
    expect(states).toEqual(["reconnecting", "ready"]);

    expect(sentFrames(socketAt(sockets, 1)).map((f) => f.method)).toEqual(["initialize", "initialized"]);

    // Heartbeat resumed on the new socket: not double-armed, not dead.
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(sentFrames(socketAt(sockets, 1)).filter((f) => f.method === "ping")).toHaveLength(1);
  });

  test("a pending request at disconnect is rejected exactly once and is not replayed after reconnect", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    const reqPromise = client.request("thread/list", { limit: 1 });
    expect(sentFrames(socketAt(sockets, 0)).filter((f) => f.method === "thread/list")).toHaveLength(1);

    socketAt(sockets, 0).closeFromServer(1006);

    await expect(reqPromise).rejects.toThrow();
    await expect(reqPromise).rejects.not.toBeInstanceOf(ConnectionClosedError);

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    // Only the handshake rides the new socket: a queued turn/start retried
    // blind could double-fire, so the dropped request must never replay.
    expect(sentFrames(socketAt(sockets, 1)).map((f) => f.method)).toEqual(["initialize", "initialized"]);
  });

  test("requests attempted while reconnecting are rejected synchronously, not queued for replay", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    expect(client.state).toBe("reconnecting");

    await expect(client.request("thread/list", { limit: 1 })).rejects.toThrow(/thread\/list/);

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    expect(sentFrames(socketAt(sockets, 1)).map((f) => f.method)).toEqual(["initialize", "initialized"]);
  });
});

// retryNow is the manual counterpart to the automatic backoff above (wired
// to ConnectionBanner's "Retry now" affordance, shown only while
// "reconnecting" - the client is already retrying on its own, so this just
// lets an impatient user short-circuit the current wait rather than
// starting a second, independent reconnect mechanism).
describe("AppwireClient retryNow", () => {
  test("is a no-op while ready - there is no backoff to short-circuit", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    client.retryNow();

    expect(sockets).toHaveLength(1); // no new dial
    expect(client.state).toBe("ready");
  });

  test("is a no-op once closed", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);
    client.close();

    client.retryNow();

    expect(sockets).toHaveLength(1); // no new dial
    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);
  });

  test("dials immediately mid-backoff, without waiting out the remaining delay", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    expect(client.state).toBe("reconnecting");
    expect(vi.getTimerCount()).toBeGreaterThan(0); // backoff timer armed

    client.retryNow();

    // No time advanced at all: the dial (and the socketFactory call it
    // makes, before its own first await) runs synchronously inside
    // retryNow() itself, not on the next backoff tick.
    expect(sockets).toHaveLength(2);
    expect(vi.getTimerCount()).toBe(0); // the pending backoff timer was cleared, no new one armed yet

    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");
    expect(client.state).toBe("ready");
    expect(sentFrames(socketAt(sockets, 1)).map((f) => f.method)).toEqual(["initialize", "initialized"]);
  });

  test("a successful retryNow reaches ready, refires onReady, and resumes heartbeat - same as an ordinary reconnect", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    let readyCount = 0;
    client.onReady(() => {
      readyCount += 1;
    });
    const states: ConnectionState[] = [];
    client.onStateChange((s) => states.push(s));
    socketAt(sockets, 0).closeFromServer(1006);

    client.retryNow();
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");

    expect(client.state).toBe("ready");
    expect(readyCount).toBe(1); // subscribed after the first ready; this is the refire
    expect(states).toEqual(["reconnecting", "ready"]);

    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(sentFrames(socketAt(sockets, 1)).filter((f) => f.method === "ping")).toHaveLength(1);
  });

  test("does not stack a second dial when called again while the first attempt is still in flight", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    expect(client.state).toBe("reconnecting");

    client.retryNow(); // dials socket #1, still waiting on its own open()
    expect(sockets).toHaveLength(2);

    client.retryNow(); // called again before socket #1 ever opens

    expect(sockets).toHaveLength(2); // still just the one attempt in flight, not a second

    // The one in-flight attempt is still live, not abandoned by the second
    // call - opening it completes a normal reconnect.
    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");
    expect(client.state).toBe("ready");
  });

  test("does not start the scheduled attempt over a reentrant manual retry handshake", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    client.onStateChange((state) => {
      if (state === "reconnecting") client.retryNow();
    });

    socketAt(sockets, 0).closeFromServer(1006);
    expect(sockets).toHaveLength(2);

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);

    expect(sockets).toHaveLength(2);

    socketAt(sockets, 1).open();
    await flushUntil(() => client.state === "ready");
    expect(client.state).toBe("ready");
  });

  test("a failed reentrant manual retry waits for doubled backoff and leaves close terminal", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    client.onStateChange((state) => {
      if (state === "reconnecting") client.retryNow();
    });

    socketAt(sockets, 0).closeFromServer(1006);
    expect(sockets).toHaveLength(2);

    socketAt(sockets, 1).closeFromServer(1006);
    await flushUntil(() => socketAt(sockets, 1).closeRequests.length === 1);

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    expect(sockets).toHaveLength(2);

    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    expect(sockets).toHaveLength(3);

    client.close();
    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);

    await vi.advanceTimersByTimeAsync(RECONNECT_MAX_MS * 2);
    expect(sockets).toHaveLength(3);
    expect(vi.getTimerCount()).toBe(0);
  });

  test("a failed retryNow attempt falls back to the ordinary backoff sequence, continuing from where it left off", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    client.retryNow(); // attempt #1 (manual)
    expect(sockets).toHaveLength(2);
    socketAt(sockets, 1).closeFromServer(1006); // fails before ever opening

    await flushUntil(() => vi.getTimerCount() > 0); // the next backoff timer arms
    expect(client.state).toBe("reconnecting");

    // reconnectAttempts wasn't reset by the manual attempt failing (only a
    // SUCCESSFUL handshake resets it) - the next automatic delay is
    // RECONNECT_BASE_MS * 2 (attempt #1 already consumed the base delay's
    // own slot when scheduleReconnect first armed it after the initial
    // drop), not back to the base delay as if retryNow had never happened.
    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS * 2 - 1);
    expect(sockets).toHaveLength(2); // not yet
    await vi.advanceTimersByTimeAsync(1);
    expect(sockets).toHaveLength(3); // now
  });
});

describe("AppwireClient close() during heartbeat/reconnect", () => {
  test("stops heartbeat and reconnection permanently, leaving no timers armed", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    expect(vi.getTimerCount()).toBeGreaterThan(0); // the heartbeat interval is armed

    client.close();

    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);

    // Real time marching on afterward must not resurrect any activity: no
    // ping, no reconnect dial, no state change.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(client.state).toBe("closed");
    expect(sockets).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  test("while a reconnect backoff timer is armed cancels the pending attempt", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    expect(client.state).toBe("reconnecting");
    expect(vi.getTimerCount()).toBeGreaterThan(0); // backoff timer armed

    client.close();

    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);

    await vi.advanceTimersByTimeAsync(RECONNECT_MAX_MS * 2);
    expect(sockets).toHaveLength(1); // the armed attempt never dialed
    expect(client.state).toBe("closed");
  });

  test("while a reconnect dial is in flight aborts it without scheduling another attempt", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    expect(sockets).toHaveLength(2); // the reconnect attempt dialed and is waiting on open()

    client.close();

    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);

    // A stale open() on the aborted attempt's socket — already superseded —
    // must not resurrect the client or start another reconnect attempt.
    socketAt(sockets, 1).open();
    await flushUntil(() => sockets.length > 2, 5);
    expect(client.state).toBe("closed");
    expect(sockets).toHaveLength(2);
  });

  // Distinct from the "dial is in flight" test above: here the attempt's
  // socket has already opened and is mid-RPC (waiting on the server's reply
  // to "initialize"), not waiting on open() at all. This exercises a
  // different internal path — failAllPending()'s ConnectionClosedError
  // rejecting the in-flight request, observed via attemptReconnect's catch —
  // that was reviewed by inspection but had no test locking it.
  test("while a reconnect attempt's initialize is in flight lands closed with no timers", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    socketAt(sockets, 0).closeFromServer(1006);
    await vi.advanceTimersByTimeAsync(RECONNECT_BASE_MS);
    expect(sockets).toHaveLength(2);

    // Suppress auto-reply so "initialize" stays in flight until close().
    socketAt(sockets, 1).autoInitialize = false;
    socketAt(sockets, 1).open();
    await flushUntil(() => sentFrames(socketAt(sockets, 1)).some((f) => f.method === "initialize"));
    expect(client.state).toBe("reconnecting"); // not ready yet: initialize hasn't resolved

    client.close();

    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);

    // The rejected in-flight request must not resurrect anything once its
    // rejection is observed on a later microtask turn.
    await flushUntil(() => false, 5);
    expect(client.state).toBe("closed");
    expect(sockets).toHaveLength(2);
  });
});

// Reentrancy: setState() dispatches to onStateChange/onReady subscribers
// *synchronously*, so a subscriber that calls close() from inside that
// dispatch runs it to completion before control returns to whichever method
// called setState(). Three call sites continue unconditionally right after a
// setState() call (dialAndHandshake at "ready", scheduleReconnect at
// "reconnecting", and — via dialAndHandshake — performHandshake at
// "connecting" on the very first connect); each is a distinct leak/orphan
// hazard if a reentrant close() isn't re-checked for.
describe("AppwireClient reentrant close() from inside setState dispatch", () => {
  test("closing at 'ready' leaves no heartbeat timer armed", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    client.onStateChange((s) => {
      if (s === "ready") client.close();
    });

    await connectReady(sockets, client);

    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);
  });

  test("closing at 'reconnecting' leaves no reconnect backoff timer armed", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    await connectReady(sockets, client);

    client.onStateChange((s) => {
      if (s === "reconnecting") client.close();
    });
    socketAt(sockets, 0).closeFromServer(1006);

    expect(client.state).toBe("closed");
    expect(vi.getTimerCount()).toBe(0);
  });

  test("closing at 'connecting' on the first connect dials no orphaned socket and rejects with ConnectionClosedError", async () => {
    const { factory, sockets } = dialer();
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: factory });
    client.onStateChange((s) => {
      if (s === "connecting") client.close();
    });

    await expect(client.connect()).rejects.toBeInstanceOf(ConnectionClosedError);

    expect(client.state).toBe("closed");
    expect(sockets).toHaveLength(0); // socketFactory was never called
    expect(vi.getTimerCount()).toBe(0);
  });
});
