// Coverage for SerfRenderer.buildDiagnosticActions — verifies that the
// diagnostic card produced by appendBanner() carries the correct retry button
// for both provider-source and hub-source errors.
//
// Provider-source errors → "Retry turn" (existing behavior).
// Hub-source errors      → "Reconnect & retry" (kata e465, added once the
// hub's auto-resume layer in t65c/ws5f/xcas could relaunch a dead daemon on
// the next startTurn).  The onclick body is shared with the Retry turn flow.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const RENDERER_SRC = fs.readFileSync("../assets/renderer.js", "utf8");
const DIAGNOSTICS_SRC = fs.readFileSync("../assets/diagnostics.js", "utf8");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation"
         data-session-id="01TEST"
         data-state="ended"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({
    ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve(""),
  });

  // Load diagnostics first — renderer reads window.SerfDiagnostics.classify().
  window.eval(DIAGNOSTICS_SRC);
  window.eval(RENDERER_SRC);

  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);

  return { window, conv };
}

const failures = [];
function pass(cond, msg) { if (!cond) failures.push("FAIL: " + msg); }

(async () => {
  const { window } = newHarness();
  // Allow the init() microtasks to settle.
  await new Promise((r) => setTimeout(r, 30));

  const renderer = window.SerfRenderer;

  // ------------------------------------------------------------------
  // 1. No submitted payload → no actions even for retry-eligible sources.
  // ------------------------------------------------------------------
  renderer.lastUserText = "";
  renderer.lastSubmittedTurn = null;
  const emptyProvider = renderer.buildDiagnosticActions({
    severity: "error", source: "provider", message: "stream ended without finish event",
  });
  pass(emptyProvider === null, "expected null when submitted payload is empty, got " + JSON.stringify(emptyProvider));

  // ------------------------------------------------------------------
  // 2. provider-source error → "Retry turn" (regression guard).
  // ------------------------------------------------------------------
  renderer.lastUserText = "say hello";
  renderer.lastSubmittedTurn = { text: "say hello", items: [{ type: "image", media_type: "image/png", data: "abc", name: "shot.png" }] };
  const providerActions = renderer.buildDiagnosticActions({
    severity: "error", source: "provider", message: "stream ended without finish event",
  });
  pass(Array.isArray(providerActions) && providerActions.length === 1,
    "provider: expected single action, got " + JSON.stringify(providerActions));
  if (providerActions && providerActions.length) {
    pass(providerActions[0].label === "Retry turn",
      "provider: label should be 'Retry turn', got " + providerActions[0].label);
    pass(typeof providerActions[0].onclick === "function",
      "provider: onclick should be a function");
  }

  // ------------------------------------------------------------------
  // 3. hub-source error → "Reconnect & retry" (new in kata e465).
  // ------------------------------------------------------------------
  const hubActions = renderer.buildDiagnosticActions({
    severity: "error", source: "hub", message: "daemon spawn timed out: process exited before rendezvous",
  });
  pass(Array.isArray(hubActions) && hubActions.length === 1,
    "hub: expected single action, got " + JSON.stringify(hubActions));
  if (hubActions && hubActions.length) {
    pass(hubActions[0].label === "Reconnect & retry",
      "hub: label should be 'Reconnect & retry', got " + hubActions[0].label);
    pass(typeof hubActions[0].onclick === "function",
      "hub: onclick should be a function");
  }

  // ------------------------------------------------------------------
  // 4. Other sources (serf, ui) → no action button.
  // ------------------------------------------------------------------
  const serfActions = renderer.buildDiagnosticActions({
    severity: "error", source: "serf", message: "configuration error: unknown provider: openrouter",
  });
  pass(serfActions === null, "serf-source diagnostics should not get a retry button, got " + JSON.stringify(serfActions));

  const uiActions = renderer.buildDiagnosticActions({
    severity: "error", source: "ui", message: "render glitch",
  });
  pass(uiActions === null, "ui-source diagnostics should not get a retry button, got " + JSON.stringify(uiActions));

  const localHubActions = renderer.buildDiagnosticActions({
    severity: "error", source: "hub", message: "send failed: HTTP 409",
  });
  pass(localHubActions === null, "local hub action failures should not get reconnect retry, got " + JSON.stringify(localHubActions));

  renderer.handle("USER_INPUT", {
    text: "replayed sha image",
    images: [{ type: "image", media_type: "image/png", sha256: "sha-only", name: "replay.png" }],
  });
  pass(renderer.lastSubmittedTurn && renderer.lastSubmittedTurn.text === "replayed sha image",
    "replayed USER_INPUT should update retry text");
  pass(renderer.lastSubmittedTurn.items.length === 0,
    "sha-only replay images should not be stored as retryable attachments");

  renderer.lastUserText = "say hello";
  renderer.lastSubmittedTurn = { text: "say hello", items: [{ type: "image", media_type: "image/png", data: "abc", name: "shot.png" }] };

  // ------------------------------------------------------------------
  // 5. Hub onclick calls SerfAppwire.startTurn with the captured payload.
  //    Confirms the factored helper threads parameters through unchanged.
  // ------------------------------------------------------------------
  const startTurnCalls = [];
  let fetchCalls = 0;
  window.fetch = (...args) => {
    fetchCalls++;
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  };
  window.SerfAppwire = {
    startTurn: (ref, text, images) => {
      startTurnCalls.push({ ref, text, images });
      return Promise.resolve({ ok: true });
    },
    refForSession: (s) => "ref:" + s,
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    tasks: () => Promise.resolve([]),
    readThread: () => Promise.resolve({}),
  };
  renderer.appwireRef = "ref:01TEST";
  renderer.ensureLiveStream = () => { /* no-op for test */ };

  // Re-build actions now that SerfAppwire + appwireRef are in place — the
  // onclick body snapshots the ref at the time of construction.
  const hubActionsLive = renderer.buildDiagnosticActions({
    severity: "error", source: "hub", message: "daemon spawn timed out",
  });
  fetchCalls = 0;
  await hubActionsLive[0].onclick();
  pass(startTurnCalls.length === 1, "hub onclick should call startTurn once, got " + startTurnCalls.length);
  pass(fetchCalls === 0, "hub onclick should NOT call fetch when SerfAppwire is present, got " + fetchCalls);
  if (startTurnCalls.length) {
    pass(startTurnCalls[0].ref === "ref:01TEST", "hub onclick: wrong ref " + startTurnCalls[0].ref);
    pass(startTurnCalls[0].text === "say hello", "hub onclick: wrong text " + startTurnCalls[0].text);
    pass(Array.isArray(startTurnCalls[0].images) && startTurnCalls[0].images.length === 1,
      "hub onclick: images should include submitted attachment payload");
    pass(startTurnCalls[0].images[0].name === "shot.png",
      "hub onclick: wrong image payload " + JSON.stringify(startTurnCalls[0].images[0]));
  }

  // ------------------------------------------------------------------
  // 6. Verify failure-banner wording adapts to the source label.
  //    When startTurn rejects, hub-source should produce "reconnect failed:".
  // ------------------------------------------------------------------
  const banners = [];
  const origAppendBanner = renderer.appendBanner.bind(renderer);
  renderer.appendBanner = (kind, text, diagnostic) => { banners.push({ kind, text, diagnostic }); };

  window.SerfAppwire.startTurn = () => Promise.reject(new Error("dial: connection refused"));
  const hubActionsFail = renderer.buildDiagnosticActions({
    severity: "error", source: "hub", message: "daemon spawn timed out",
  });
  await hubActionsFail[0].onclick();
  pass(banners.length === 1, "hub failure should append exactly one banner, got " + banners.length);
  if (banners.length) {
    pass(banners[0].text.indexOf("reconnect failed:") === 0,
      "hub failure banner should start with 'reconnect failed:', got " + banners[0].text);
    pass(banners[0].diagnostic && banners[0].diagnostic.title === "Reconnect error",
      "hub failure title should be 'Reconnect error', got " + (banners[0].diagnostic && banners[0].diagnostic.title));
  }

  // And provider-source failure still says "retry failed:".
  banners.length = 0;
  const providerActionsFail = renderer.buildDiagnosticActions({
    severity: "error", source: "provider", message: "stream ended without finish event",
  });
  await providerActionsFail[0].onclick();
  pass(banners.length === 1, "provider failure should append exactly one banner, got " + banners.length);
  if (banners.length) {
    pass(banners[0].text.indexOf("retry failed:") === 0,
      "provider failure banner should start with 'retry failed:', got " + banners[0].text);
    pass(banners[0].diagnostic && banners[0].diagnostic.title === "Retry error",
      "provider failure title should be 'Retry error', got " + (banners[0].diagnostic && banners[0].diagnostic.title));
  }

  renderer.appendBanner = origAppendBanner;

  // ------------------------------------------------------------------
  // 7. SerfAppwire absent at click time → diagnostic banner, no fetch.
  //    Kata 05vb dropped the /send legacy fallback: the retry path now
  //    requires appwire because /send doesn't trigger hub auto-resume.
  // ------------------------------------------------------------------
  const fallbackBanners = [];
  renderer.appendBanner = (kind, text, diagnostic) => {
    fallbackBanners.push({ kind, text, diagnostic });
  };
  delete window.SerfAppwire;
  fetchCalls = 0;
  const hubActionsNoAppwire = renderer.buildDiagnosticActions({
    severity: "error", source: "hub", message: "daemon spawn timed out",
  });
  await hubActionsNoAppwire[0].onclick();
  pass(fetchCalls === 0,
    "no-appwire onclick must NOT call fetch (dead /send fallback), got " + fetchCalls + " calls");
  pass(fallbackBanners.length === 1,
    "no-appwire onclick should surface a diagnostic banner, got " + fallbackBanners.length);
  if (fallbackBanners.length) {
    pass(fallbackBanners[0].text.indexOf("reconnect failed:") === 0,
      "no-appwire banner should start with 'reconnect failed:', got " + fallbackBanners[0].text);
    pass(fallbackBanners[0].text.toLowerCase().indexOf("appwire unavailable") !== -1,
      "no-appwire banner should mention 'appwire unavailable', got " + fallbackBanners[0].text);
    pass(fallbackBanners[0].diagnostic && fallbackBanners[0].diagnostic.title === "Reconnect error",
      "no-appwire banner title should be 'Reconnect error', got " + (fallbackBanners[0].diagnostic && fallbackBanners[0].diagnostic.title));
  }

  // And provider-source with no appwire uses the retry-flavoured wording.
  fallbackBanners.length = 0;
  fetchCalls = 0;
  const providerActionsNoAppwire = renderer.buildDiagnosticActions({
    severity: "error", source: "provider", message: "stream ended without finish event",
  });
  await providerActionsNoAppwire[0].onclick();
  pass(fetchCalls === 0,
    "no-appwire provider onclick must NOT call fetch, got " + fetchCalls + " calls");
  pass(fallbackBanners.length === 1 && fallbackBanners[0].text.indexOf("retry failed:") === 0,
    "no-appwire provider banner should start with 'retry failed:', got " + (fallbackBanners[0] && fallbackBanners[0].text));

  renderer.appendBanner = origAppendBanner;

  if (failures.length === 0) {
    console.log("PASS: buildDiagnosticActions covers provider + hub sources");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
