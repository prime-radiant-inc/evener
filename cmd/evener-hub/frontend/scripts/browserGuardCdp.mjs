// Shared CDP client plumbing for every browser guard (layoutguard,
// overflowguard, spawnguard). browserGuardProcess.mjs owns the PROCESS
// lifecycle (one Vite dev server + one headless Chrome with a private
// profile); this module owns the WIRE: waiting for endpoints, one WebSocket
// send/pending map, navigation, evaluation, viewport emulation, and pinned
// pseudo-states.
//
// WHY ONE MODULE: all three guards grew private copies of the same
// connect/send/navigate plumbing (layoutguard/cdp.mjs's withPage,
// overflowguard's connect, spawnguard's connect - the latter two verbatim
// twins). The guards stay separate ENTRYPOINTS on purpose - the Makefile
// runs each with its own log and PASS/FAIL verdict so one guard's missing
// browser cannot mask another's result - but there was never a reason for
// three divergent wire implementations, and layoutguard's copy is where the
// file:// font bug lived.
//
// Origin discipline (kata 8ecz): guards never touch the shared evener-hub dev
// server on 9180 or the shared MCP Chrome. Every page load is pinned to the
// guard's OWN loopback Vite origin by assertGuardOrigin.

import os from "node:os";

/**
 * Wrap a CDP operation with a timeout to prevent infinite hangs.
 *
 * THE TIMER MUST BE CLEARED WHEN THE RACE SETTLES. A pending timer is a live
 * handle on Node's event loop, and the guards deliberately end by setting
 * process.exitCode rather than calling process.exit(), so the process only
 * leaves when the loop drains. Leaving the loser of every race armed meant a
 * guard that had finished all its work sat there until the last 30-second timer
 * expired: measured on spawnguard at 1.451s of work followed by a 31.102s
 * process, and on a one-call script at 30.044s. Every green run paid it.
 *
 * @param {Promise} promise - The operation to timeout
 * @param {number} ms - Timeout in milliseconds
 * @param {string} operation - Name of the operation for error message
 * @returns {Promise} - The operation result or a timeout error
 */
function withTimeout(promise, ms, operation) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`timeout calling ${operation} after ${ms}ms`)), ms);
    }),
  ]).finally(() => clearTimeout(timer));
}

export function devtoolsHttpURL(endpoint, pathname) {
  const announced = new URL(endpoint.url);
  const host = endpoint.host ?? announced.hostname;
  const port = endpoint.port ?? Number(announced.port || 80);
  return `http://${host}:${port}${pathname}`;
}

function abortReason(signal) {
  return signal?.reason ?? new Error("operation aborted");
}

function delay(ms, signal) {
  if (!signal) return new Promise((resolve) => setTimeout(resolve, ms));
  if (signal.aborted) return Promise.reject(abortReason(signal));
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, ms);
    const abort = () => {
      clearTimeout(timer);
      reject(abortReason(signal));
    };
    signal.addEventListener("abort", abort, { once: true });
  });
}

const startupFailures = new WeakSet();

function raceWithFailure(promise, failure) {
  if (!failure) return promise;
  return Promise.race([
    promise,
    failure.then((error) => {
      startupFailures.add(error);
      throw error;
    }),
  ]);
}

function raceWithAbort(promise, signal) {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(abortReason(signal));
  return new Promise((resolve, reject) => {
    const abort = () => reject(abortReason(signal));
    signal.addEventListener("abort", abort, { once: true });
    promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
  });
}

/**
 * One probe attempt's own tripwire bound.
 *
 * Once Chrome has announced its DevTools endpoint the port is BOUND, so a probe
 * can no longer fail fast: the connection is accepted and, while the browser
 * thread behind it is still busy, simply ignored. fetch never gives up on a
 * connected socket of its own accord, so a single attempt that lands in that
 * window used to hold the caller's whole startup deadline open while the
 * poll-every-100ms loop below waited on it - one attempt, then the deadline
 * (run 34257184696: "deadline exceeded after 30000ms" printed underneath the
 * DevTools announcement it had already received).
 *
 * This is a tripwire, not the mechanism: the wait still ends when the endpoint
 * answers, and the caller's deadline still decides how long the phase may take.
 * Measured announcement-to-answer on a warm local Chrome: 138ms to 1.6s.
 */
export const PROBE_ATTEMPT_TIMEOUT_MS = 2000;

/**
 * Bound ONE attempt without losing the caller's deadline.
 *
 * An AbortController rather than withTimeout's race: the race's loser keeps
 * running, and the loser here is a live socket. Composing with the caller's
 * signal is what still lets the startup deadline cancel the request in flight
 * instead of leaving it holding the guard's event loop open.
 *
 * Which is also why clear() ABORTS an unsettled request rather than only
 * clearing the timer. The attempt is not always what ends the race: a Chrome
 * that exits mid-probe settles the failure promise instead, and the request it
 * was racing stays connected to an endpoint nobody is waiting for any more -
 * a live socket outliving the wait that owned it, until its own bound expires
 * seconds later. A request that already answered is left alone; aborting that
 * would only tear down a response the caller has in hand.
 */
function boundAttempt(signal, ms) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error(`probe attempt exceeded ${ms}ms`)), ms);
  let settled = false;
  return {
    signal: signal ? AbortSignal.any([signal, controller.signal]) : controller.signal,
    settle: () => {
      settled = true;
    },
    clear: () => {
      clearTimeout(timer);
      if (!settled) controller.abort(new Error("probe attempt abandoned before it answered"));
    },
  };
}

