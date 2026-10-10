import { once } from "node:events";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { Thread } from "@evener/appwire-client";
import { bindFilePath, type DocPort, type FileReference } from "@evener/appwire-client/docContent";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { useDocumentRead } from "./useDocumentRead";

// Local complete wire fixture, matching the transcript tests' hydration shape.
const thread: Thread = {
  id: "thr_owner",
  sessionId: "sess_owner",
  preview: "test",
  ephemeral: false,
  modelProvider: "anthropic/claude-sonnet-4-5",
  createdAt: 1000,
  updatedAt: 1000,
  status: { type: "idle" },
  cwd: "/work/a",
  cliVersion: "1.0.0",
  source: "evener",
  evener: {
    ref: "remote:034MXwo6BpPH0QQCgdICSf",
    capabilities: {
      send: true,
      steer: true,
      interrupt: true,
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      queue: true,
      goal: true,
      sharedNotes: true,
      rename: true,
    },
    queue: { revision: 0 },
  },
};
const session = thread.evener?.ref ?? "";
function bound(path = "plan.md", cwd = "/work/a"): FileReference {
  const reference = bindFilePath(path, cwd);
  if (!reference) throw new Error("expected reference");
  return reference;
}
let answer: (request: IncomingMessage, response: ServerResponse) => void;
let requests: URL[];
let port: DocPort;
let closeHTTP: () => Promise<void>;
let client: FakeClient;
let images: HTMLImageElement[];
let finishes: number;
let finishWaiters: Array<{ count: number; resolve: () => void }>;
async function listenHTTP(server: Server): Promise<string> {
  for (let attempt = 0; attempt < 64; attempt += 1) {
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("expected TCP fixture");
    // Installed Node 22 Fetch's highest forbidden port is 10080. The sandbox
    // can allocate these as ephemeral ports, so use the safe range above it.
    if (address.port > 10080) return `http://127.0.0.1:${address.port}`;
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  }
  throw new Error("could not obtain a Fetch-safe HTTP fixture port in 64 attempts");
}
function finished() {
  finishes += 1;
  for (const waiter of finishWaiters) if (finishes >= waiter.count) waiter.resolve();
  finishWaiters = finishWaiters.filter((waiter) => waiter.count > finishes);
}
async function readSettled(count: number) {
  await act(async () => {
    if (finishes < count) await new Promise<void>((resolve) => finishWaiters.push({ count, resolve }));
  });
}
beforeEach(async () => {
  connectionStore.setState({ client: null, state: "idle" });
  client = new FakeClient("ready");
  connectionStore.getState().connect(client);
  requests = [];
  images = [];
  finishes = 0;
  finishWaiters = [];
  answer = (_request, response) => {
    response.setHeader("Content-Type", "text/plain");
    response.end("healthy bytes");
  };
  const server = createServer((request, response) => {
    // Do not leave native fetch pooled sockets alive across fixture retirement.
    response.setHeader("Connection", "close");
    requests.push(new URL(request.url ?? "", "http://fixture"));
    answer(request, response);
  });
  port = {
    origin: await listenHTTP(server),
    fetch: async (url) => {
      const response = await fetch(url).catch((error) => {
        finished();
        throw error;
      });
      if (!response.ok) {
        finished();
        return response;
      }
      return {
        ok: response.ok,
        status: response.status,
        headers: response.headers,
        arrayBuffer: async () => {
          const buffer = await response.arrayBuffer();
          finished();
          return buffer;
        },
      };
    },
  };
  vi.stubGlobal("Image", function imageRequest() {
    const image = document.createElement("img");
    images.push(image);
    return image;
  });
  closeHTTP = async () => {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  };
});
afterEach(async () => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await closeHTTP();
});
function renderRead(reference = bound()) {
  return renderHook(
    ({ reference, reopen, visible, port }) => useDocumentRead(session, reference, reopen, visible, port),
    { initialProps: { reference, reopen: 0, visible: true, port } },
  );
}
function fail(status = 503) {
  answer = (_request, response) => {
    response.statusCode = status;
    response.end("attachment unavailable");
  };
}
function recover() {
  answer = (_request, response) => {
    response.setHeader("Content-Type", "text/plain");
    response.end("recovered bytes");
  };
}

