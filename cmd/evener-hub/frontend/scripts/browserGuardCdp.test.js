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
import os, { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, test, vi } from "vitest";

import {
  applyViewport,
  BOOT_RETRY_LIMIT,
  CDP_EVALUATE_TRIPWIRE_MS,
  clearViewportOverride,
  collectFontStatusInPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  FONT_POLL_DEADLINE_MS,
  FONT_READY_TIMEOUT_MS,
  forcePseudoStates,
  harnessStylesheetsLoadedInPage,
  navigateTo,
  PROBE_ATTEMPT_TIMEOUT_MS,
  STARTUP_DEADLINE_CAP_MS,
  STARTUP_DEADLINE_MS,
  startupDeadlineMs,
  waitForFonts,
  waitForHttp,
} from "./browserGuardCdp.mjs";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
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
  // An explicit budget: this pins the deadline MECHANISM at a known floor, not
  // the (now load-aware) default, which would otherwise vary with the runner.
  const deadline = createStartupDeadline(30_000);
  const pending = waitForHttp("http://127.0.0.1:1/json/version", "chrome", () => null, {
    signal: deadline.signal,
  });

  vi.advanceTimersByTime(30_000);
  await assert.rejects(pending, /browser startup deadline exceeded after 30000ms/);
  deadline.clear();
});

// The startup deadline is an ENVIRONMENT TRIPWIRE, not an assertion about
// startup time: a machine that is demonstrably oversubscribed must not read a
// correct-but-slow startup as broken. Before this it was a fixed floor
// regardless of load, which is how runs 33803871850 and 34257184696 died on a
// cold, contended runner. These pin the load-aware default.
test("a loaded machine widens the default startup deadline past the fixed floor", async () => {
  vi.spyOn(os, "loadavg").mockReturnValue([16, 16, 16]);
  vi.spyOn(os, "availableParallelism").mockReturnValue(16);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const deadline = createStartupDeadline();
  let aborted = false;
  deadline.signal.addEventListener("abort", () => {
    aborted = true;
  });

  await vi.advanceTimersByTimeAsync(STARTUP_DEADLINE_MS);
  assert.equal(
    aborted,
    false,
    `the startup tripwire still fired at the fixed ${STARTUP_DEADLINE_MS}ms floor on a loaded machine`,
  );
  deadline.clear();
});

// Widening the tripwire must not make it infinite: a startup that never comes
// up still fails, and it fails with the budget it actually had.
test("a loaded machine's widened deadline still fires at the ceiling", async () => {
  vi.spyOn(os, "loadavg").mockReturnValue([32, 32, 32]);
  vi.spyOn(os, "availableParallelism").mockReturnValue(16);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const deadline = createStartupDeadline();
  let reason = null;
  deadline.signal.addEventListener("abort", () => {
    reason = deadline.signal.reason;
  });

  await vi.advanceTimersByTimeAsync(120_000);
  assert.ok(reason, "the widened deadline never fired");
  assert.match(String(reason?.message), /browser startup deadline exceeded after 120000ms/);
  deadline.clear();
});

test("startupDeadlineMs interpolates from the floor to the ceiling with load per core", () => {
  assert.equal(startupDeadlineMs({ load1: 0, cores: 16 }), STARTUP_DEADLINE_MS);
  assert.equal(startupDeadlineMs({ load1: 8, cores: 16 }), 75_000);
  assert.equal(startupDeadlineMs({ load1: 16, cores: 16 }), STARTUP_DEADLINE_CAP_MS);
  assert.equal(startupDeadlineMs({ load1: 64, cores: 16 }), STARTUP_DEADLINE_CAP_MS);
});

test("startupDeadlineMs treats an unreadable load or core count as idle", () => {
  assert.equal(startupDeadlineMs({ load1: Number.NaN, cores: 16 }), STARTUP_DEADLINE_MS);
  assert.equal(startupDeadlineMs({ load1: -1, cores: 16 }), STARTUP_DEADLINE_MS);
  assert.equal(startupDeadlineMs({ load1: 8, cores: 0 }), STARTUP_DEADLINE_MS);
  assert.equal(startupDeadlineMs({ load1: Number.NaN, cores: Number.NaN }), STARTUP_DEADLINE_MS);
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
  // An idle machine so the load-aware default is deterministic: it stays at the
  // floor. (loadavg/availableParallelism are read at deadline-creation time,
  // which is here.)
  vi.spyOn(os, "loadavg").mockReturnValue([0, 0, 0]);
  vi.spyOn(os, "availableParallelism").mockReturnValue(16);
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
  assert.ok(
    liveTimers() <= before,
    `live timers went from ${before} to ${liveTimers()}: the fallback deadline outlived its poll`,
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
  const fireLoadEvent = () => socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
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
    () => {
      outcome = { loaded: true };
    },
    (error) => {
      outcome = { error };
    },
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
    const navigation = navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/").then(() => {
      completed = true;
    });
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
      throw new Error('Page.navigate: {"code":-32000,"message":"Cannot navigate to invalid URL"}');
    },
  });
  const before = liveTimers();

  await assert.rejects(navigateTo({ ws: socket, send }, "not-a-url"), /Cannot navigate to invalid URL/);

  assert.equal(socket.listenerCount("message"), 0);
  assert.equal(liveTimers(), before);
});

// The unbooted-page seam. A transient network change on the host (observed:
// bursts of net::ERR_NETWORK_CHANGED, including on 127.0.0.1, when a
// Tailscale interface flaps) kills the Vite dev-server module burst mid-boot.
// The page still parses and fires its load event - readyState complete, both
// script tags in the DOM - but the harness entry module never runs, so its
// boot marker is missing and no injected stylesheet arrives. The guards used
// to walk straight into waitForFonts on that dead page and misreport the
// environment flake as "declares no web fonts".
//
// navigateTo's boot options are the seam every guard shares: a page whose
// harness never booted is re-navigated a bounded number of times, and a page
// that never recovers fails with the boot cause instead of the fonts check.
// No test here needs a real browser, a real network, or a real flap: the fakes
// decide per call whether the page booted.

