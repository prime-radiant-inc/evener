// @vitest-environment node

// The WIRE half of the guards' plumbing (browserGuardCdp.mjs). Everything here
// runs against a fake `send` and a fake socket: the module's contract with
// Chrome is CDP request/response shapes, and none of these cases needs a
// browser to hold it to that contract.
//
// What it exists to pin is the resource discipline around every CDP call, which
// is invisible to a guard's PASS/FAIL verdict and therefore rots silently: a
// 30-second timeout timer left armed after the call it was guarding already
// answered, and a "message" listener left on a socket the guards reuse across
// cases.

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, test, vi } from "vitest";

import {
  applyViewport,
  clearViewportOverride,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  forcePseudoStates,
  navigateTo,
  waitForHttp,
} from "./browserGuardCdp.mjs";

afterEach(() => {
  vi.useRealTimers();
});

test("preserves the announced endpoint host when building HTTP URLs", () => {
  assert.equal(
    devtoolsHttpURL({ url: "ws://[::1]:43210/devtools/browser/test" }, "/json/version"),
    "http://[::1]:43210/json/version",
  );
  assert.equal(
    devtoolsHttpURL({ url: "ws://localhost:43211/devtools/browser/test" }, "/json/list"),
    "http://localhost:43211/json/list",
  );
  assert.equal(
    devtoolsHttpURL({ url: "ws://127.0.0.1:80/devtools/browser/test", host: "127.0.0.1", port: 80 }, "/json/version"),
    "http://127.0.0.1:80/json/version",
  );
});

test("one startup deadline aborts the pending HTTP readiness phase", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const deadline = createStartupDeadline();
  const pending = waitForHttp("http://127.0.0.1:1/json/version", "chrome", () => null, {
    signal: deadline.signal,
  });

  vi.advanceTimersByTime(30_000);
  await assert.rejects(pending, /browser startup deadline exceeded after 30000ms/);
  deadline.clear();
});

/**
 * A loopback endpoint that ACCEPTS connections and answers nothing until
 * `silentMs` have passed since it started listening, then answers every
 * request. That is the shape of a Chrome which has bound - and therefore
 * announced - its DevTools port while the browser thread behind it is still
 * too busy to serve /json/version.
 */
function silentEndpoint(silentMs) {
  let listeningAt = 0;
  let requests = 0;
  // An abandoned attempt leaves its socket behind on this side too. They are
  // destroyed by hand at the end of the test: a net.Server stays a live handle
  // on the event loop until every connection it accepted is gone, and a test
  // file that never drains is a hang, not a failure.
  const sockets = new Set();
  const server = createServer((socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.on("error", () => {});
    socket.on("data", () => {
      requests++;
      if (Date.now() - listeningAt < silentMs) return;
      socket.end("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}");
    });
  });
  return {
    requestCount: () => requests,
    listen: () =>
      new Promise((resolve) => {
        server.listen(0, "127.0.0.1", () => {
          listeningAt = Date.now();
          resolve(`http://127.0.0.1:${server.address().port}/json/version`);
        });
      }),
    close: () => {
      for (const socket of sockets) socket.destroy();
      server.close();
    },
  };
}

// The flake this pins (run 34257184696): Chrome announced its DevTools endpoint
// and the guard still died on "browser startup deadline exceeded after
// 30000ms". A probe attempt had no bound of its own, so the ONE request that
// landed while the browser was still unresponsive held the whole startup
// budget open - fetch does not give up on a connected socket - and the
// poll-every-100ms loop below it never ran a second time. The loop only ever
// advanced when an attempt failed FAST, which after the announcement it cannot:
// the port is bound, so the connection is accepted and then simply ignored.
test("a probe attempt that never answers is abandoned so the poll keeps going", async (context) => {
  const endpoint = silentEndpoint(400);
  context.after(() => endpoint.close());
  const url = await endpoint.listen();
  const deadline = createStartupDeadline(3_000);
  context.after(() => deadline.clear());
  const before = liveTimers();
  const started = Date.now();

  await waitForHttp(url, "chrome devtools endpoint", () => null, {
    signal: deadline.signal,
    attemptTimeoutMs: 150,
  });

  const elapsed = Date.now() - started;
  assert.ok(elapsed < 3_000, `the wait took ${elapsed}ms: it spent the whole startup deadline inside one attempt`);
  assert.ok(endpoint.requestCount() > 1, `only ${endpoint.requestCount()} request was ever sent: the poll never retried`);
  assert.equal(liveTimers(), before, "an attempt's own timer outlived the attempt it was bounding");
});