/**
 * What the poll actually did, for whichever way it ended.
 *
 * A phase that ran out of budget and a phase that ran out of attempts are both
 * unreadable without it: waiting for Chrome's stderr announcement and waiting
 * for the announced endpoint to answer share one deadline and used to report
 * the same bare "browser startup deadline exceeded", and the attempt-cap
 * message named neither how many attempts there had been nor what the last one
 * was doing.
 */
function describeAttempts(attempts, lastAttempt) {
  return `after ${attempts} attempt${attempts === 1 ? "" : "s"}, last attempt ${lastAttempt}`;
}

/**
 * The budget one startup PHASE gets - the wait for Chrome's DevTools
 * announcement, or a poll for an endpoint to answer. Each phase's caller arms
 * one of these and hands its signal down, and for a poll that signal is the
 * ONLY thing bounding total wall time: the attempt cap inside waitForHttp is a
 * backstop, not a clock.
 *
 * This is an ENVIRONMENT TRIPWIRE, not an assertion about startup time: the
 * phase still ends only when the awaited event arrives, and the deadline only
 * decides how long silence is read as "broken" rather than "slow". A FIXED
 * floor therefore failed correct startups on loaded runners (runs 33803871850
 * and 34257184696: "browser startup deadline exceeded after 30000ms" on a cold,
 * contended machine). The announcement half was widened to
 * CHROME_ANNOUNCEMENT_DEADLINE_MS; the endpoint/vite poll kept the bare floor.
 * It is now load-aware: the floor on an idle machine, widening with the
 * machine's oversubscription toward the announcement budget's ceiling.
 */
export const STARTUP_DEADLINE_MS = 30000;
export const STARTUP_DEADLINE_CAP_MS = 120000;

/**
 * The startup deadline for a machine whose 1-minute load is `load1` over
 * `cores` cores.
 *
 * Pressure is load per core, clamped to [0, 1]: an idle or lightly loaded box
 * keeps the floor, a box at or past one runnable task per core gets the
 * ceiling, and everything between interpolates. The signal is load AVERAGE, so
 * it reflects contention that is already underway rather than a single
 * instantaneous sample. An unreadable load (or a platform whose loadavg is
 * meaningless, e.g. Windows) is treated as idle and keeps the floor -- an
 * unknown machine must never look MORE pressured than it is.
 */
export function startupDeadlineMs({
  floorMs = STARTUP_DEADLINE_MS,
  capMs = STARTUP_DEADLINE_CAP_MS,
  load1 = os.loadavg()[0],
  cores = os.availableParallelism(),
} = {}) {
  // availableParallelism accounts for CPU affinity and, where the platform
  // exposes one, a cgroup CPU quota -- the same effective-core signal
  // scripts/lib/load-aware-workers.sh derives from nproc and the cgroup quota.
  // An unreadable signal is treated as idle (pressure 0): an unknown machine
  // must never look more pressured than it is.
  const coresReadable = Number.isFinite(cores) && cores >= 1;
  const load = Number.isFinite(load1) && load1 > 0 ? load1 : 0;
  const pressure = coresReadable ? Math.min(1, load / cores) : 0;
  return Math.round(floorMs + (capMs - floorMs) * pressure);
}

export function createStartupDeadline(ms = startupDeadlineMs()) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error(`browser startup deadline exceeded after ${ms}ms`)), ms);
  return {
    signal: controller.signal,
    clear: () => clearTimeout(timer),
  };
}

/**
 * Poll a URL until it answers OK; the error names what never came up.
 *
 * launchFailed reports the LAUNCH error of the subsystem this endpoint belongs
 * to, if spawn() never got it running at all (browserGuardProcess.mjs's
 * getViteLaunchError / getChromeLaunchError - kata ssca). A subsystem that was
 * never running has nothing to poll for, so the wait stops with the launch
 * error itself rather than spending 30 seconds arriving at a timeout that names
 * the endpoint instead of the reason. It is deliberately per-subsystem: a
 * Chrome that could not launch must not abort - or be blamed for - the wait on
 * a Vite that is coming up fine.
 *
 * Every attempt carries its own bound (PROBE_ATTEMPT_TIMEOUT_MS) so that one
 * request the endpoint accepts and never answers cannot stand in for the whole
 * poll. The total bound is the caller's deadline, and a caller that does not
 * pass one gets STARTUP_DEADLINE_MS of its own: the attempt cap below is a
 * backstop against an endless loop, not a wall-clock bound, and with
 * per-attempt bounds it no longer approximates one - 300 silent attempts would
 * otherwise be ten minutes of polling nobody asked for.
 */