test("navigateTo re-navigates an unbooted harness page and recovers when the retry boots", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  let bootChecks = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        bootChecks++;
        // The first navigation lands on the dead page (the entry module never
        // ran); the re-navigation boots.
        return { result: { result: { value: bootChecks > 1 } } };
      default:
        return {};
    }
  };
  const retryNotes = [];
  const noteError = vi.spyOn(console, "error").mockImplementation((...args) => {
    retryNotes.push(args.join(" "));
  });

  const before = liveTimers();
  try {
    await navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
      bootExpression: "typeof window.settledShell !== 'undefined'",
      bootLabel: "the shellguard entry global window.settledShell",
      retryDelayMs: 0,
    });
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 2, "a page whose harness never booted must be re-navigated");
  assert.equal(bootChecks, 2, "every navigation must be followed by exactly one boot check");
  assert.ok(
    retryNotes.some((note) => note.includes("never booted")),
    "a recovered flake must leave a line in the guard log, not pass silently",
  );
  assert.equal(socket.listenerCount("message"), 0);
  assert.ok(liveTimers() <= before, "the recovery must not leave a timer holding the event loop");
});

test("navigateTo fails with the boot cause - never the font check - when the retries are exhausted", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        // The commit and boot burst land after the navigate response binds this
        // navigation's loaderId to the attempt; the failures belong to THIS
        // attempt's document, the document live once the frame has navigated.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          // The burst dies on EVERY navigation: the page still loads, but the
          // module and stylesheet requests fail with the network error. Evidence
          // is scoped per attempt, so the environment framing holds only while
          // the FINAL attempt itself died on the wire (the stale-evidence test
          // below pins the other side).
          for (const type of ["Script", "Stylesheet"]) {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { type, errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
          }
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  const before = liveTimers();
  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /never booted/);
        assert.match(error.message, /overflowharness\.html/);
        assert.match(error.message, /window\.settled/);
        // The captured network failures must reach the diagnosis.
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        // The dead page used to misreport as an empty font set; that
        // diagnostic must never be the terminal error for an unbooted page.
        assert.doesNotMatch(error.message, /font/i);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT, "the retry budget must be bounded");
  assert.equal(socket.listenerCount("message"), 0);
  assert.ok(liveTimers() <= before, "the exhausted retries must not leave a timer holding the event loop");
});

test("a booted page is not re-navigated and its empty font set still fails the fonts check", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const evaluateResults = [
    { result: { result: { value: true } } }, // the boot check: the harness ran
    { result: { result: { value: { stalled: false, documents: [{ label: "the top document", faces: [] }] } } } },
  ];
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return evaluateResults.shift();
      default:
        return {};
    }
  };

  await navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/generated/editorial-transcript-360/harness.html", {
    bootExpression: "typeof window.measure !== 'undefined'",
    bootLabel: "the case harness global window.measure",
    retryDelayMs: 0,
  });
  assert.equal(navigations, 1, "a booted page must not be re-navigated");

  // kata e4sh stands: an empty font set on a page that DID boot is still a
  // failure, and still the fonts check's own diagnostic.
  await assert.rejects(waitForFonts(send), /declares no web fonts/);
  assert.equal(socket.listenerCount("message"), 0);
});

// Attribution (review M1): the marker can be missing without anything dying
// on the wire - a harness entry whose module-init path throws at top level, a
// bad script href, or a broken import graph all leave the boot global unset
// and report no Network.loadingFailed event. That failure is deterministic
// code, and blaming a network flake would invite retrying it instead of
// investigating it, so the environment framing must be earned: only a boot
// death that actually captured failed requests may call itself one.
test("navigateTo blames the harness entry, not the environment, when no request failure was captured", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  const before = liveTimers();
  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /spawnguard\.html/);
        assert.match(error.message, /window\.settledSpawn/);
        // No request died on the wire, so nothing may claim an environment
        // flake: the diagnostic must point at the harness entry instead.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.doesNotMatch(error.message, /transient network change/);
        assert.match(error.message, /harness entry/);
        assert.match(error.message, /No request failures were captured/);
        assert.doesNotMatch(error.message, /font/i);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT, "the bounded retry budget is unchanged");
  assert.equal(socket.listenerCount("message"), 0);
  assert.ok(liveTimers() <= before);
});

// Evidence scoping (review round 3): the loading-failure window is per
// ATTEMPT. A network flap that killed attempt 1 must not launder a
// deterministic harness bug that killed the FINAL attempt as an environment
// problem - the terminal diagnosis describes the boot death that decided the
// run, and only the final attempt's failures are that attempt's evidence.
test("evidence from an earlier attempt does not outlive it: only the final attempt names the cause", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        // Attempt 1 dies in the flap; every re-navigation lands on a page
        // whose harness never boots for a deterministic reason and reports
        // nothing on the wire.
        if (navigations === 1) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  const before = liveTimers();
  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /harness boot failure - likely a test case regression/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
  assert.ok(liveTimers() <= before);
});

