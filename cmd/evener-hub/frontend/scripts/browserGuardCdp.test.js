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
  collectFontStatusInPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  FONT_POLL_DEADLINE_MS,
  FONT_READY_TIMEOUT_MS,
  forcePseudoStates,
  navigateTo,
  PROBE_ATTEMPT_TIMEOUT_MS,
  STARTUP_DEADLINE_MS,
  waitForFonts,
  waitForHttp,
} from "./browserGuardCdp.mjs";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
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
 * A loopback endpoint that ACCEPTS connections and answers nothing for its
 * first `ignoredRequests` requests, then answers every request after them.
 * That is the shape of a Chrome which has bound - and therefore announced -
 * its DevTools port while the browser thread behind it is still too busy to
 * serve /json/version.
 *
 * What it answers is decided by that COUNT and never by a clock, so the retry
 * these tests pin does not ride on scheduler timing. Requests are counted by
 * parsing complete request heads rather than by counting "data" events or
 * accepted connections: one request split across two TCP reads, or a
 * connection undici opens without sending on, must not read as two attempts
 * and let a poll that never retried pass.
 */
function silentEndpoint(ignoredRequests) {
  let requests = 0;
  let announceRequest;
  let announceClose;
  const firstRequest = new Promise((resolve) => {
    announceRequest = resolve;
  });
  const firstConnectionClosed = new Promise((resolve) => {
    announceClose = resolve;
  });
  // A net.Server stays a live handle on the event loop until every connection
  // it accepted is gone, and an abandoned attempt leaves its socket on this
  // side too - so they are destroyed by hand when the test ends.
  const sockets = new Set();
  const server = createServer((socket) => {
    sockets.add(socket);
    socket.on("close", () => {
      sockets.delete(socket);
      announceClose();
    });
    socket.on("error", () => {});
    let received = "";
    socket.on("data", (chunk) => {
      received += chunk;
      // Every probe is a GET with no body, so the blank line ends the request.
      for (let head = received.indexOf("\r\n\r\n"); head >= 0; head = received.indexOf("\r\n\r\n")) {
        received = received.slice(head + 4);
        requests++;
        announceRequest();
        if (requests > ignoredRequests) {
          socket.end("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}");
        }
      }
    });
  });
  return {
    requestCount: () => requests,
    firstRequest: () => firstRequest,
    firstConnectionClosed: () => firstConnectionClosed,
    listen: () =>
      new Promise((resolve) => {
        server.listen(0, "127.0.0.1", () => resolve(`http://127.0.0.1:${server.address().port}/json/version`));
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
//
// The endpoint is a real socket on purpose: what has to hold is that fetch
// honours the abort and the NEXT request goes out, which no fake transport can
// stand in for. Nothing here is timed, though - the endpoint ignores its first
// request and answers the second, so a slow machine changes when this test
// finishes and never whether it passes.
test("a probe attempt that never answers is abandoned so the poll keeps going", async (context) => {
  const endpoint = silentEndpoint(1);
  context.onTestFinished(() => endpoint.close());
  const url = await endpoint.listen();
  const deadline = createStartupDeadline(3_000);
  context.onTestFinished(() => deadline.clear());
  const before = liveTimers();

  // Before the fix this rejected with the deadline, having spent all 3000ms
  // of it inside the first attempt.
  await waitForHttp(url, "chrome devtools endpoint", () => null, {
    signal: deadline.signal,
    attemptTimeoutMs: 150,
  });

  assert.equal(deadline.signal.aborted, false, "the poll did not finish inside its own startup budget");
  assert.ok(
    endpoint.requestCount() >= 2,
    `the endpoint parsed ${endpoint.requestCount()} request(s): the poll never retried after the first went unanswered`,
  );
  // A leak here shows up as GROWTH - one armed timer per abandoned attempt.
  // The count is not asserted equal because it is process-wide: unrelated
  // timers belonging to the runner expire while this test is running, and a
  // test that fails when one of those happens to fire is not a test.
  assert.ok(
    liveTimers() <= before,
    `live timers went from ${before} to ${liveTimers()}: an attempt's own timer outlived the attempt it was bounding`,
  );
});

// A startup deadline that names neither the phase it died in nor what its
// attempts were doing is why the CI log above could not be read: waiting for
// the stderr announcement and waiting for the endpoint to answer share one
// budget and, until now, one indistinguishable message.
test("the startup deadline says which endpoint it was polling and how often", async (context) => {
  const endpoint = silentEndpoint(Number.MAX_SAFE_INTEGER);
  context.onTestFinished(() => endpoint.close());
  const url = await endpoint.listen();
  const deadline = createStartupDeadline(600);
  context.onTestFinished(() => deadline.clear());

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

// Every test above hands waitForHttp its own attemptTimeoutMs so it can run in
// milliseconds, which means none of them would notice an edit that put the
// PRODUCTION bound back above the deadline it has to retry inside - restoring
// the flake with this whole file still green. This is the only thing holding
// the two constants in a workable relationship.
test("the shipped attempt bound leaves room to retry inside the startup deadline", () => {
  assert.ok(
    PROBE_ATTEMPT_TIMEOUT_MS * 5 <= STARTUP_DEADLINE_MS,
    `one attempt may hold ${PROBE_ATTEMPT_TIMEOUT_MS}ms of a ${STARTUP_DEADLINE_MS}ms phase: too few fit for a poll to be a poll`,
  );
});

// An attempt is not always what ends its own race. A Chrome that exits
// mid-probe settles the failure promise, waitForHttp rejects with it, and the
// request that was racing is still connected - to an endpoint nobody is waiting
// on any more. Clearing the attempt's timer on the way out does not take that
// socket down; only aborting its controller does.
test("a probe abandoned because the browser died does not leave its request running", async (context) => {
  const endpoint = silentEndpoint(Number.MAX_SAFE_INTEGER);
  context.onTestFinished(() => endpoint.close());
  const url = await endpoint.listen();
  const deadline = createStartupDeadline(30_000);
  context.onTestFinished(() => deadline.clear());
  const died = new Error("Chrome exited before DevTools readiness (code 1, signal none)");

  await assert.rejects(
    waitForHttp(url, "chrome devtools endpoint", () => null, {
      signal: deadline.signal,
      // Chrome dies only once its request is on the wire, so this pins the
      // abandoned-mid-flight case and not a race with connection setup.
      failure: endpoint.firstRequest().then(() => died),
      // Far longer than this test can run: the attempt's own bound must not be
      // what eventually closes the socket, or it would pass without the fix.
      attemptTimeoutMs: 600_000,
    }),
    /Chrome exited before DevTools readiness/,
  );

  // Without the abort this never resolves and the test dies of its own timeout.
  await endpoint.firstConnectionClosed();
});

// waitForHttp's total bound is the caller's deadline, and for most of this
// module's life a caller could simply not pass one - 300 attempts at their own
// bound apiece, ten minutes of polling, with nothing to stop it. Callers now
// get a deadline whether they bring one or not.
test("a poll with no caller deadline arms one of its own", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const before = liveTimers();
  const pending = waitForHttp("http://127.0.0.1:1/json/version", "vite dev server", () => null, {
    fetchImpl: () => new Promise(() => {}),
  });
  const rejected = assert.rejects(pending, (error) => {
    assert.match(error.message, new RegExp(`browser startup deadline exceeded after ${STARTUP_DEADLINE_MS}ms`));
    assert.match(error.message, /vite dev server/);
    return true;
  });

  await vi.advanceTimersByTimeAsync(STARTUP_DEADLINE_MS);
  await rejected;
  // The fallback is a timer this module armed itself; leaving it behind would
  // hold a guard's event loop open for the rest of its budget.
  assert.ok(liveTimers() <= before, `live timers went from ${before} to ${liveTimers()}: the fallback deadline outlived its poll`);
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

// The load timer is armed before Page.navigate's timer. If it expires while
// that command is pending, serial awaits leave its rejection unobserved and
// eventually replace the useful load failure with the command's later error.
test("a load timeout rejects navigation while Page.navigate is still pending", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const socket = fakeSocket();
  let announceNavigate;
  const navigateIssued = new Promise((resolve) => {
    announceNavigate = resolve;
  });
  let rejectNavigate;
  const reply = new Promise((_, reject) => {
    rejectNavigate = reject;
  });
  const send = (method) => {
    if (method === "Page.enable") return Promise.resolve({});
    assert.equal(method, "Page.navigate");
    // Model one tick spent sending the command, so the earlier load deadline
    // can fire independently rather than both tripwires firing in one tick.
    vi.advanceTimersByTime(1);
    announceNavigate();
    return reply;
  };
  let outcome;
  const observed = navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/").then(
    () => { outcome = { loaded: true }; },
    (error) => { outcome = { error }; },
  );
  try {
    await navigateIssued;
    await vi.advanceTimersToNextTimerAsync();
    assert.ok(outcome?.error, "the load failure must reach the caller before Page.navigate settles");
    assert.match(outcome.error.message, /timeout calling navigateTo after 30000ms/);
    assert.equal(socket.listenerCount("message"), 0);
  } finally {
    // A later command failure must also be observed, without replacing the
    // failure already delivered to the caller or becoming unhandled.
    rejectNavigate(new Error("late Page.navigate failure"));
    await observed;
    await drainMicrotasks();
  }
  assert.match(outcome.error.message, /timeout calling navigateTo after 30000ms/);
  assert.equal(vi.getTimerCount(), 0);
});

for (const first of ["load", "reply"]) {
  test(`navigateTo waits for both completions when ${first} arrives first`, async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const socket = fakeSocket();
    let announceNavigate;
    const navigateIssued = new Promise((resolve) => {
      announceNavigate = resolve;
    });
    let resolveNavigate;
    const reply = new Promise((resolve) => {
      resolveNavigate = resolve;
    });
    const send = (method) => {
      if (method === "Page.enable") return Promise.resolve({});
      assert.equal(method, "Page.navigate");
      announceNavigate();
      return reply;
    };
    const load = () => socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
    const completeReply = () => resolveNavigate({ result: { frameId: "fixture-frame" } });
    let completed = false;
    const navigation = navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/").then(() => { completed = true; });
    await navigateIssued;
    try {
      (first === "load" ? load : completeReply)();
      await drainMicrotasks();
      assert.equal(completed, false, "one completion must not make the page ready");
    } finally {
      load();
      completeReply();
      await navigation;
    }
    assert.equal(completed, true);
    assert.equal(socket.listenerCount("message"), 0);
    assert.equal(vi.getTimerCount(), 0);
  });
}

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

// waitForFonts runs on one coordinated in-page deadline: the registration
// poll owns the whole budget and the fonts.ready await races the remainder,
// capped by FONT_READY_TIMEOUT_MS. RoboRev caught the two gaps this pins:
// an unbounded fonts.ready await could overrun the evaluate() wrapper's
// 30000ms ceiling and turn every diagnostic into an opaque Runtime.evaluate
// timeout, and a registration budget shorter than the observed >10s
// stylesheet-application windows under load would let the poll expire early
// and report a still-registering document as fontless.

test("the registration poll and the fonts.ready cap together stay under the evaluate() ceiling", () => {
  assert.ok(
    FONT_POLL_DEADLINE_MS > 0 && FONT_READY_TIMEOUT_MS > 0,
    "both in-page budgets must be positive waits, not disabled",
  );
  assert.ok(
    FONT_POLL_DEADLINE_MS > 10000,
    "the registration poll must cover the observed stylesheet-application windows exceeding 10s under load",
  );
  assert.ok(
    FONT_READY_TIMEOUT_MS <= FONT_POLL_DEADLINE_MS,
    "the fonts.ready cap must never exceed the shared deadline",
  );
  assert.ok(
    FONT_POLL_DEADLINE_MS <= 20000,
    `the shared in-page deadline (${FONT_POLL_DEADLINE_MS}ms) bounds every wait in the page, and must leave ` +
      `the 30000ms evaluate() wrapper headroom, or a fontless document dies as an opaque timeout instead ` +
      `of the diagnostic`,
  );
});

test("a fonts.ready that never settles is capped and reported as a stall", async () => {
  const faces = [{ family: "Mono Test", status: "loaded" }];
  vi.stubGlobal("document", {
    fonts: {
      size: faces.length,
      ready: new Promise(() => {}),
      forEach: (callback) => faces.forEach(callback),
    },
    querySelectorAll: () => [],
  });
  const started = Date.now();
  const result = await collectFontStatusInPage({ pollMs: 5000, readyMs: 20 });
  assert.equal(result.stalled, true);
  assert.ok(
    Date.now() - started < 5000,
    "the stalled fonts.ready must be capped at readyMs, not awaited out to the poll deadline",
  );
  assert.deepEqual(result.documents, [{ label: "the top document", faces }]);
});

test("a fontless document exhausts the poll, then still collects and reports", async () => {
  vi.stubGlobal("document", {
    fonts: { size: 0, ready: Promise.resolve(), forEach: () => {} },
    querySelectorAll: () => [],
  });
  const result = await collectFontStatusInPage({ pollMs: 0, readyMs: 20 });
  assert.equal(result.stalled, false);
  assert.deepEqual(result.documents, [{ label: "the top document", faces: [] }]);
});

test("a fontless document whose fonts.ready hangs reports the stall immediately", async () => {
  vi.stubGlobal("document", {
    fonts: { size: 0, ready: new Promise(() => {}), forEach: () => {} },
    querySelectorAll: () => [],
  });
  const started = Date.now();
  const result = await collectFontStatusInPage({ pollMs: 0, readyMs: 5000 });
  assert.equal(result.stalled, true);
  assert.ok(
    Date.now() - started < 1000,
    "an exhausted budget must cap fonts.ready at the zero remainder, not at readyMs",
  );
  assert.deepEqual(result.documents, [{ label: "the top document", faces: [] }]);
});

test("the registration poll waits out a font that arrives mid-poll", async () => {
  const face = { family: "Mono Test", status: "loaded" };
  const registeredAt = Date.now() + 100;
  const isRegistered = () => Date.now() >= registeredAt;
  vi.stubGlobal("document", {
    fonts: {
      // An empty set's fonts.ready settles instantly - the real-world trap this
      // poll exists for - so only waiting for the transition can see the face;
      // collecting immediately would read an empty set and report "fontless".
      get size() {
        return isRegistered() ? 1 : 0;
      },
      ready: Promise.resolve(),
      forEach: (callback) => {
        if (isRegistered()) callback(face);
      },
    },
    querySelectorAll: () => [],
  });
  const result = await collectFontStatusInPage({ pollMs: 1000, readyMs: 20 });
  assert.equal(result.stalled, false);
  assert.deepEqual(result.documents, [{ label: "the top document", faces: [face] }]);
});

test("waitForFonts reports a stalled font load as an environment problem", async () => {
  const send = async () => ({
    result: {
      result: { value: { stalled: true, documents: [{ label: "the top document", faces: [] }] } },
    },
  });
  await assert.rejects(
    waitForFonts(send),
    /environment problem, not a test case failure.*font load is stalled/s,
  );
});

test("waitForFonts passes settled fonts and still names a fontless document", async () => {
  const settled = {
    stalled: false,
    documents: [{ label: "the top document", faces: [{ family: "Mono Test", status: "loaded" }] }],
  };
  const sendSettled = async () => ({ result: { result: { value: settled } } });
  await waitForFonts(sendSettled);

  const fontless = { stalled: false, documents: [{ label: "the top document", faces: [] }] };
  const sendFontless = async () => ({ result: { result: { value: fontless } } });
  await assert.rejects(waitForFonts(sendFontless), /declares no web fonts/);
});