export async function waitForHttp(
  url,
  label,
  launchFailed = () => null,
  { signal, failure = null, fetchImpl = fetch, attemptTimeoutMs = PROBE_ATTEMPT_TIMEOUT_MS } = {},
) {
  const ownDeadline = signal ? null : createStartupDeadline();
  const deadline = signal ?? ownDeadline.signal;
  let attempts = 0;
  let lastAttempt = "none";
  try {
    for (let attempt = 0; attempt < 300; attempt++) {
      if (deadline.aborted) throw abortReason(deadline);
      const launchError = launchFailed();
      if (launchError) throw launchError;
      const bound = boundAttempt(deadline, attemptTimeoutMs);
      attempts++;
      lastAttempt = "still in flight";
      try {
        const request = fetchImpl(url, { signal: bound.signal });
        request.then(bound.settle, bound.settle);
        const response = await raceWithFailure(raceWithAbort(request, deadline), failure);
        if (response.ok) return;
        lastAttempt = `answered HTTP ${response.status}`;
      } catch (error) {
        if (startupFailures.has(error) || deadline.aborted)
          throw startupFailures.has(error) ? error : abortReason(deadline);
        // The child process is still starting, or this attempt outlasted its
        // own bound and the next one gets a fresh connection.
        lastAttempt = error.message;
      } finally {
        bound.clear();
      }
      await raceWithFailure(delay(100, deadline), failure);
    }
    throw new Error(`${label} never came up at ${url} ${describeAttempts(attempts, lastAttempt)}`);
  } catch (error) {
    // The deadline's own message stays the prefix: that is what the runners
    // frame as an environment problem rather than a test case failure.
    if (attempts > 0 && error === abortReason(deadline)) {
      throw new Error(`${error.message} while polling ${url} for ${label} ${describeAttempts(attempts, lastAttempt)}`);
    }
    throw error;
  } finally {
    ownDeadline?.clear();
  }
}

/**
 * Find Chrome's page target over CDP and open a command channel to it.
 * send() rejects on a CDP error response so a failing command can never read
 * as a successful measurement. Callers close() in a finally.
 */
export async function connectPage(endpoint) {
  const targets = await (await fetch(devtoolsHttpURL(endpoint, "/json/list"))).json();
  const target = targets.find((entry) => entry.type === "page");
  if (!target) throw new Error("chrome exposed no page target");

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  let id = 0;
  const pending = new Map();
  ws.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (message.id !== undefined && pending.has(message.id)) {
      pending.get(message.id)(message);
      pending.delete(message.id);
    }
  });
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", reject, { once: true });
  });

  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const requestId = ++id;
      pending.set(requestId, (message) => {
        if (message.error) reject(new Error(`${method}: ${JSON.stringify(message.error)}`));
        else resolve(message);
      });
      ws.send(JSON.stringify({ id: requestId, method, params }));
    });

  return { ws, send, close: () => ws.close() };
}

// The bounded re-navigation budget navigateTo's boot options use: a harness
// page that loaded but never booted is re-navigated at most this many times.
export const BOOT_RETRY_LIMIT = 2;

// The pause between those re-navigations, giving a transient network change
// time to settle before the burst is asked for again.
export const BOOT_RETRY_DELAY_MS = 250;

/**
 * layoutguard's half of the boot seam, exported so the wire tests can call it
 * against a stub document. It runs in the page via toString(), so it must not
 * close over anything outside its own body: the documents come from the page's
 * own globals and the answer is a plain boolean.
 *
 * A layoutguard case has no entry module that could leave a boot global -
 * harness.html is static and window.measure comes from an inline script that
 * always runs - so the case's boot contract is that every stylesheet link
 * actually loaded, one document at a time (the top document's tokens.css and
 * resolved.css, plus whatever any same-origin srcdoc fixture iframe links for
 * itself): a request the burst died on leaves link.sheet null, and a page
 * whose stylesheets died measures a fontless, unstyled page. A document that
 * links NO stylesheet at all is not a booted case either - [].every(...) is
 * vacuously true, so that shape has to be refused explicitly or an error page
 * would pass as booted and die in waitForFonts as the fonts misfire.
 */
export function harnessStylesheetsLoadedInPage() {
  const docs = [document];
  for (const frame of document.querySelectorAll("iframe")) {
    try {
      if (frame.contentDocument) docs.push(frame.contentDocument);
    } catch {}
  }
  return docs.every((doc) => {
    const links = Array.from(doc.querySelectorAll('link[rel="stylesheet"]'));
    if (links.length === 0) return false;
    return links.every((link) => link.sheet !== null);
  });
}

/**
 * Group the Network.loadingFailed events captured during one attempt's boot
 * window by what failed and how often, so the terminal error reports the
 * burst ("Script net::ERR_NETWORK_CHANGED x3") instead of 40
 * near-identical lines.
 */
function summarizeLoadingFailures(failures) {
  if (failures.length === 0) return null;
  const counts = new Map();
  for (const failure of failures) {
    const kind = `${failure?.type ?? "Unknown"} ${failure?.errorText ?? "(no error text)"}`;
    counts.set(kind, (counts.get(kind) ?? 0) + 1);
  }
  return [...counts.entries()].map(([kind, count]) => (count === 1 ? kind : `${kind} x${count}`)).join("; ");
}

// The error texts that name a host network-state change - the flake this seam
// exists for (a Tailscale interface flap announces itself to Chrome as
// ERR_NETWORK_CHANGED; the interface's own outage arrives as
// ERR_INTERNET_DISCONNECTED). Anything else that fails on the wire - a
// blocked request, an ERR_FAILED resource the harness itself caused - is
// deterministic evidence about the harness: reported, but never counted as
// the network-change signature that earns the environment framing. Extending
// this set is a diagnosis decision, not a catch-all: a text added here lets
// every guard's boot-death attribution claim an environment cause.
const NETWORK_CHANGE_FAILURE_TEXTS = ["net::ERR_NETWORK_CHANGED", "net::ERR_INTERNET_DISCONNECTED"];