// Abort filtering (review round 3): a re-navigation ABORTS the previous
// attempt's in-flight requests, so canceled / net::ERR_ABORTED arrivals are
// the seam's own doing, and a harness aborting its own request is equally
// deterministic. Neither may be recorded as wire evidence - not counted, not
// listed.
test("a canceled or aborted request is never counted as wire evidence", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        // Both flavors arrive on EVERY attempt, including the final one: the
        // first is canceled by the re-navigation (canceled: true), the second
        // aborts outright (net::ERR_ABORTED).
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Network.loadingFailed",
            params: { type: "Script", errorText: "net::ERR_FAILED", canceled: true },
          }),
        });
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Network.loadingFailed",
            params: { type: "Script", errorText: "net::ERR_ABORTED" },
          }),
        });
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        // Dropped whole: neither flavor may appear in the diagnosis as if it
        // were evidence.
        assert.doesNotMatch(error.message, /ERR_ABORTED/);
        assert.doesNotMatch(error.message, /ERR_FAILED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// Classification (review round 3): a request failure that is NOT a recognized
// network-state change - a blocked request, an ERR_FAILED resource the harness
// itself caused - is deterministic evidence about the harness. It is included
// in the diagnosis so the reader can judge it, but it never flips the
// attribution: only recognized network-change failures earn the environment
// framing.
test("a deterministic request failure is reported but never flips the attribution", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        // The commit (after the response binds its loaderId) precedes the
        // request's death: the failure belongs to this attempt's document, not
        // the one that was live beforehand.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { type: "Script", errorText: "net::ERR_FAILED" },
            }),
          });
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/transcriptscrollguard.html", {
        bootExpression: "typeof window.waitForTranscriptSettled !== 'undefined'",
        bootLabel: "the transcriptscrollguard entry global window.waitForTranscriptSettled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /harness boot failure - likely a test case regression/);
        // Included for the reader, so the diagnosis is not blind to what the
        // wire actually said.
        assert.match(error.message, /net::ERR_FAILED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// Mixed evidence (review round 4, F1): a final window that captured BOTH a
// recognized network-change failure AND a deterministic one cannot be named
// from the wire alone. Issuing the environment verdict would silently discard
// the deterministic evidence; issuing the regression verdict would discard the
// flap. The diagnosis must report the window as inconclusive and include BOTH
// summaries so the reader can judge.
test("a mixed final window is inconclusive and reports both kinds of evidence", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        // The commit lands after the response binds its loaderId; both failures
        // are the committed document's own boot burst.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          for (const errorText of ["net::ERR_NETWORK_CHANGED", "net::ERR_FAILED"]) {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: `req-${navigations}-${errorText}`, type: "Script", errorText },
              }),
            });
          }
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /inconclusive/);
        // Neither verdict may be issued as fact.
        assert.doesNotMatch(error.message, /environment problem, not a test case failure/);
        assert.doesNotMatch(error.message, /an environment flake, not a test case regression/);
        // Both kinds of evidence are included, so the reader can judge.
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        assert.match(error.message, /net::ERR_FAILED/);
        assert.match(error.message, /network-change failures/);
        assert.match(error.message, /deterministic failures/);
        assert.doesNotMatch(error.message, /font/i);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// Request-ownership scoping (review round 4, F2): evidence is scoped by the
// attempt that OWNED the request, not by arrival time. A flap death for a
// request attempt 1 sent, delivered late - after the final attempt started -
// belongs to attempt 1 and must not flip the final attribution.
test("a late failure for an earlier attempt's request does not flip the final attribution", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  let enables = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.enable":
        enables++;
        // Attempt 1's request dies on the wire, but the failure is delivered
        // late - inside the FINAL attempt's window, in the gap before its own
        // requests exist (so the abort filter cannot cover it).
        if (enables === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-early", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return {};
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        if (navigations === 1) {
          // Attempt 1 sends the request whose death arrives late.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-early", loaderId: "loader-1" },
            }),
          });
        }
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /harness boot failure - likely a test case regression/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The unmatched-requestId edge is a decided rule, not an accident: a
// loadingFailed whose requestWillBeSent was never seen has no request
// identity, so it is anchored to the last COMMITTED navigation - the document
// that is live when the failure arrives. In the gap before a new navigation
// commits that is still the PREVIOUS document (the gap tests below pin
// that side); once the final navigation has committed, a death arriving with
// no seen send counts for that final attempt.
test("a loadingFailed whose send was never seen counts for the navigation that is live when it arrives", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        // The final navigation has COMMITTED by the time its boot check runs,
        // so its document is the live one an unseen failure arriving now
        // belongs to.
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-unseen", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
        bootExpression: "typeof window.settledShell !== 'undefined'",
        bootLabel: "the shellguard entry global window.settledShell",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The navigate gap (review round 5): the attempt counter advances at loop
// top, BEFORE the new navigation's response commits - and in that gap the
// OLD document still owns the socket and can still emit events. Tagging by
// the counter files them under the new attempt; tagging is by the event's
// OWN loaderId, so a request belongs to the document that actually sent it
// and its death cannot land in the final attempt's bucket.
test("an old document's request emitted in the navigate gap is not filed under the new attempt", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // The counter already points at the final attempt, but this
          // navigation's response has not landed: the live document is still
          // attempt 2's (loader-2), and it emits a request here.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-old-doc", loaderId: `loader-${navigations - 1}` },
            }),
          });
        } else {
          // Every earlier navigation commits, naming its loaderId.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
        }
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-old-doc", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
          // The final navigation commits only now.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
        }
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The death belongs to attempt 2's document; the final attempt's
        // bucket is empty, so the final diagnosis is the regression framing.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

test("an unseen failure arriving in the navigate gap belongs to the previous navigation", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations !== 1 + BOOT_RETRY_LIMIT) {
          // The earlier navigations commit, so their documents own the gap.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
        }
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // In the gap: the counter already says the final attempt, but the
          // final navigation has not committed - the live document is still
          // attempt 2's. An unseen death arriving here belongs to that
          // document, not to the final attempt the counter names.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-unseen-gap", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
          // The final navigation commits only now.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
        }
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A load event is NOT correlated with a navigation: a delayed Page.loadEventFired
// from the old document can arrive just as the new navigation starts. Treating
// it (plus the Page.navigate response) as proof of commit would advance the
// live-document identity before the new frame has navigated, so the old
// document's still-live requests in that window would be filed under the
// incoming attempt - stale evidence in the terminal verdict.
test("a delayed load event from the previous document does not commit the incoming navigation", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // A delayed load event from the PREVIOUS document arrives as this
          // navigation starts. The new frame does not commit yet.
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          // After the navigate response resolves - and so after any
          // uncorrelated load-fallback would have run - the previous document
          // emits a request that dies; the new frame commits only afterwards.
          setTimeout(() => {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.requestWillBeSent",
                params: { requestId: "req-delayed" },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: "req-delayed", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Page.frameNavigated",
                params: { frame: { loaderId: `loader-${navigations}` } },
              }),
            });
          }, 0);
          return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
        }
        // The commit lands after the navigate response binds this navigation's
        // loaderId to the attempt - so the pre-commit failure above resolves
        // through it once the frame commits.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        }, 0);
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        // Hold the boot check open past the delayed request above so it lands
        // before the verdict.
        if (navigations === 1 + BOOT_RETRY_LIMIT) await new Promise((resolve) => setTimeout(resolve, 10));
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The delayed request belongs to the previous document; the final
        // attempt severed nothing, so this is a harness regression.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// Commit observation must outlive one navigation's load wait. When a stale load