test("replacement clears A even when B fails and late A arrives", async () => {
  let aReads = 0;
  let releaseA: (() => void) | undefined;
  let healthyB = false;
  answer = (request, response) => {
    const target = new URL(request.url ?? "", "http://fixture").searchParams.get("path");
    response.setHeader("Content-Type", "text/plain");
    if (target === "/work/a/plan.md") {
      aReads += 1;
      if (aReads === 1) response.end("A bytes");
      else
        releaseA = () => {
          if (!response.destroyed && !response.writableEnded) response.end("late A bytes");
        };
    } else if (target === "/work/b/plan.md") {
      response.statusCode = healthyB ? 200 : 503;
      response.end(healthyB ? "B bytes" : "attachment unavailable");
    } else {
      response.statusCode = 404;
      response.end("unexpected target");
    }
  };
  const observed: Array<string | undefined> = [];
  const view = renderHook(
    ({ reference }) => {
      const state = useDocumentRead(session, reference, 0, true, port);
      observed.push(state.content?.text);
      return state;
    },
    { initialProps: { reference: bound() } },
  );
  await waitFor(() => expect(view.result.current.content?.text).toBe("A bytes"));
  act(() => view.result.current.reload());
  await waitFor(() => expect(releaseA).toBeTypeOf("function"));
  act(() => {
    view.result.current.reload();
    view.result.current.reload();
  });
  expect(requests).toHaveLength(2);
  observed.length = 0;
  view.rerender({ reference: bound("plan.md", "/work/b") });
  expect(observed[0]).toBeUndefined();
  expect(view.result.current.content).toBeUndefined();
  if (!releaseA) throw new Error("old read did not reach fixture");
  releaseA();
  await waitFor(() => expect(view.result.current.errorKind).toBe("error"));
  expect(view.result.current.content).toBeUndefined();
  healthyB = true;
  act(() => view.result.current.reload());
  await waitFor(() => expect(view.result.current.content?.text).toBe("B bytes"));
});
test("port origin replacement suppresses old bytes in render before effects", async () => {
  const observed: Array<string | undefined> = [];
  const original = bound();
  const view = renderHook(
    ({ selectedPort }) => {
      const state = useDocumentRead(session, original, 0, true, selectedPort);
      observed.push(state.content?.text);
      return state;
    },
    { initialProps: { selectedPort: port } },
  );
  await readSettled(1);
  expect(view.result.current.content?.text).toBe("healthy bytes");
  const server = createServer((_request, response) => {
    response.setHeader("Connection", "close");
    response.statusCode = 503;
    response.end("new hub unavailable");
  });
  const origin = await listenHTTP(server);
  try {
    observed.length = 0;
    view.rerender({ selectedPort: { ...port, origin } });
    expect(observed[0]).toBeUndefined();
    expect(view.result.current.content).toBeUndefined();
    await readSettled(2);
    expect(view.result.current.errorKind).toBe("error");
  } finally {
    view.unmount();
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  }
});
test.each([403, 404, 501])("real HTTP %i stops timers but explicit reload recovers", async (status) => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  fail(status);
  const view = renderRead();
  await readSettled(1);
  expect(view.result.current.errorKind).toBe(
    status === 403 ? "forbidden" : status === 404 ? "not-found" : "host-unsupported",
  );
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => vi.advanceTimersByTimeAsync(60_000));
  expect(requests).toHaveLength(1);
  recover();
  act(() => view.result.current.reload());
  await readSettled(2);
  expect(view.result.current.content?.text).toBe("recovered bytes");
});
test("continuously-ready remote attachment retries at 1,2,4,8,15,15 seconds and recovers", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  fail();
  const view = renderRead();
  await readSettled(1);
  let count = 1;
  for (const delay of [1000, 2000, 4000, 8000, 15000, 15000]) {
    await act(async () => vi.advanceTimersByTimeAsync(delay - 1));
    expect(requests).toHaveLength(count);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    count += 1;
    await readSettled(count);
    expect(requests).toHaveLength(count);
    expect(connectionStore.getState().state).toBe("ready");
  }
  recover();
  await act(async () => vi.advanceTimersByTimeAsync(15000));
  await readSettled(count + 1);
  expect(view.result.current.content?.text).toBe("recovered bytes");
  await act(async () => vi.advanceTimersByTimeAsync(60000));
  expect(requests).toHaveLength(count + 1);
  expect(vi.getTimerCount()).toBe(0);
});
test("same-identity transient failure retains bytes and actual HTTP explanation", async () => {
  const view = renderRead();
  await readSettled(1);
  fail();
  act(() => view.result.current.reload());
  await readSettled(2);
  expect(view.result.current.content?.text).toBe("healthy bytes");
  expect(view.result.current.notice).toBe("readDocFile: error (status 503)");
});
test("transport disconnect is transient and paced recovery becomes useful", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let dropped = false;
  answer = (request) => {
    dropped = true;
    request.socket.destroy();
  };
  const view = renderRead();
  await readSettled(1);
  expect(view.result.current.errorKind).toBe("error");
  expect(dropped).toBe(true);
  recover();
  await act(async () => vi.advanceTimersByTimeAsync(1000));
  await readSettled(2);
  expect(view.result.current.content?.text).toBe("recovered bytes");
});
test("only owning resync and controller recovery refresh, unrelated host events do not", async () => {
  const view = renderRead();
  await readSettled(1);
  act(() =>
    client.emitNotification({
      method: "evener/thread/resync",
      params: { ref: "other:unrelated", threadId: "thr_other" },
    }),
  );
  act(() =>
    client.emitNotification({
      method: "evener/host/notification",
      params: { host: "other", method: "evener/auth/updated", params: {} },
    }),
  );
  expect(requests).toHaveLength(1);
  act(() => client.emitNotification({ method: "evener/thread/resync", params: { ref: session, threadId: thread.id } }));
  await readSettled(2);
  act(() => client.emitStateChange("reconnecting"));
  recover();
  act(() => client.emitStateChange("ready"));
  await readSettled(3);
  expect(view.result.current.content?.text).toBe("recovered bytes");
});
test("hide, background and resume retain single flight, retired completion never publishes, close removes timers and listeners", async () => {
  let release: (() => void) | undefined;
  answer = (_request, response) => {
    response.setHeader("Content-Type", "text/plain");
    release = () => response.end("retired bytes");
  };
  const listeners = client.listenerCount;
  const view = renderRead();
  await waitFor(() => expect(release).toBeTypeOf("function"));
  view.rerender({ reference: bound(), reopen: 0, visible: false, port });
  view.rerender({ reference: bound(), reopen: 0, visible: true, port });
  expect(requests).toHaveLength(1);
  recover();
  release?.();
  await readSettled(2);
  expect(view.result.current.content?.text).toBe("recovered bytes");
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  fail();
  act(() => view.result.current.reload());
  await readSettled(3);
  expect(vi.getTimerCount()).toBe(1);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  act(() => fireEvent(document, new Event("visibilitychange")));
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => vi.advanceTimersByTimeAsync(60000));
  expect(requests).toHaveLength(3);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  recover();
  act(() => fireEvent(document, new Event("visibilitychange")));
  await readSettled(4);
  expect(view.result.current.content?.text).toBe("recovered bytes");
  view.unmount();
  expect(client.listenerCount).toBe(listeners);
  expect(vi.getTimerCount()).toBe(0);
  act(() => client.emitNotification({ method: "evener/thread/resync", params: { ref: session, threadId: thread.id } }));
  await act(async () => vi.advanceTimersByTimeAsync(60000));
  expect(requests).toHaveLength(4);
});
test("image same-identity preload preserves healthy generation on failure and swaps only current successful loads", async () => {
  const reference = bound("pic.png");
  const view = renderRead(reference);
  await waitFor(() => expect(images).toHaveLength(1));
  const first = images[0] as HTMLImageElement;
  act(() => fireEvent.load(first));
  const generation = view.result.current.imageGeneration;
  expect(generation).toBeTruthy();
  act(() => view.result.current.reload());
  await waitFor(() => expect(images).toHaveLength(2));
  expect(view.result.current.imageGeneration).toBe(generation);
  const second = images[1] as HTMLImageElement;
  expect(second.src).not.toBe(first.src);
  act(() => fireEvent.error(second));
  expect(view.result.current.imageGeneration).toBe(generation);
  expect(view.result.current.notice).toMatch(/Image/);
  act(() => view.result.current.reload());
  await waitFor(() => expect(images).toHaveLength(3));
  act(() => fireEvent.load(images[2] as HTMLImageElement));
  expect(view.result.current.imageGeneration).not.toBe(generation);
  expect(view.result.current.notice).toBeUndefined();
});
test("retired image load settles before replacement starts, hiding resumes single-flight and close detaches handlers", async () => {
  const reference = bound("pic.png");
  const view = renderRead(reference);
  await waitFor(() => expect(images).toHaveLength(1));
  const first = images[0] as HTMLImageElement;
  view.rerender({ reference: bound("pic.png", "/work/b"), reopen: 0, visible: true, port });
  expect(view.result.current.imageGeneration).toBeUndefined();
  expect(images).toHaveLength(1);
  act(() => fireEvent.load(first));
  await waitFor(() => expect(images).toHaveLength(2));
  expect(view.result.current.imageGeneration).toBeUndefined();
  const second = images[1] as HTMLImageElement;
  view.rerender({ reference: bound("pic.png", "/work/b"), reopen: 0, visible: false, port });
  view.rerender({ reference: bound("pic.png", "/work/b"), reopen: 0, visible: true, port });
  expect(images).toHaveLength(2);
  act(() => fireEvent.error(second));
  await waitFor(() => expect(images).toHaveLength(3));
  expect(view.result.current.notice).toBeUndefined();
  const third = images[2] as HTMLImageElement;
  view.unmount();
  expect(third.onload).toBeNull();
  expect(third.onerror).toBeNull();
  expect(third.hasAttribute("src")).toBe(false);
  fireEvent.load(third);
  expect(images).toHaveLength(3);
});