function isNetworkChangeFailure(failure) {
  return NETWORK_CHANGE_FAILURE_TEXTS.includes(failure?.errorText ?? "");
}

/**
 * Page.enable, navigate, and await the load event - the triple every guard
 * re-wrote. Resolves to the navigation's loaderId - the document identity the
 * boot seam correlates request evidence by - or null when the response does
 * not report one; the legacy two-argument callers ignore the return value.
 *
 * The listener comes off in a finally, not only on the load event: on the
 * timeout path the load never fires, and a handler left behind keeps parsing
 * every later CDP message on a socket the guards reuse across cases.
 */
async function navigateToOnce({ ws, send }, url) {
  await withTimeout(send("Page.enable"), 30000, "Page.enable");
  let handler;
  let abandonLoad;
  const loaded = withTimeout(
    new Promise((resolve) => {
      abandonLoad = resolve;
      handler = (event) => {
        if (JSON.parse(event.data).method === "Page.loadEventFired") resolve();
      };
      ws.addEventListener("message", handler);
    }),
    30000,
    "navigateTo",
  ).finally(() => ws.removeEventListener("message", handler));
  try {
    // Observe both immediately: the load tripwire can fire while Page.navigate
    // is still pending. Serial awaits leave that first rejection unhandled.
    const [, navigated] = await Promise.all([
      loaded,
      withTimeout(send("Page.navigate", { url }), 30000, "Page.navigate"),
    ]);
    return navigated?.result?.loaderId ?? null;
  } finally {
    // A failed command may never produce a load event. Release that wait (and
    // its listener/timer) without replacing the error Promise.all observed.
    abandonLoad();
    await Promise.allSettled([loaded]);
  }
}

/**
 * Navigate, and on a boot-checked harness page prove the harness actually ran.
 *
 * THE UNBOOTED-PAGE FLAKE THIS ABSORBS: a transient network change on the
 * host (measured on this machine: bursts of net::ERR_NETWORK_CHANGED, on
 * 127.0.0.1 loopback requests too, whenever a Tailscale interface flaps)
 * kills the Vite dev-server module burst mid-boot. The page still parses and
 * fires its load event - document.readyState "complete", both script tags in
 * the DOM - but the harness entry module never runs: its boot global is
 * missing and no injected stylesheet arrives. Every guard then walked into
 * waitForFonts on that dead page and reported the environment flake as "the
 * top document declares no web fonts".
 *
 * THE SEAM: callers pass `bootExpression`, an in-page expression that is
 * truthy exactly when the harness booted - for the tsx-entry harnesses, the
 * module-scope global their entry assigns (window.settled and friends); for
 * layoutguard's static case pages, that every stylesheet link actually loaded
 * (a dead burst leaves link.sheet null). It is evaluated AFTER the load event:
 * module scripts block the load event until their whole static import tree
 * has evaluated, so at load time a marker that will ever exist already does.
 * An unbooted page is re-navigated, bounded by BOOT_RETRY_LIMIT, and one that
 * never recovers fails with the boot cause - never the fonts error - plus the
 * Network.loadingFailed evidence captured inside the window.
 *
 * THE ATTRIBUTION IS EARNED, NOT ASSUMED: a missing boot marker alone does
 * not name the cause. A harness entry whose module-init path throws at top
 * level, a bad script href, or a broken import graph all leave the marker
 * unset while every request succeeds - a deterministic code failure. Only a
 * boot death whose FINAL attempt owned recognized network-change failures is
 * framed as the environment flake. Evidence is scoped by DOCUMENT IDENTITY,
 * never by the attempt counter or arrival time: each navigation's
 * Page.navigate response names its loaderId, every request is tagged with
 * the loaderId it reported (Network.requestWillBeSent, first writer wins -
 * redirects re-emit under the same pair), and each loadingFailed resolves
 * through its request's loaderId to the attempt that committed that
 * document. An unseen send anchors to the last COMMITTED navigation - the
 * document that is live when the failure arrives, which in the gap between
 * a new navigation's issue and its response is still the previous one; the
 * attempt counter advances at loop top and must never name an event's owner.
 * When Chrome reports no loaderId at all, no identity exists to honor and
 * attribution degrades to the arrival counter. The terminal attribution
 * reads only the final attempt's failures, so a flap that killed an earlier
 * attempt cannot launder a deterministic death on the final one.
 * Navigation-induced aborts
 * are never recorded at all. A final window that captured BOTH
 * network-change and other failures is reported as INCONCLUSIVE with both
 * summaries rather than issuing a definitive verdict either way; any other
 * captured failure is reported under the harness diagnosis rather than
 * flipping it.
 *
 * The legacy two-argument call is unchanged: no boot options, no Network
 * domain, no extra evaluate - skillguard's driver and editorial-preview keep
 * exactly the behavior they had.
 *
 * @param {{ws: object, send: Function}} page - the connected CDP page
 * @param {string} url - where to navigate
 * @param {object} [options]
 * @param {string} [options.bootExpression] - in-page expression, truthy when booted
 * @param {string} [options.bootLabel] - human name of the marker for errors and logs
 * @param {number} [options.retryDelayMs] - pause before each re-navigation
 */