// event from the previous document resolves the wait, the navigation can still
// commit afterwards; a per-navigation listener would already be gone and the
// commit lost, leaving the new document's own loaderId unmapped and its wire
// death filed under the previous attempt. The commit listener lives for the
// whole window, so the final attempt's own failure still counts for it.
test("a commit that arrives after a stale load still names the final attempt's document", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // A delayed load event from the previous document ends the wait for
          // this navigation's load; the new frame commits only afterwards.
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          setTimeout(() => {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.requestWillBeSent",
                params: { requestId: "req-final", loaderId: `loader-${navigations}` },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: "req-final", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Page.frameNavigated",
                params: { frame: { loaderId: `loader-${navigations}` } },
              }),
            });
          }, 0);
          return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
        }
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        if (navigations === 1 + BOOT_RETRY_LIMIT) await new Promise((resolve) => setTimeout(resolve, 10));
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A navigation whose frame never commits cannot settle its loaderId. Retry
// boundaries must not move attribution: the failure stays with the attempt that
// owned the live document when the request was SENT, never the retry that
// happens to be in flight when it arrives.
test("an uncommitted navigation's loaderId failure stays with its send-time attempt", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations !== 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: `loader-${navigations}` } },
            }),
          });
        } else {
          // The final navigation's own main resource dies and the frame NEVER
          // commits: its loaderId has no commit to settle it.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-nocommit", loaderId: `loader-${navigations}` },
            }),
          });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-nocommit", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The uncommitted loaderId belongs to the previous committed document's
        // attempt, so the final attempt's window is empty.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A commit must bind to the navigation that named its loaderId, never to the
// ambient retry counter. When a commit is delivered late - after attempts++ has
// already moved on - recording it against the counter would map the previous
// document's loaderId to the incoming retry, and its failure would then land in
// the final verdict as environment evidence. The Page.navigate response binds
// the loaderId to its attempt, so the late commit still names its own.
test("a commit delivered after the counter advanced still names its own navigation", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: "loader-1" } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          return { result: { frameId: "fixture-frame", loaderId: "loader-1" } };
        }
        if (navigations === 2) {
          // This navigation's load event resolves its wait before it commits;
          // its request (loader-2) is the one whose death arrives late.
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-late", loaderId: "loader-2" },
            }),
          });
          return { result: { frameId: "fixture-frame", loaderId: "loader-2" } };
        }
        // The final navigation's window: attempt 2's commit only NOW arrives,
        // after the counter has advanced to this final attempt - and the late
        // failure for attempt 2's request arrives with it.
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: "loader-2" } },
          }),
        });
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Network.loadingFailed",
            params: { requestId: "req-late", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
          }),
        });
        // This navigation's own commit and load land after its response, which
        // binds loader-3 to the final attempt.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: "loader-3" } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        }, 0);
        return { result: { frameId: "fixture-frame", loaderId: "loader-3" } };
      case "Runtime.evaluate":
        if (navigations === 1 + BOOT_RETRY_LIMIT) await new Promise((resolve) => setTimeout(resolve, 10));
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The late failure belongs to attempt 2; the final attempt captured
        // nothing of its own, so the diagnosis is the regression framing.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A top document whose navigate response reports no loaderId has no identity to
// bind, so its commit must take the navigation in flight - never a shared
// "null" entry left by an earlier loaderless navigation, which would bind the
// just-committed document to a stale attempt and file its failure outside the
// final window.
test("a loaderless navigation commits against the attempt in flight, not an earlier one", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        // The commit lands after the response queues this loaderless
        // navigation's attempt; it must take THAT attempt, never a shared entry
        // left by an earlier loaderless navigation.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: { id: `top-${navigations}` } } }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          if (navigations === 1 + BOOT_RETRY_LIMIT) {
            // An unseen death in the final attempt's own document.
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
          }
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
        bootExpression: "typeof window.settledShell !== 'undefined'",
        bootLabel: "the shellguard entry global window.settledShell",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The final attempt's own document owns it, so the environment framing
        // is earned.
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A subframe commit keeps the SEND-time attempt its own request bound it to. A
// frame id is stable across navigations, so keying on it (or on the ambient top
// document) would let a delayed iframe Page.frameNavigated from an earlier
// document resolve to the newest attempt; the failure could then be promoted
// into the final verdict. This test reuses ONE top frame id to pin that.
test("a delayed subframe commit keeps the send-time attempt, not the current retry", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { id: "top", loaderId: "loader-1" } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          // Attempt 1's document creates an iframe whose commit is delayed.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-sub", loaderId: "loader-sub" },
            }),
          });
          return { result: { frameId: "fixture-frame", loaderId: "loader-1" } };
        }
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { id: "top", loaderId: `loader-${navigations}` } },
            }),
          });
          if (navigations === 1 + BOOT_RETRY_LIMIT) {
            // The long-delayed iframe commit and the iframe resource's death
            // both arrive in the final window; they belong to attempt 1.
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Page.frameNavigated",
                params: { frame: { id: "sub-1", parentId: "top", loaderId: "loader-sub" } },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: "req-sub", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
          }
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        }, 0);
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        if (navigations === 1 + BOOT_RETRY_LIMIT) await new Promise((resolve) => setTimeout(resolve, 10));
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/layoutharness.html", {
        bootExpression: "window.measurementAfterStylesheetsLoaded",
        bootLabel: "the layoutharness boot global window.measurementAfterStylesheetsLoaded",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The iframe belongs to attempt 1; the final attempt captured nothing.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The boot check must not read attribution while the current navigation's