test("session route replacement suppresses content in its first render and captures the replacement session", async () => {
  const reference = bound();
  const observed: Array<string | undefined> = [];
  const view = renderHook(
    ({ selectedSession }) => {
      const state = useDocumentRead(selectedSession, reference, 0, true, port);
      observed.push(state.content?.text);
      return state;
    },
    { initialProps: { selectedSession: session } },
  );
  await readSettled(1);
  fail();
  observed.length = 0;
  view.rerender({ selectedSession: "local:034MXwo6BpPH0QQCgdICSf" });
  expect(observed[0]).toBeUndefined();
  await readSettled(2);
  expect(view.result.current.content).toBeUndefined();
  expect(requests[1]?.searchParams.get("session")).toBe("local:034MXwo6BpPH0QQCgdICSf");
});

test("same identity reopen requests current bytes without clearing healthy content", async () => {
  const reference = bound();
  const view = renderRead(reference);
  await readSettled(1);
  let release: (() => void) | undefined;
  answer = (_request, response) => {
    response.setHeader("Content-Type", "text/plain");
    release = () => response.end("new current bytes");
  };
  view.rerender({ reference, reopen: 1, visible: true, port });
  expect(view.result.current.content?.text).toBe("healthy bytes");
  await waitFor(() => expect(release).toBeTypeOf("function"));
  release?.();
  await readSettled(2);
  expect(view.result.current.content?.text).toBe("new current bytes");
});