export async function navigateTo(
  page,
  url,
  { bootExpression = null, bootLabel = null, retryDelayMs = BOOT_RETRY_DELAY_MS } = {},
) {
  if (!bootExpression) return navigateToOnce(page, url);
  const { ws, send } = page;
  // EVIDENCE IS SCOPED BY DOCUMENT IDENTITY, never by the attempt counter or
  // arrival time: each navigation's Page.navigate response names its
  // loaderId, requests are tagged with the loaderId they reported, and each
  // loadingFailed resolves through that loaderId to the attempt that
  // committed the document. The terminal attribution reads only the final
  // attempt's bucket, so a flap that killed an earlier attempt cannot
  // launder a deterministic death on the final one however late its
  // failures are delivered.
  const requestLoaderIds = new Map();
  const loaderAttempts = new Map();
  const failuresByAttempt = new Map();
  // The last navigation whose response has COMMITTED. It advances only when
  // a navigateToOnce resolves - never when the counter increments - so in
  // the gap between attempts++ and the new navigate's response this still
  // names the PREVIOUS document: the document that is live and emitting.
  let currentLoaderId = null;
  const onLoadingFailed = (event) => {
    // This handler sits on the guards' shared CDP socket, which other
    // listeners (connectPage's pending-command resolver, the load tripwire)
    // share: a frame it cannot parse, or an event without params, must be
    // skipped, never thrown - an exception here breaks the dispatch of every
    // later event on the socket.
    let message;
    try {
      message = JSON.parse(event.data);
    } catch {
      return;
    }
    if (!message?.params) return;
    if (message.method === "Network.requestWillBeSent") {
      // Tag by the event's OWN loaderId - the identity of the document that
      // sent it - never by the counter: an old document can still emit while
      // the counter already points at the new attempt. First writer wins: a
      // request's redirects re-emit requestWillBeSent under the SAME
      // requestId and loaderId, and the document that started the request
      // owns its whole lifecycle. A loaderId reported before its navigate
      // response lands resolves at attribution time, once the mapping is
      // built.
      if (message.params.requestId && !requestLoaderIds.has(message.params.requestId)) {
        requestLoaderIds.set(message.params.requestId, message.params.loaderId ?? null);
      }
      return;
    }
    if (message.method !== "Network.loadingFailed") return;
    // A re-navigation ABORTS the previous attempt's in-flight requests, so
    // canceled:true / net::ERR_ABORTED arrivals are the seam's own doing, and
    // a harness aborting its own request is equally deterministic. Neither is
    // a wire death, so neither is recorded: evidence must be failures that
    // happened to requests the page wanted kept alive.
    if (message.params.canceled === true || message.params.errorText === "net::ERR_ABORTED") return;
    // Resolve by identity, degrading only when identity is unavailable: a
    // seen request resolves through its loaderId; an unseen send anchors to
    // the last COMMITTED navigation (currentLoaderId - the document that is
    // live when the failure arrives, which in the navigate gap is still the
    // previous one); and a loaderId that was never mapped - a navigate
    // response that reported none - has no identity to honor, so the arrival
    // counter is the only anchor left.
    const requestLoaderId = requestLoaderIds.get(message.params.requestId) ?? currentLoaderId;
    const owner = loaderAttempts.get(requestLoaderId) ?? attempts;
    const bucket = failuresByAttempt.get(owner) ?? [];
    bucket.push(message.params);
    failuresByAttempt.set(owner, bucket);
  };
  ws.addEventListener("message", onLoadingFailed);
  let attempts = 0;
  try {
    // Network events only flow while the domain is enabled, and only this
    // window needs them. The enable sits inside the SAME cleanup scope as the
    // loop: a command whose response times out may still have been applied by
    // Chrome, and the guards reuse this page for every later case, so the
    // disable must go out on every path - including the enable never
    // answering.
    await withTimeout(send("Network.enable"), 30000, "Network.enable");
    for (;;) {
      attempts++;
      const loaderId = await navigateToOnce(page, url);
      if (loaderId) loaderAttempts.set(loaderId, attempts);
      // Identity for the navigation that just committed. currentLoaderId
      // advances only HERE - never at the counter increment above - so the
      // gap between the two still belongs to the previous document.
      currentLoaderId = loaderId;
      if (await evaluate(send, bootExpression)) return;
      if (attempts > BOOT_RETRY_LIMIT) {
        const budget =
          `after ${attempts} navigation${attempts === 1 ? "" : "s"} (${BOOT_RETRY_LIMIT} ` +
          `re-navigation${BOOT_RETRY_LIMIT === 1 ? "" : "s"} allowed)`;
        const verdict =
          `${bootLabel ?? bootExpression} did not hold even though the page's load event fired, ` +
          `so the page is a dead document no measurement can read`;
        // Only the FINAL attempt's owned failures name the cause: earlier
        // attempts' buckets exist but are never read here.
        const finalFailures = failuresByAttempt.get(attempts) ?? [];
        const networkEvidence = summarizeLoadingFailures(finalFailures.filter(isNetworkChangeFailure));
        const otherEvidence = summarizeLoadingFailures(
          finalFailures.filter((failure) => !isNetworkChangeFailure(failure)),
        );
        let diagnosis;
        if (networkEvidence && otherEvidence) {
          // A window holding BOTH a network change and deterministic failures
          // cannot be named from the wire alone: either verdict would discard
          // live evidence. Report both and let the reader judge.
          diagnosis =
            `inconclusive boot failure - the final attempt's window captured BOTH a network change and ` +
            `deterministic failures, so the wire evidence alone cannot name the cause: the harness page at ` +
            `${url} never booted ${budget}: ${verdict}. Chrome reported these network-change failures during ` +
            `the boot window: ${networkEvidence}. It also reported these deterministic failures: ` +
            `${otherEvidence}. If the network-change failures explain the death (the transient change this ` +
            `seam exists for - net::ERR_NETWORK_CHANGED is its signature), treat the run as an environment ` +
            `flake and retry the gate; if the deterministic failures point at the harness, check what the ` +
            `boot check measures - the harness entry and the product modules it imports (a top-level throw in ` +
            `module init, a bad script href, or a broken import graph all leave the boot marker unset), or ` +
            `the case's own stylesheet links.`;
        } else if (networkEvidence) {
          diagnosis =
            `environment problem, not a test case failure: the harness page at ${url} never booted ${budget}: ` +
            `${verdict}. Chrome reported these failed requests during the boot window: ${networkEvidence}. A ` +
            `burst dying on the wire like that is the transient network change this seam exists for ` +
            `(net::ERR_NETWORK_CHANGED is its signature) - an environment flake, not a test case regression.`;
        } else if (otherEvidence) {
          diagnosis =
            `harness boot failure - likely a test case regression, not an environment flake: the harness page at ` +
            `${url} never booted ${budget}: ${verdict}. Chrome reported these failed requests during the boot ` +
            `window: ${otherEvidence} - none of them the wire-death signature of the network-change flake this ` +
            `seam exists for, so no host network change explains the missing marker: check what the boot check ` +
            `measures - the harness entry and the product modules it imports (a top-level throw in module init, ` +
            `a bad script href, or a broken import graph all leave the boot marker unset), or the case's own ` +
            `stylesheet links.`;
        } else {
          diagnosis =
            `harness boot failure - likely a test case regression, not an environment flake: the harness page at ` +
            `${url} never booted ${budget}: ${verdict}. No request failures were captured during the boot ` +
            `window, so no request died on the wire: check what the boot check measures - the harness entry and ` +
            `the product modules it imports (a top-level throw in module init, a bad script href, or a broken ` +
            `import graph all leave the boot marker unset without reporting a failed request), or the case's ` +
            `own stylesheet links.`;
        }
        throw new Error(diagnosis);
      }
      const captured = failuresByAttempt.get(attempts)?.length ?? 0;
      console.error(
        `navigateTo: the harness page at ${url} never booted (attempt ${attempts}: ` +
          `${bootLabel ?? bootExpression} is missing` +
          (captured > 0 ? `, ${captured} request failure${captured === 1 ? "" : "s"} captured` : "") +
          ") - re-navigating",
      );
      await delay(retryDelayMs);
    }
  } finally {
    await withTimeout(send("Network.disable"), 30000, "Network.disable").catch(() => {});
    ws.removeEventListener("message", onLoadingFailed);
  }
}

