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
// Origin discipline (kata 8ecz): guards never touch the shared serf-hub dev
// server on 9180 or the shared MCP Chrome. Every page load is pinned to the
// guard's OWN loopback Vite origin by assertGuardOrigin.

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
    })
  ]).finally(() => clearTimeout(timer));
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
 */
export async function waitForHttp(url, label, launchFailed = () => null) {
  for (let attempt = 0; attempt < 300; attempt++) {
    const launchError = launchFailed();
    if (launchError) throw launchError;
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      // The child process is still starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${label} never came up at ${url}`);
}

/**
 * Find Chrome's page target over CDP and open a command channel to it.
 * send() rejects on a CDP error response so a failing command can never read
 * as a successful measurement. Callers close() in a finally.
 */
export async function connectPage(cdpPort) {
  const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
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

/**
 * Page.enable, navigate, and await the load event - the triple every guard
 * re-wrote.
 *
 * The listener comes off in a finally, not only on the load event: on the
 * timeout path the load never fires, and a handler left behind keeps parsing
 * every later CDP message on a socket the guards reuse across cases.
 */
export async function navigateTo({ ws, send }, url) {
  await withTimeout(send("Page.enable"), 30000, "Page.enable");
  let handler;
  let abandonLoad;
  const loaded = withTimeout(new Promise((resolve) => {
    abandonLoad = resolve;
    handler = (event) => {
      if (JSON.parse(event.data).method === "Page.loadEventFired") resolve();
    };
    ws.addEventListener("message", handler);
  }), 30000, "navigateTo").finally(() => ws.removeEventListener("message", handler));
  try {
    await withTimeout(send("Page.navigate", { url }), 30000, "Page.navigate");
  } catch (error) {
    // The navigate never happened, so nothing will ever load. Settling the wait
    // here is what takes the listener off and clears its timer: left pending it
    // would hold the guard's event loop open for another 30 seconds and then
    // reject with nobody awaiting it, which Node turns into a crash that buries
    // the error being thrown right here.
    abandonLoad();
    await loaded.catch(() => {});
    throw error;
  }
  await loaded;
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
    "Runtime.evaluate"
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
 */
export async function waitForFonts(send) {
  const documents = await evaluate(
    send,
    `(async () => {
       const found = [{ label: "the top document", doc: document }];
       const frames = document.querySelectorAll("iframe");
       for (let index = 0; index < frames.length; index++) {
         try {
           const doc = frames[index].contentDocument;
           if (doc) found.push({ label: "iframe #" + index + " (" + (frames[index].className || "no class") + ")", doc });
         } catch {
           // Cross-origin: not reachable, and not something a guard builds.
         }
       }
       await Promise.all(found.map(({ doc }) => doc.fonts.ready));
       return found.map(({ label, doc }) => {
         const faces = [];
         doc.fonts.forEach((face) => faces.push({ family: face.family, status: face.status }));
         return { label, faces };
       });
     })()`,
  );
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
    "Emulation.setDeviceMetricsOverride"
  );
}

/** Metrics overrides persist per target; clear between cases sharing one page. */
export async function clearViewportOverride(send) {
  await withTimeout(send("Emulation.clearDeviceMetricsOverride"), 30000, "Emulation.clearDeviceMetricsOverride").catch(() => {});
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
    const found = await withTimeout(send("DOM.querySelector", { nodeId: rootId, selector }), 30000, "DOM.querySelector");
    // DOM.querySelector answers with nodeId 0 for "no match" rather than
    // failing - forcing nothing would leave the case measuring the resting
    // state while reporting the forced one, so it stops here instead.
    if (!found.result?.nodeId) throw new Error(`forcePseudoStates: no element matches ${selector}`);
    await withTimeout(send("CSS.forcePseudoState", { nodeId: found.result.nodeId, forcedPseudoClasses: pseudoClasses }), 30000, "CSS.forcePseudoState");
  }
}

/**
 * Belt-and-suspenders guard rail: refuse to proceed if a measurement ever
 * lands anywhere but the guard's own loopback Vite origin - above all the
 * shared serf-hub dev server on 9180 (kata 8ecz's shared-instance class of
 * bug).
 */
export async function assertGuardOrigin(send, expectedHost) {
  const origin = await evaluate(send, "location.protocol + '//' + location.host");
  if (origin.includes("9180")) {
    throw new Error("refusing: this eval landed on port 9180 (the shared serf-hub dev server)");
  }
  if (origin !== `http://${expectedHost}`) {
    throw new Error(`refusing: expected origin http://${expectedHost}, got ${origin}`);
  }
}