// commit is still pending: a load event from the old document can resolve the
// wait first, and reading the verdict then would fall the final navigation's
// own failure back to the previous attempt. The loop waits, bounded, for the
// commit before the check runs.
test("a pending commit is awaited before the final verdict is read", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // The wait resolves on a stale load from the old document, and the
          // final navigation's own request dies - all before it commits.
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-pending", loaderId: `loader-${navigations}` },
            }),
          });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-pending", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
          // The commit lands only on a later task.
          setTimeout(() => {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Page.frameNavigated",
                params: { frame: { id: `top-${navigations}`, loaderId: `loader-${navigations}` } },
              }),
            });
          }, 0);
          return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
        }
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { id: `top-${navigations}`, loaderId: `loader-${navigations}` } },
          }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The commit wait must match THIS navigation only. A stale top commit (here
// loaderless, from before this navigation) must not release it, or the check
// runs before the awaited loaderId commits and its failure falls back.
test("a stale top commit does not release the commit wait", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations !== 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { id: "top", loaderId: `loader-${navigations}` } },
            }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
        }
        // The wait resolves on a stale load; the final navigation's own request
        // dies before it commits.
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Network.requestWillBeSent",
            params: { requestId: "req-final", loaderId: `loader-${navigations}` },
          }),
        });
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Network.loadingFailed",
            params: { requestId: "req-final", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
          }),
        });
        // A stale, LOADERLESS top commit lands first; the awaited commit only
        // lands on a later task.
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: { id: "top" } } }),
          });
        }, 0);
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { id: "top", loaderId: `loader-${navigations}` } },
            }),
          });
        }, 5);
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A loaderless navigation has no loaderId to match on, but its commit must
// still be awaited: otherwise the check reads attribution before its document
// is live and the failure is misreported as a regression.
test("a loaderless navigation's commit is awaited before the verdict", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        }
        setTimeout(() => {
          // Each loaderless commit lands after its response queued the attempt.
          socket.dispatch("message", {
            data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: { id: "top" } } }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          if (navigations === 1 + BOOT_RETRY_LIMIT) {
            // An unseen death in the final attempt's own document.
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
          }
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
        bootExpression: "typeof window.settledShell !== 'undefined'",
        bootLabel: "the shellguard entry global window.settledShell",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A loaderless navigation that never commits must be EXPIRED, not left at the
// head of the pending queue: otherwise the NEXT navigation's legitimate commit
// is consumed by the stale entry, that navigation times out, and its evidence
// is excluded from its own verdict.
test("a never-committing loaderless navigation does not consume the next retry's commit", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1) {
          // This navigation never commits: its wait must expire.
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          return { result: { frameId: "fixture-frame" } };
        }
        // The wait resolves on a stale load; the commit lands after the
        // response queued this navigation's attempt.
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        setTimeout(() => {
          socket.dispatch("message", {
            data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: {} } }),
          });
          if (navigations === 1 + BOOT_RETRY_LIMIT) {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
          }
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The final attempt's own delete (its commit and death) must count.
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A loaderless Page.frameNavigated can arrive BEFORE its Page.navigate response.
// The navigation is queued before the command is issued, so that early commit
// reconciles instead of being lost and forcing a needless settle timeout that
// leaves the live-document identity stale.
test("a loaderless commit arriving before its response is reconciled", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations !== 1 + BOOT_RETRY_LIMIT) {
          setTimeout(() => {
            socket.dispatch("message", {
              data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: {} } }),
            });
            socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          }, 0);
          return { result: { frameId: "fixture-frame" } };
        }
        // The final navigation's commit arrives BEFORE its response.
        socket.dispatch("message", {
          data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: {} } }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Network.loadingFailed",
            params: { type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
          }),
        });
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// Redirects re-emit requestWillBeSent under the SAME requestId and the same
// loaderId, possibly during a LATER attempt's window; first-writer-wins keeps
// the request owned by the document that started it.
test("a redirected request re-emitted in a later window stays owned by its first document", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        if (navigations === 1 || navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-redirect", loaderId: "loader-1" },
            }),
          });
        }
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-redirect", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/transcriptscrollguard.html", {
        bootExpression: "typeof window.waitForTranscriptSettled !== 'undefined'",
        bootLabel: "the transcriptscrollguard entry global window.waitForTranscriptSettled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The residual identity-less case, decided explicitly: a Page.navigate
// response that reports no loaderId leaves its document with no identity to
// honor, so an unseen death in its window degrades to the ARRIVAL COUNTER -
// the round-4 semantics, because no better anchor exists in that mode.
test("without a reported loaderId, attribution degrades to the arrival counter", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        // No loaderId in any navigate response: identity is unavailable.
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-no-identity", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
        bootExpression: "typeof window.settledShell !== 'undefined'",
        bootLabel: "the shellguard entry global window.settledShell",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The LIVE-DOCUMENT identity must advance at the navigation's COMMIT (the main
// frame's Page.frameNavigated), not at the Page.navigate response (which only
// signals initiation) and not at the load event. A new document emits
// Network.requestWillBeSent for its module burst and its
// iframes BETWEEN commit and load, and a resource that dies on the wire does
// not delay the load event - so the failure arrives while the identity still
// named the previous document. Binding a first-seen loaderId to the previous
// attempt files the flake in the prior bucket and leaves the final attempt's
// bucket empty: the transient network change this seam exists for is then
// misreported as a deterministic harness regression.
test("a first-seen loaderId emitted before the load event belongs to the document currently loading", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // The final navigation COMMITS (Page.frameNavigated), then its own
          // document emits a request under its loaderId and loses it on the
          // wire - all before Page.loadEventFired. A request bearing the new
          // loaderId cannot exist before the navigation that named it has
          // committed, so the commit event is the anchor for this window.
          setTimeout(() => {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Page.frameNavigated",
                params: { frame: { loaderId: `loader-${navigations}`, url: "http://127.0.0.1:65535/" } },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.requestWillBeSent",
                params: { requestId: "req-loading-doc", loaderId: `loader-${navigations}` },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: "req-loading-doc", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
            socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          }, 0);
          return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
        }
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The failure belongs to the final attempt's own document, so the
        // environment framing is earned.
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// Subframe (iframe) documents get their OWN loaderIds, which no top-level
// Page.navigate response ever names. Without binding them on first sight the
// mapping is empty, so a late failure for an iframe resource falls back to the
// ARRIVAL counter and can be filed under the final attempt - flipping a
// deterministic death into an environment framing. The first-seen loaderId
// binds to the attempt owning the live document (first writer wins).
test("an iframe loaderId resolves to the attempt whose document committed the frame", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        if (navigations === 1) {
          // Attempt 1's document creates an iframe with its OWN loaderId -
          // never named by any Page.navigate response - and the iframe commits.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-iframe", loaderId: "loader-iframe" },
            }),
          });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Page.frameNavigated",
              params: { frame: { loaderId: "loader-iframe", parentId: "fixture-frame" } },
            }),
          });
        }
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // The iframe resource dies on the wire, but its failure is delivered
          // late - inside the FINAL attempt's window. It belongs to the iframe
          // document attempt 1 committed, not to the final attempt.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-iframe", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/layoutharness.html", {
        bootExpression: "window.measurementAfterStylesheetsLoaded",
        bootLabel: "the layoutharness boot global window.measurementAfterStylesheetsLoaded",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The death belongs to attempt 1's iframe document; the final attempt's
        // bucket is empty, so the final diagnosis is the regression framing.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A request whose send was seen but reported NO loaderId is not the same as an
