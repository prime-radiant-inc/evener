// Heartbeat, reconnect-with-backoff, and onReady-refire behavior added on top
// of the Task 4 state machine (see client.ts / client.test.ts). Split into
// its own file per the task manifest; client.test.ts keeps the general
// connect/request/notification/close coverage and owns the one test that
// Task 5 supersedes (the server-close state assertion).
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  AppwireClient,
  HEARTBEAT_INTERVAL_MS,
  HEARTBEAT_TIMEOUT_MS,
  RECONNECT_BASE_MS,
  RECONNECT_MAX_MS,
  type ConnectionState,
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
function dialer(options: { autoInitialize?: boolean } = {}): {
  factory: (url: string) => FakeSocket;
  sockets: FakeSocket[];
} {
  const sockets: FakeSocket[] = [];
  const factory = () => {
    const fake = new FakeSocket({ autoInitialize: options.autoInitialize ?? true });
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
});