test("image retry increments generation without resetting backoff and stops on success or close", async () => {
  const view = renderRead(bound("pic.png"));
  await waitFor(() => expect(images).toHaveLength(1));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const urls = new Set<string>();
  let count = 1;
  for (const delay of [1000, 2000, 4000, 8000, 15000, 15000]) {
    const image = images[count - 1] as HTMLImageElement;
    urls.add(image.src);
    await act(async () => fireEvent.error(image));
    expect(view.result.current.errorKind).toBe("error");
    await act(async () => vi.advanceTimersByTimeAsync(delay - 1));
    expect(images).toHaveLength(count);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    count += 1;
    expect(images).toHaveLength(count);
  }
  expect(urls.size).toBe(6);
  await act(async () => fireEvent.load(images[count - 1] as HTMLImageElement));
  expect(view.result.current.imageGeneration).toBeTruthy();
  expect(view.result.current.notice).toBeUndefined();
  await act(async () => vi.advanceTimersByTimeAsync(60000));
  expect(images).toHaveLength(count);
  expect(vi.getTimerCount()).toBe(0);
  act(() => view.result.current.reload());
  await act(async () => vi.advanceTimersByTimeAsync(0));
  await act(async () => fireEvent.error(images[count] as HTMLImageElement));
  expect(vi.getTimerCount()).toBe(1);
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => vi.advanceTimersByTimeAsync(60000));
  expect(images).toHaveLength(count + 1);
});

test("returning to A while B is in flight does not revive retired A content", async () => {
  const a = bound();
  const b = bound("plan.md", "/work/b");
  const view = renderRead(a);
  await readSettled(1);
  let releaseB: (() => void) | undefined;
  answer = (_request, response) => {
    response.setHeader("Content-Type", "text/plain");
    releaseB = () => response.end("B bytes");
  };
  view.rerender({ reference: b, reopen: 0, visible: true, port });
  await waitFor(() => expect(releaseB).toBeTypeOf("function"));
  view.rerender({ reference: a, reopen: 0, visible: true, port });
  expect(view.result.current.content).toBeUndefined();
  recover();
  releaseB?.();
  await readSettled(3);
  expect(view.result.current.content?.text).toBe("recovered bytes");
});