// unseen request: it has a decided rule - anchor to the document that was live
// and emitting WHEN IT WAS SENT, never to whichever document is live when its
// failure finally arrives. Conflating the recorded null with an unseen send
// routes both through the arrival-time fallback.
test("a request that reported no loaderId anchors to the document live at send time, not at failure arrival", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        if (navigations === 1) {
          // The send is seen but reports no loaderId; attempt 1's document
          // (loader-1) is the one live and emitting right now.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-null" },
            }),
          });
        }
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-null", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The death belongs to attempt 1's document; the final attempt's bucket
        // is empty, so the final diagnosis is the regression framing.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A request whose send was seen but reported no loaderId, sent while NO
// document has committed (currentLoaderId is null), has only its send-time
// attempt as an anchor. Leaving a null loader id to resolve later hands it to
// whichever attempt is live when the failure arrives - a later retry - instead
// of the attempt that was in flight when it was sent.
test("a loaderless request sent before any document commits owns its send-time attempt", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1) {
          // No navigation has committed yet: currentLoaderId is null, and the
          // in-flight attempt is the request's only owner.
          socket.dispatch("message", {
            data: JSON.stringify({ method: "Network.requestWillBeSent", params: { requestId: "req-precommit" } }),
          });
        }
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-precommit", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The Page.navigate RESPONSE signals initiation, not commit: the previous
// document is still live and emitting until the frame navigates. Advancing the
// live-document identity at the response would anchor the old document's own
// request to the INCOMING attempt, injecting its failure into the new
// attempt's bucket - on the final attempt, old evidence into the terminal
// verdict. The identity advances at the commit (Page.frameNavigated) instead.
test("an old-document request emitted between the navigate response and the commit is not filed under the new attempt", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // The response lands, but the frame has NOT navigated yet: the
          // previous document emits a loaderless request that dies on the
          // wire before the incoming commit. It belongs to the previous
          // document, not the incoming attempt.
          setTimeout(() => {
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.requestWillBeSent",
                params: { requestId: "req-old-live" },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: "req-old-live", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Page.frameNavigated",
                params: { frame: { loaderId: `loader-${navigations}` } },
              }),
            });
            socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
          }, 0);
          return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
        }
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
        bootExpression: "typeof window.settledShell !== 'undefined'",
        bootLabel: "the shellguard entry global window.settledShell",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// The incoming navigation's OWN main-resource request can carry the new
