import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { AppwireClient, type AnyNotification, type ConnectionState } from "./client";
import { ConnectionClosedError, RequestTimeoutError, WireError } from "./errors";
import { rpcURLFromLocation } from "./transport";
import { FAKE_INITIALIZE_RESULT, FakeSocket } from "./testing/fakeSocket";

const DEFAULT_CLIENT_INFO = { name: "serf-web", version: "0.1.0" };
const DEFAULT_CAPABILITIES = { experimentalApi: false };

function sentFrames(fake: FakeSocket): Array<Record<string, unknown>> {
  return fake.sent.map((raw) => JSON.parse(raw) as Record<string, unknown>);
}

function lastSentFrame(fake: FakeSocket): Record<string, unknown> {
  const frames = sentFrames(fake);
  const last = frames[frames.length - 1];
  if (!last) throw new Error("expected at least one sent frame");
  return last;
}

// connectReady drives a FakeSocket through open() and returns the pending
// connect() promise so callers can await the full initialize handshake.
function connectReady(fake: FakeSocket, client: AppwireClient) {
  const connecting = client.connect();
  fake.open();
  return connecting;
}

// flushUntil drains microtask turns until `done()` reports true (or a
// bounded number of turns elapse, so a genuine hang fails fast instead of
// silently). Promise continuations inside AppwireClient (e.g. resuming after
// waitForOpen) need at least one turn to run before a test can inspect their
// side effects, like fake.sent; polling a real condition instead of a magic
// flush count keeps this correct if a future change (performHandshake is
// exactly what the next change touches) adds or removes an await in that path.
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("rpcURLFromLocation", () => {
  test("upgrades https to wss", () => {
    expect(rpcURLFromLocation({ protocol: "https:", host: "example.com" })).toBe("wss://example.com/rpc");
  });

  test("upgrades http to ws", () => {
    expect(rpcURLFromLocation({ protocol: "http:", host: "localhost:5173" })).toBe("ws://localhost:5173/rpc");
  });
});