// A startup deadline that names neither the phase it died in nor what its
// attempts were doing is why the CI log above could not be read: waiting for
// the stderr announcement and waiting for the endpoint to answer share one
// budget and, until now, one indistinguishable message.
test("the startup deadline says which endpoint it was polling and how often", async (context) => {
  const endpoint = silentEndpoint(Number.MAX_SAFE_INTEGER);
  context.after(() => endpoint.close());
  const url = await endpoint.listen();
  const deadline = createStartupDeadline(600);
  context.after(() => deadline.clear());

  await assert.rejects(
    waitForHttp(url, "chrome devtools endpoint", () => null, { signal: deadline.signal, attemptTimeoutMs: 100 }),
    (error) => {
      assert.match(error.message, /browser startup deadline exceeded after 600ms/);
      assert.match(error.message, /chrome devtools endpoint/);
      assert.match(error.message, new RegExp(`polling ${url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
      assert.match(error.message, /after \d+ attempts?/);
      return true;
    },
  );
});

const cdpModuleUrl = new URL("./browserGuardCdp.mjs", import.meta.url).href;

/** Timers this process is currently holding the event loop open for. */
function liveTimers() {
  return process.getActiveResourcesInfo().filter((resource) => resource === "Timeout").length;
}

/**
 * A stand-in for the CDP WebSocket: the guards only ever use it as an event
 * target, and navigateTo's contract is about which listeners it leaves behind.
 */
function fakeSocket() {
  const listeners = new Map();
  return {
    listenerCount(type) {
      return (listeners.get(type) ?? []).length;
    },
    addEventListener(type, handler) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(handler);
    },
    removeEventListener(type, handler) {
      const registered = listeners.get(type) ?? [];
      const at = registered.indexOf(handler);
      if (at >= 0) registered.splice(at, 1);
    },
    dispatch(type, event) {
      for (const handler of [...(listeners.get(type) ?? [])]) handler(event);
    },
  };
}

/**
 * Answer each CDP method with the response shape browserGuardCdp.mjs reads out
 * of it. `onNavigate` is how a case says what the page does once it is asked to
 * navigate: the default loads, and a case can instead never load or make the
 * navigate itself fail.
 */
function fakeSend(socket, { onNavigate } = {}) {
  const fireLoadEvent = () =>
    socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
  const navigated = onNavigate ?? fireLoadEvent;
  return async (method) => {
    switch (method) {
      case "Page.navigate":
        navigated();
        return {};
      case "DOM.getDocument":
        return { result: { root: { nodeId: 1 } } };
      case "DOM.querySelector":
        return { result: { nodeId: 2 } };
      default:
        return { result: { result: { value: 42 } } };
    }
  };
}

/** Run every already-queued microtask. setImmediate is not a mocked timer. */
function drainMicrotasks() {
  return new Promise((resolve) => setImmediate(resolve));
}

// The bug this pins: withTimeout raced the call against a 30-second timer and
// then walked away from the loser. A pending timer is a live handle on the
// event loop, so nothing was leaked in the memory sense -- it was worse. Every
// guard ends by setting process.exitCode rather than calling process.exit(), so
// the process only leaves when the loop drains, and it could not drain until
// the last timer expired. Every GREEN run paid the full 30 seconds.
test("no CDP call leaves a timeout timer holding the event loop", async () => {
  const socket = fakeSocket();
  const send = fakeSend(socket);
  const before = liveTimers();

  await navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/");
  await evaluate(send, "1 + 41");
  await applyViewport(send, { width: 800, height: 600 });
  await clearViewportOverride(send);
  await forcePseudoStates(send, [{ selector: "#focusable", pseudoClasses: ["focus-visible"] }]);

  assert.equal(liveTimers(), before);
});

// The measurement above is in-process and cheap, but it is also one API call
// away from the thing that actually hurt: the guard process not exiting. This
// runs the real shape -- a script that does its CDP work and then falls off the
// end with process.exitCode set, exactly as layoutguard, overflowguard and
// spawnguard's run.mjs do -- and holds it to the wall clock. Before the fix
// this script took 30.044 seconds; spawnguard took 31.102 against 1.451
// seconds of work. The bound is deliberately loose: it is here to catch a
// 30-second hang, not to police startup jitter.
// Let the elapsed-time assertion judge the child after it exits, including
// the 30-second timer-leak case, without a shorter runner deadline.
test("a guard-shaped script exits as soon as its work is done", { timeout: 0 }, async (context) => {
  const dir = mkdtempSync(path.join(tmpdir(), "browser-guard-cdp-test-"));
  context.onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
  const script = path.join(dir, "guardShaped.mjs");
  writeFileSync(
    script,
    `import { evaluate } from ${JSON.stringify(cdpModuleUrl)};\n` +
      `const send = async () => ({ result: { result: { value: 42 } } });\n` +
      `if (await evaluate(send, "1 + 41") !== 42) process.exitCode = 1;\n` +
      `// No process.exit() here, on purpose: that is what the guards do.\n`,
  );

  const started = Date.now();
  const child = spawn(process.execPath, [script], { stdio: ["ignore", "ignore", "pipe"] });
  let stderr = "";
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  const code = await new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("exit", resolve);
  });
  const elapsed = Date.now() - started;

  assert.equal(code, 0, `guard-shaped script failed: ${stderr}`);
  assert.ok(
    elapsed < 10_000,
    `the script took ${elapsed}ms: a settled CDP call is still holding a timeout timer, so the guard cannot exit until it fires`,
  );
});

test("navigateTo stops listening for the load event once the page has loaded", async () => {
  const socket = fakeSocket();
  await navigateTo({ ws: socket, send: fakeSend(socket) }, "http://127.0.0.1:65535/");
  assert.equal(socket.listenerCount("message"), 0);
});

// The timeout path is where the listener used to survive: the load event never
// fires, so the handler that removed itself on load never ran, and it stayed on
// a socket the guards reuse for every later case -- parsing every CDP message
// that arrives for the rest of the run. Mock timers stand in for the 30 seconds
// so this stays a millisecond test.
test("navigateTo stops listening when the load event never arrives", async () => {
  const socket = fakeSocket();
  let announceNavigate;
  const navigateIssued = new Promise((resolve) => {
    announceNavigate = resolve;
  });
  const send = fakeSend(socket, { onNavigate: announceNavigate });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });

  const navigation = navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/");
  const rejected = assert.rejects(navigation, /timeout calling navigateTo after 30000ms/);
  // The clock may only jump once every OTHER timeout in flight has been cleared
  // by the call it was guarding, or this would report whichever of those fired
  // first instead of the load wait under test. Ticking before the load wait is
  // even armed is worse: nothing would ever fire it and this would hang.
  await navigateIssued;
  await drainMicrotasks();
  vi.advanceTimersByTime(30_000);
  await rejected;

  assert.equal(socket.listenerCount("message"), 0);
});

// A CDP error on the navigate itself means the page will never load, so the
// load wait has to be settled by hand. Left pending it holds the guard open for
// its own 30 seconds and then rejects with nobody awaiting it -- an unhandled
// rejection, which Node turns into a crash long after the real error below was
// the whole diagnosis.
test("a failed navigate takes the load wait down with it", async () => {
  const socket = fakeSocket();
  const send = fakeSend(socket, {
    onNavigate: () => {
      throw new Error("Page.navigate: {\"code\":-32000,\"message\":\"Cannot navigate to invalid URL\"}");
    },
  });
  const before = liveTimers();

  await assert.rejects(navigateTo({ ws: socket, send }, "not-a-url"), /Cannot navigate to invalid URL/);

  assert.equal(socket.listenerCount("message"), 0);
  assert.equal(liveTimers(), before);
});