// loaderId BEFORE the frame commits (Page.navigate response -> frameStartedLoading
// -> frameNavigated -> load). A seen request must therefore be attributed once
// the mapping is SETTLED, not at arrival: binding the first-seen loaderId to the
// still-live previous document and bucketing there would file the final
// attempt's own wire death under the previous attempt, leaving the terminal
// window empty and misreporting the flake as a harness regression.
test("a main-resource failure carrying the new loaderId before the commit still counts for the loading attempt", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        if (navigations === 1 + BOOT_RETRY_LIMIT) {
          // The new document's own main resource is requested and dies on the
          // wire under the NEW loaderId - all before the frame commits.
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.requestWillBeSent",
              params: { requestId: "req-main", loaderId: `loader-${navigations}` },
            }),
          });
          socket.dispatch("message", {
            data: JSON.stringify({
              method: "Network.loadingFailed",
              params: { requestId: "req-main", type: "Document", errorText: "net::ERR_NETWORK_CHANGED" },
            }),
          });
        }
        socket.dispatch("message", {
          data: JSON.stringify({
            method: "Page.frameNavigated",
            params: { frame: { loaderId: `loader-${navigations}` } },
          }),
        });
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame", loaderId: `loader-${navigations}` } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/overflowharness.html", {
        bootExpression: "typeof window.settled !== 'undefined'",
        bootLabel: "the overflowharness entry global window.settled",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The failure belongs to the loading attempt's own document once the
        // commit names it, so the environment framing is earned.
        assert.match(error.message, /environment problem, not a test case failure/);
        assert.match(error.message, /net::ERR_NETWORK_CHANGED/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// A committed document may report NO loaderId (currentLoaderId null). The
// attempt that committed it must still own the requests it keeps emitting: the
// document is live until the NEXT navigation commits, and attributing its
// failures to the retry now in flight would inject stale wire evidence into the
// terminal verdict. The committed attempt is tracked separately from its
// optional loaderId.
test("a loaderless committed document's still-live request does not bleed into the next retry", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        // Commit and load land after the response registers the attempt.
        setTimeout(() => {
          if (navigations === 1 + BOOT_RETRY_LIMIT) {
            // The previous (loaderless) document is still live and emits a
            // request that dies before the incoming attempt commits.
            socket.dispatch("message", {
              data: JSON.stringify({ method: "Network.requestWillBeSent", params: { requestId: "req-stale" } }),
            });
            socket.dispatch("message", {
              data: JSON.stringify({
                method: "Network.loadingFailed",
                params: { requestId: "req-stale", type: "Script", errorText: "net::ERR_NETWORK_CHANGED" },
              }),
            });
          }
          // Every navigation commits but reports no loaderId: identity is
          // unavailable, so only the committed ATTEMPT number can own requests.
          socket.dispatch("message", {
            data: JSON.stringify({ method: "Page.frameNavigated", params: { frame: {} } }),
          });
          socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        }, 0);
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/spawnguard.html", {
        bootExpression: "typeof window.settledSpawn !== 'undefined'",
        bootLabel: "the spawnguard entry global window.settledSpawn",
        retryDelayMs: 0,
      }),
      (error) => {
        assert.match(error.message, /never booted/);
        // The stale failure belongs to the previous committed document; the
        // final attempt severed no evidence, so this is a harness regression.
        assert.doesNotMatch(error.message, /environment problem/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// layoutguard's boot predicate (review M2): [].every(...) is vacuously true,
// so a document that links NO stylesheet at all - an error page, a wrong-page
// harness, a stripped document - used to pass the boot check and walk into
// waitForFonts to die as the fonts misfire. Every document the predicate
// visits must have linked at least one stylesheet, and every link must have
// loaded.
const sheetLoaded = () => ({ sheet: { cssRules: [] } });
const sheetFailed = () => ({ sheet: null });

function stubStylesheetPage({ top, frames = [] }) {
  const frameStubs = frames.map((links) => ({
    contentDocument: {
      querySelectorAll: (selector) => (selector.includes("stylesheet") ? [...links] : []),
    },
  }));
  return {
    querySelectorAll: (selector) => {
      if (selector === "iframe") return frameStubs;
      return selector.includes("stylesheet") ? [...top] : [];
    },
  };
}

test("the stylesheet boot predicate passes a case whose every document linked and loaded its links", async () => {
  vi.stubGlobal(
    "document",
    stubStylesheetPage({ top: [sheetLoaded(), sheetLoaded()], frames: [[sheetLoaded(), sheetLoaded()]] }),
  );
  assert.equal(harnessStylesheetsLoadedInPage(), true);
});

test("the stylesheet boot predicate rejects a link whose sheet never arrived", async () => {
  vi.stubGlobal("document", stubStylesheetPage({ top: [sheetLoaded(), sheetFailed()] }));
  assert.equal(harnessStylesheetsLoadedInPage(), false);
});

test("the stylesheet boot predicate rejects a document that links no stylesheet at all", async () => {
  vi.stubGlobal("document", stubStylesheetPage({ top: [] }));
  assert.equal(harnessStylesheetsLoadedInPage(), false);
});

test("the stylesheet boot predicate rejects a fixture frame that links no stylesheet at all", async () => {
  vi.stubGlobal(
    "document",
    stubStylesheetPage({ top: [sheetLoaded(), sheetLoaded()], frames: [[], [sheetLoaded(), sheetLoaded()]] }),
  );
  assert.equal(harnessStylesheetsLoadedInPage(), false);
});

// The boot listener is a handler on the guards' shared CDP socket (review L1):
// a frame it cannot parse, or a loadingFailed event without params, must be
// survived - an exception in this listener would break the dispatch of every
// later event on the same socket the guards reuse across cases.
test("a CDP frame the boot listener cannot parse cannot break the navigation", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  let enables = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.enable":
        // The garbage frame arrives while ONLY the seam's boot listener is
        // attached: between attempts, after navigateToOnce's load tripwire
        // has taken its own listener off.
        enables++;
        if (enables === 2) socket.dispatch("message", { data: "this is not a CDP message" });
        return {};
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        // The first page is dead; the re-navigation boots.
        return { result: { result: { value: navigations > 1 } } };
      default:
        return {};
    }
  };

  const retryNotes = [];
  const noteError = vi.spyOn(console, "error").mockImplementation((...args) => {
    retryNotes.push(args.join(" "));
  });

  try {
    await navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
      bootExpression: "typeof window.settledShell !== 'undefined'",
      bootLabel: "the shellguard entry global window.settledShell",
      retryDelayMs: 0,
    });
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 2, "a garbage frame must not turn a recovering page into a failure");
  assert.deepEqual(retryNotes, [
    "navigateTo: the harness page at http://127.0.0.1:65535/shellguard.html never booted (attempt 1: the shellguard entry global window.settledShell is missing) - re-navigating",
  ]);
  assert.equal(socket.listenerCount("message"), 0);
});