describe("AppwireClient", () => {
  test("connect performs initialize handshake then notifies ready", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    const states: ConnectionState[] = [];
    client.onStateChange((s) => states.push(s));
    let readyCount = 0;
    client.onReady(() => {
      readyCount += 1;
    });

    const result = await connectReady(fake, client);

    expect(client.state).toBe("ready");
    expect(states).toEqual(["connecting", "ready"]);
    expect(readyCount).toBe(1);
    expect(result).toEqual(FAKE_INITIALIZE_RESULT);

    const frames = sentFrames(fake);
    expect(frames[0]).toEqual({
      id: 1,
      method: "initialize",
      params: { clientInfo: DEFAULT_CLIENT_INFO, capabilities: DEFAULT_CAPABILITIES },
    });
    expect(frames[1]).toEqual({ method: "initialized", params: {} });
  });

  test("connect is idempotent across concurrent callers", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });

    const first = client.connect();
    const second = client.connect();
    fake.open();
    await first;

    expect(second).toBe(first);
    expect(sentFrames(fake).filter((f) => f.method === "initialize")).toHaveLength(1);
  });

  test("connect sends a caller-provided clientInfo instead of the default", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({
      url: "ws://x/rpc",
      socketFactory: () => fake,
      clientInfo: { name: "custom-client", version: "9.9.9" },
    });

    await connectReady(fake, client);

    const initializeFrame = sentFrames(fake)[0];
    expect(initializeFrame?.params).toMatchObject({
      clientInfo: { name: "custom-client", version: "9.9.9" },
    });
  });

  test("connect rejects and closes when the server rejects initialize", async () => {
    const fake = new FakeSocket({ autoInitialize: false });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });

    const connecting = client.connect();
    fake.open();
    const rejection = expect(connecting).rejects.toBeInstanceOf(WireError);

    // Let performHandshake resume past waitForOpen and actually send
    // "initialize" before replying to it.
    await flushUntil(() => fake.sent.length > 0);
    const frame = lastSentFrame(fake);
    fake.receive({ id: frame.id, error: { code: 401, message: "unauthorized" } });

    await rejection;
    expect(client.state).toBe("closed");
  });

  test("request resolves the matching id and types the result", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const reqPromise = client.request("thread/list", { limit: 10 });
    const frame = lastSentFrame(fake);
    expect(frame.method).toBe("thread/list");
    fake.receive({ id: frame.id, result: { data: [], nextCursor: "" } });

    await expect(reqPromise).resolves.toEqual({ data: [], nextCursor: "" });
  });

  test("request rejects with WireError on error response", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const reqPromise = client.request("thread/list", { limit: 10 });
    const frame = lastSentFrame(fake);
    fake.receive({
      id: frame.id,
      error: { code: 404, message: "thread not found", data: { serfErrorInfo: "boom" } },
    });

    await expect(reqPromise).rejects.toBeInstanceOf(WireError);
    await expect(reqPromise).rejects.toMatchObject({
      code: 404,
      message: "thread not found",
      data: { serfErrorInfo: "boom" },
      serfErrorInfo: "boom",
    });
  });

  test("request rejects with RequestTimeoutError after timeoutMs", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const reqPromise = client.request("thread/list", { limit: 10 }, { timeoutMs: 5000 });
    // Attach the rejection expectation before advancing the fake clock: once
    // attached it installs a handler synchronously, so the promise is never
    // observably unhandled when the timer callback rejects it below.
    const rejection = expect(reqPromise).rejects.toBeInstanceOf(RequestTimeoutError);
    await vi.advanceTimersByTimeAsync(5000);

    await rejection;
  });

  test("notifications dispatch to subscribers with method+params", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const received: AnyNotification[] = [];
    client.onNotification((n) => received.push(n));

    fake.receive({ method: "thread/started", params: {} });

    expect(received).toEqual([{ method: "thread/started", params: {} }]);
  });

  test("notification unsubscribe is idempotent and stops delivery", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const received: AnyNotification[] = [];
    const unsubscribe = client.onNotification((n) => received.push(n));

    fake.receive({ method: "thread/started", params: {} });
    unsubscribe();
    unsubscribe(); // must not throw on a second call

    fake.receive({ method: "thread/started", params: {} });

    expect(received).toHaveLength(1);
  });

  test("requests before ready are rejected except initialize/ping", async () => {
    const fake = new FakeSocket({ autoInitialize: false });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });

    void client.connect();
    fake.open();
    expect(client.state).toBe("connecting");

    await expect(client.request("thread/list", { limit: 1 })).rejects.toThrow(/thread\/list/);

    void client.request("ping", {});
    expect(sentFrames(fake).some((f) => f.method === "ping")).toBe(true);
  });

  test("close() transitions to closed and rejects pending requests", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const reqPromise = client.request("thread/list", { limit: 1 });
    client.close();

    expect(client.state).toBe("closed");
    await expect(reqPromise).rejects.toBeInstanceOf(ConnectionClosedError);
    await expect(client.request("thread/list", { limit: 1 })).rejects.toThrow();
  });

  test("server-initiated close fails pending requests and updates state", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });
    await connectReady(fake, client);

    const states: ConnectionState[] = [];
    client.onStateChange((s) => states.push(s));
    const reqPromise = client.request("thread/list", { limit: 1 });

    fake.closeFromServer(1006);

    expect(client.state).toBe("closed");
    expect(states).toEqual(["closed"]);
    await expect(reqPromise).rejects.toThrow();
  });

  test("close() after socket open but before ready rejects the pending connect() promise", async () => {
    const fake = new FakeSocket({ autoInitialize: false });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });

    const connecting = client.connect();
    fake.open();
    // Let performHandshake resume past waitForOpen and register+send
    // "initialize" so close() rejects it via the pending-request path
    // (rather than the earlier waitForOpen-abort path exercised by the
    // "before the socket ever opens" test below).
    await flushUntil(() => fake.sent.length > 0);
    // Attach before close() so the rejection is never observably unhandled
    // (same reasoning as the timeout test above).
    const rejection = expect(connecting).rejects.toBeInstanceOf(ConnectionClosedError);

    client.close();

    await rejection;
    expect(client.state).toBe("closed");
  });

  // Regression test: close() used to null socket.onopen/onclose/onerror
  // before calling socket.close(), which wiped the exact handlers
  // waitForOpen()'s promise needed to ever settle — connect() would hang
  // forever (state flips to "closed" but nothing ever resolves/rejects the
  // caller's promise). A short per-test timeout makes a reintroduced hang
  // fail fast instead of burning the whole suite's default timeout.
  test(
    "close() before the socket ever opens still rejects the pending connect() promise",
    async () => {
      const fake = new FakeSocket({ autoInitialize: false });
      const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });

      const connecting = client.connect();
      // Deliberately never call fake.open().
      const rejection = expect(connecting).rejects.toBeInstanceOf(ConnectionClosedError);

      client.close();

      await rejection;
      expect(client.state).toBe("closed");
    },
    1000,
  );

  // Regression test: setState()'s dispatch loops had no per-handler
  // try/catch (unlike notification dispatch), so a throwing subscriber on
  // setState("ready") — called from inside performHandshake's try — was
  // caught by the handshake's own catch and tore down an otherwise-successful
  // connection.
  test("throwing onStateChange/onReady subscribers do not abort a successful connect()", async () => {
    const fake = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake });

    const states: ConnectionState[] = [];
    let readyCount = 0;
    client.onStateChange(() => {
      throw new Error("boom from a bad state subscriber");
    });
    client.onStateChange((s) => states.push(s));
    client.onReady(() => {
      throw new Error("boom from a bad ready subscriber");
    });
    client.onReady(() => {
      readyCount += 1;
    });

    const result = await connectReady(fake, client);

    expect(client.state).toBe("ready");
    expect(result).toEqual(FAKE_INITIALIZE_RESULT);
    expect(states).toEqual(["connecting", "ready"]);
    expect(readyCount).toBe(1);
  });
});