/**
 * Evaluate an expression in the page (awaitPromise + returnByValue). A page
 * exception is an error carrying the page's own details, never a silently
 * undefined measurement.
 */
export async function evaluate(send, expression) {
  const response = await withTimeout(
    send("Runtime.evaluate", {
      expression,
      awaitPromise: true,
      returnByValue: true,
    }),
    30000,
    "Runtime.evaluate",
  );
  if (response.result.exceptionDetails) {
    throw new Error(`page eval threw: ${JSON.stringify(response.result.exceptionDetails)}`);
  }
  return response.result.result.value;
}

/**
 * Block until the page's web fonts have settled, and fail if the page is not
 * measuring the product's own fonts - either because a face it asked for could
 * not be fetched, or because it never asked for any.
 *
 * Page.loadEventFired does not mean the text is in its final font. Both faces
 * in global.css declare `font-display: swap`, so the document paints with the
 * fallback and re-lays out when the woff2 arrives - and every text metric a
 * guard measures (widths, wrap points, the 559/560 boundary) differs between
 * the two. Measuring on load is a coin flip decided by whether the font was
 * warm in Chrome's cache.
 *
 * AN EMPTY FONT SET IS A FAILURE, NOT A PASS (kata e4sh). A page declaring no
 * @font-face has an empty document.fonts, so awaiting it and finding nothing
 * broken used to read as success while the page rendered every glyph in a host
 * fallback the product never ships. Ten of layoutguard's fourteen cases sat in
 * exactly that position. Nothing but this check stops the eleventh from being
 * added the same way, so "no fonts" now says so instead of saying nothing.
 *
 * Same-origin frames count as part of the page, and are checked ONE DOCUMENT AT
 * A TIME. A case may build its fixtures inside srcdoc iframes - layoutguard's
 * activity-tree-responsive builds three - and each of those documents links the
 * stylesheet for itself. Pooling every face into one list and asking whether the
 * TOTAL is empty would let one fixture frame lose its stylesheet, and silently
 * revert to a host fallback, while its siblings kept theirs and carried the
 * check: exactly the silence e4sh exists to end, one frame further down.
 *
 * The page's load event, which every caller has already awaited, does not fire
 * until subframes have loaded, so their documents are complete here and need no
 * readiness dance of their own. Awaiting each frame's own fonts.ready is not
 * bookkeeping: before it, activity-tree-responsive measured its fixtures
 * mid-swap in a fallback (measured, on this machine: a deep row label 663px
 * wide and an 835px panel, against 671px and 856px once the frames' faces are
 * actually settled).
 *
 * Deliberately NOT a hardcoded family list, and deliberately not "a face must
 * have LOADED". A face loads only when some text actually uses it, so a case
 * whose markup is all mono legitimately leaves the sans face "unloaded"
 * forever - measured: three of layoutguard's fourteen honest cases load the
 * mono face alone. What must never pass is a face the page DID request failing
 * to arrive: that is the 404 which would otherwise leave every guard green and
 * permanently measuring the fallback, and it reports as status "error".
 *
 * fonts.ready settles INSTANTLY while a set is still empty, so racing the
 * stylesheet application that registers the faces misreads "not arrived yet"
 * as "declares none". Under sustained machine load that race fired across
 * guards (overflowguard, then retirementguard) while every case passed
 * standalone. The in-page wait runs on ONE coordinated deadline: the
 * registration poll owns the whole FONT_POLL_DEADLINE_MS budget - it has to,
 * because stylesheet application was observed taking more than 10s under
 * sustained load - and the fonts.ready await, unbounded while any load
 * hangs, races the budget's REMAINDER, capped at FONT_READY_TIMEOUT_MS, and
 * reports a stall as data. No wait in the page can outlast FONT_POLL_DEADLINE_MS,
 * pinned by test at 20000ms so the evaluate() wrapper's 30000ms ceiling
 * always fires last: a genuinely fontless or font-stalled document reports
 * one of the actionable diagnostics below, never an opaque timeout.
 */