test("a loadingFailed event without params is survived and never miscounts as captured evidence", async () => {
  const socket = fakeSocket();
  let navigations = 0;
  const send = async (method) => {
    switch (method) {
      case "Page.navigate":
        navigations++;
        socket.dispatch("message", { data: JSON.stringify({ method: "Page.loadEventFired" }) });
        if (navigations === 1) {
          socket.dispatch("message", { data: JSON.stringify({ method: "Network.loadingFailed" }) });
        }
        return { result: { frameId: "fixture-frame" } };
      case "Runtime.evaluate":
        return { result: { result: { value: false } } };
      default:
        return {};
    }
  };
  const noteError = vi.spyOn(console, "error").mockImplementation(() => {});

  try {
    await assert.rejects(
      navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/transcriptscrollguard.html", {
        bootExpression: "typeof window.waitForTranscriptSettled !== 'undefined'",
        bootLabel: "the transcriptscrollguard entry global window.waitForTranscriptSettled",
        retryDelayMs: 0,
      }),
      (error) => {
        // The event carried nothing usable, so it is not evidence: the
        // rejection must still be the boot diagnostic naming no captured
        // failures, never a TypeError out of the summarizer.
        assert.match(error.message, /never booted/);
        assert.match(error.message, /No request failures were captured/);
        return true;
      },
    );
  } finally {
    noteError.mockRestore();
  }

  assert.equal(navigations, 1 + BOOT_RETRY_LIMIT);
  assert.equal(socket.listenerCount("message"), 0);
});

// An inner navigation failure with boot options enabled (review L2): the
// original CDP error must propagate unchanged - no retry, no rewrapping - and
// the seam must still tear the Network domain down and remove its listener.
test("an inner navigation failure with boot options propagates without a retry", async () => {
  const socket = fakeSocket();
  const sent = [];
  let navigations = 0;
  let bootChecks = 0;
  const send = async (method) => {
    sent.push(method);
    switch (method) {
      case "Page.navigate":
        navigations++;
        throw new Error('Page.navigate: {"code":-32000,"message":"Cannot navigate to invalid URL"}');
      case "Runtime.evaluate":
        bootChecks++;
        return { result: { result: { value: true } } };
      default:
        return {};
    }
  };

  const before = liveTimers();
  await assert.rejects(
    navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
      bootExpression: "typeof window.settledShell !== 'undefined'",
      bootLabel: "the shellguard entry global window.settledShell",
      retryDelayMs: 0,
    }),
    /Cannot navigate to invalid URL/,
  );

  assert.equal(navigations, 1, "a navigation that failed must not be retried as if it had booted");
  assert.equal(bootChecks, 0, "the boot check must not run on a page that never navigated");
  assert.ok(sent.includes("Network.disable"), "the Network domain must be torn down on the failure path");
  assert.equal(socket.listenerCount("message"), 0);
  assert.ok(liveTimers() <= before, "the failure path must not leave a timer holding the event loop");
});

// Cleanup scoping (review round 3): a Network.enable whose response never
// arrives may still have ENABLED the domain inside Chrome - a timeout on the
// command does not prove the command was not applied. The guards reuse this
// page for every later case, so the disable must go out on that path too, not
// only when the enable answers.
test("a Network.enable that never answers still tears the Network domain down", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const socket = fakeSocket();
  const sent = [];
  const send = async (method) => {
    sent.push(method);
    if (method === "Network.enable") return new Promise(() => {});
    return {};
  };

  const rejected = assert.rejects(
    navigateTo({ ws: socket, send }, "http://127.0.0.1:65535/shellguard.html", {
      bootExpression: "typeof window.settledShell !== 'undefined'",
      bootLabel: "the shellguard entry global window.settledShell",
      retryDelayMs: 0,
    }),
    /timeout calling Network\.enable after 30000ms/,
  );
  await vi.advanceTimersByTimeAsync(30_000);
  await rejected;

  assert.ok(
    sent.includes("Network.disable"),
    "the enable timing out must still disable the domain: the guards reuse this page",
  );
  assert.equal(socket.listenerCount("message"), 0);
  assert.equal(vi.getTimerCount(), 0, "the enable-timeout path must not leave a timer armed");
});

// waitForFonts runs on one coordinated in-page deadline: the registration
// poll owns the whole budget and the fonts.ready await races the remainder,
// capped by FONT_READY_TIMEOUT_MS. RoboRev caught the two gaps this pins:
// an unbounded fonts.ready await could overrun the evaluate() wrapper's
// ceiling (CDP_EVALUATE_TRIPWIRE_MS) and turn every diagnostic into an opaque
// Runtime.evaluate timeout, and a registration budget shorter than the observed >10s
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
      `the ${CDP_EVALUATE_TRIPWIRE_MS}ms evaluate() wrapper headroom, or a fontless document dies as an opaque timeout instead ` +
      `of the diagnostic`,
  );
});

// #919: the Runtime.evaluate ceiling was a bare 30000 literal, so it could not
// be referenced or held to the relationship it has with the guards -- it is the
// ONE hard bound above every guard's in-page budget, and a guard whose own
// tripwire overran it lost its structured blocker to an opaque
// `timeout calling Runtime.evaluate after 30000ms` (the #900 diagnosis). Named
// now, like the Go tripwires in PR #852 and PR #873, and kept above the guards'
// in-page waits so the guard's own blocker reports first whenever the page is
// still making progress.
test("the evaluate ceiling is named and outlasts every in-page guard budget", async () => {
  assert.ok(
    CDP_EVALUATE_TRIPWIRE_MS > FONT_POLL_DEADLINE_MS,
    `the evaluate ceiling (${CDP_EVALUATE_TRIPWIRE_MS}ms) must outlast the longest in-page guard budget ` +
      `(${FONT_POLL_DEADLINE_MS}ms), or the guard's own blocker is pre-empted by an opaque timeout`,
  );
  // evaluate() actually rides this ceiling: a send that never answers rejects
  // at exactly the named value, so the constant is the wired bound, not a
  // decorative export beside a separate literal.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const pending = evaluate(() => new Promise(() => {}), "1 + 41");
  const rejected = assert.rejects(
    pending,
    new RegExp(`timeout calling Runtime\\.evaluate after ${CDP_EVALUATE_TRIPWIRE_MS}ms`),
  );
  await vi.advanceTimersByTimeAsync(CDP_EVALUATE_TRIPWIRE_MS);
  await rejected;
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
  await assert.rejects(waitForFonts(send), /environment problem, not a test case failure.*font load is stalled/s);
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