// The registration poll's deadline, which is also the TOTAL in-page budget:
// the fonts.ready await below runs on this deadline's remainder. It must
// cover the stylesheet-application windows observed exceeding 10s under
// sustained machine load, and it must lose to the evaluate() ceiling, never
// win against it.
export const FONT_POLL_DEADLINE_MS = 20000;

// The most the fonts.ready await may take once the poll exits early. When the
// poll spends the whole budget waiting on a fontless document, the remainder
// is zero and the await reports a stalled load immediately instead of
// hanging on it.
export const FONT_READY_TIMEOUT_MS = 8000;

// The in-page half of waitForFonts, exported so the wire tests can call it
// against a stub document. It runs in the page via toString(), so it must not
// close over anything outside its own body: the budgets are passed in, the
// documents come from the page's own globals, and it returns plain data (a
// stalled flag plus each document's faces) for the node side to diagnose.
export async function collectFontStatusInPage({ pollMs, readyMs }) {
  const found = [{ label: "the top document", doc: document }];
  const frames = document.querySelectorAll("iframe");
  for (let index = 0; index < frames.length; index++) {
    try {
      const doc = frames[index].contentDocument;
      if (doc) found.push({ label: `iframe #${index} (${frames[index].className || "no class"})`, doc });
    } catch {
      // Cross-origin: not reachable, and not something a guard builds.
    }
  }
  const deadline = Date.now() + pollMs;
  while (found.some(({ doc }) => doc.fonts.size === 0) && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  // fonts.ready is unbounded while any load hangs, so it is raced against the
  // shared deadline's remainder (never more than readyMs); a stall reports as
  // data, and clearing the cap timer keeps a settled wait from holding the
  // guard process open for the loser's remaining ms.
  const remaining = Math.max(deadline - Date.now(), 0);
  let readyCap;
  const settled = await Promise.race([
    Promise.all(found.map(({ doc }) => doc.fonts.ready)).then(() => true),
    new Promise((resolve) => {
      readyCap = setTimeout(() => resolve(false), Math.min(readyMs, remaining));
    }),
  ]).finally(() => clearTimeout(readyCap));
  return {
    stalled: !settled,
    documents: found.map(({ label, doc }) => {
      const faces = [];
      doc.fonts.forEach((face) => {
        faces.push({ family: face.family, status: face.status });
      });
      return { label, faces };
    }),
  };
}

export async function waitForFonts(send) {
  const result = await evaluate(
    send,
    `(${collectFontStatusInPage.toString()})(${JSON.stringify({
      pollMs: FONT_POLL_DEADLINE_MS,
      readyMs: FONT_READY_TIMEOUT_MS,
    })})`,
  );
  if (result.stalled) {
    throw new Error(
      `environment problem, not a test case failure: web fonts did not settle within ${FONT_READY_TIMEOUT_MS}ms of the ` +
        `${FONT_POLL_DEADLINE_MS}ms registration poll ending, so a font load is stalled and every text measurement ` +
        `below would race a fallback. Check that Vite is serving node_modules/@fontsource-variable/* and that no ` +
        `font request hangs.`,
    );
  }
  const documents = result.documents;
  const fontless = documents.filter((entry) => entry.faces.length === 0).map((entry) => entry.label);
  if (fontless.length > 0) {
    throw new Error(
      `${fontless.join(" and ")} declares no web fonts, so any text measured there would be taken in a host ` +
        `fallback font rather than the one the product ships - and this check would have asserted nothing ` +
        `about it. Every document on the page must declare the faces for ITSELF: a layoutguard case's top ` +
        `document needs "styles/global.css" in its case.json cssFiles, a fixture the case builds in an iframe ` +
        `needs the case's generated stylesheet linked inside that frame's OWN document, and a harness ` +
        `entrypoint needs to import ../styles/global.css.`,
    );
  }
  const failed = documents.flatMap((entry) =>
    entry.faces.filter((face) => face.status === "error").map((face) => `${face.family} in ${entry.label}`),
  );
  if (failed.length > 0) {
    throw new Error(
      `environment problem, not a test case failure: a web font this page requested failed to load (${failed.join(", ")}). ` +
        `Every text measurement below would be taken in the fallback font. Check that Vite is serving ` +
        `node_modules/@fontsource-variable/* and that global.css resolves its @font-face src.`,
    );
  }
}

/** Pin exact browser metrics for a case that must not inherit Chrome's ambient window size. */
export async function applyViewport(send, viewport) {
  await withTimeout(
    send("Emulation.setDeviceMetricsOverride", {
      width: viewport.width,
      height: viewport.height,
      deviceScaleFactor: viewport.deviceScaleFactor ?? 1,
      mobile: viewport.mobile ?? false,
      screenWidth: viewport.width,
      screenHeight: viewport.height,
    }),
    30000,
    "Emulation.setDeviceMetricsOverride",
  );
  // Metrics mobile:true alone does NOT flip the pointer media features; touch
  // emulation is what makes (pointer: coarse)/(hover: none) match, the same
  // combination DevTools' device toolbar applies. Without it the page renders
  // its fine-pointer rules and every coarse-gated tap floor is invisible.
  if (viewport.touch) {
    await withTimeout(
      send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 }),
      30000,
      "Emulation.setTouchEmulationEnabled",
    );
  }
}

/** Metrics overrides persist per target; clear between cases sharing one page. */
export async function clearViewportOverride(send) {
  await withTimeout(send("Emulation.clearDeviceMetricsOverride"), 30000, "Emulation.clearDeviceMetricsOverride").catch(
    () => {},
  );
  await withTimeout(
    send("Emulation.setTouchEmulationEnabled", { enabled: false }),
    30000,
    "Emulation.setTouchEmulationEnabled",
  ).catch(() => {});
}

/**
 * Read the realized viewport out of the page - the input to layoutguard's
 * diagnoseRealizedViewport, which turns "Chrome ignored the override" into a
 * named failure instead of a geometry mystery.
 */
export async function realizedViewport(send) {
  return evaluate(
    send,
    `JSON.stringify({
      windowInnerWidth: window.innerWidth,
      windowInnerHeight: window.innerHeight,
      documentClientWidth: document.documentElement.clientWidth,
      documentClientHeight: document.documentElement.clientHeight,
      visualViewportWidth: window.visualViewport ? window.visualViewport.width : null,
      visualViewportHeight: window.visualViewport ? window.visualViewport.height : null
    })`,
  ).then((json) => JSON.parse(json));
}

/**
 * Pin pseudo-classes ON before evaluating, via CSS.forcePseudoState (the
 * same mechanism DevTools' ":hov" toggle uses). Needed because some states
 * cannot be reached from a page script at all: there is no way to synthesize
 * a trusted hover, and a programmatic .focus() does NOT match :focus-visible
 * (measured in Chrome - it stayed unmatched at opacity 0). This pins the
 * SELECTOR match, so it proves the cascade applies the rule and nothing
 * overrides it; whether Chrome's own heuristic calls a given focus "visible"
 * is Chrome's contract, not ours. A selector that matches no element is an
 * error, never a silent no-op.
 */
export async function forcePseudoStates(send, states) {
  if (states.length === 0) return;
  await withTimeout(send("DOM.enable"), 30000, "DOM.enable");
  await withTimeout(send("CSS.enable"), 30000, "CSS.enable");
  const doc = await withTimeout(send("DOM.getDocument", { depth: -1 }), 30000, "DOM.getDocument");
  const rootId = doc.result.root.nodeId;
  for (const { selector, pseudoClasses } of states) {
    const found = await withTimeout(
      send("DOM.querySelector", { nodeId: rootId, selector }),
      30000,
      "DOM.querySelector",
    );
    // DOM.querySelector answers with nodeId 0 for "no match" rather than
    // failing - forcing nothing would leave the case measuring the resting
    // state while reporting the forced one, so it stops here instead.
    if (!found.result?.nodeId) throw new Error(`forcePseudoStates: no element matches ${selector}`);
    await withTimeout(
      send("CSS.forcePseudoState", { nodeId: found.result.nodeId, forcedPseudoClasses: pseudoClasses }),
      30000,
      "CSS.forcePseudoState",
    );
  }
}

/**
 * Belt-and-suspenders guard rail: refuse to proceed if a measurement ever
 * lands anywhere but the guard's own loopback Vite origin - above all the
 * shared evener-hub dev server on 9180 (kata 8ecz's shared-instance class of
 * bug).
 */
export async function assertGuardOrigin(send, expectedHost) {
  const origin = await evaluate(send, "location.protocol + '//' + location.host");
  if (origin.includes("9180")) {
    throw new Error("refusing: this eval landed on port 9180 (the shared evener-hub dev server)");
  }
  if (origin !== `http://${expectedHost}`) {
    throw new Error(`refusing: expected origin http://${expectedHost}, got ${origin}`);
  }
}
