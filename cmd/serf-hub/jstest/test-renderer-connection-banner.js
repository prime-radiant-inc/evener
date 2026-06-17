// Transport failure → CHROME reconnect banner, NOT the transcript (mockup #15
// Alt A, case 1). A daemon/appwire drop is Serf's fault, not the agent's, so it
// must leave the conversation entirely and dock as a chrome banner:
//   • amber "Reconnecting…" while the socket is down (recovering, not broken);
//   • escalates to red "Connection lost" if reconnection fails / gives up;
//   • clears on reconnect;
//   • glyph-paired (colorblind-safe);
//   • NO transcript error row for the transport drop;
//   • send is DISABLED while disconnected (we do not fake a queue).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

function createWindow(overrides) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body class="app">
    <header class="workspace-header" data-session-id="sess-live"></header>
    <div id="conversation" data-session-id="sess-live" data-state="active"></div>
    <form data-input-form data-session-id="sess-live">
      <textarea class="message-input"></textarea>
      <button class="send-btn" type="submit">send</button>
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/s/sess-live",
  });
  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  window.SerfAppwire = Object.assign({
    tasks: () => Promise.resolve([]),
    refForSession: (s) => "local:" + s,
    eventsFromThread: () => [],
    eventsFromNotification: () => [],
    onNotification: () => () => {},
  }, overrides);
  require("./load-renderer").evalRenderer(window);
  window.SerfRenderer.init(window.document.getElementById("conversation"));
  return window;
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const banner = (w) => w.document.querySelector(".connection-banner");
const transcriptErrors = (w) =>
  Array.from(w.document.querySelectorAll("#conversation .diagnostic, #conversation .banner.error"));

(async () => {
  // ── 1. Transport drop → amber chrome banner, no transcript error ──────────
  let lostHandler = null;
  let readThreadCalls = 0;
  const w = createWindow({
    onConnectionLost: (h) => { lostHandler = h; return () => {}; },
    onConnectionRestored: () => () => {},
    readThread: () => {
      readThreadCalls++;
      return Promise.resolve({ thread: { id: "thread-live", sessionId: "sess-live", serf: { ref: "local:thread-live" }, turns: [] } });
    },
  });
  await wait(30);
  pass(typeof lostHandler === "function", "renderer registers a connection-lost handler");

  lostHandler(new Error("connection closed"));
  await wait(10);

  const b = banner(w);
  pass(!!b, "a chrome connection banner appears on transport drop");
  pass(!!b && /reconnect/i.test(b.textContent), "the banner reads 'Reconnecting…' while the socket is down");
  pass(!!b && b.classList.contains("reconnecting"), "the banner is in the amber 'reconnecting' state");
  pass(!!b && !b.classList.contains("lost"), "the banner is NOT in the red 'lost' state while still reconnecting");
  pass(/[⟳↻⤬✕⚠◐]/.test((b && b.textContent) || ""), "the banner is glyph-paired (colorblind-safe)");
  pass(transcriptErrors(w).length === 0, "transport drop does NOT pollute the transcript with an error row (got " + transcriptErrors(w).length + ")");

  // Send is disabled while disconnected (we disable rather than fake a queue).
  const sendBtn = w.document.querySelector(".send-btn");
  pass(!!sendBtn && sendBtn.disabled, "send is disabled while disconnected");

  // ── 2. Reconnect clears the banner ────────────────────────────────────────
  await wait(300); // let scheduleAppwireReconnect → ensureLiveStream → readThread run
  pass(readThreadCalls >= 2, "a reconnect is attempted (readThread re-runs)");
  pass(!banner(w), "the banner clears once the connection is restored");

  // ── 3. Reconnection that keeps failing escalates to red "Connection lost" ──
  let lostHandler2 = null;
  const w2 = createWindow({
    onConnectionLost: (h) => { lostHandler2 = h; return () => {}; },
    onConnectionRestored: () => () => {},
    readThread: () => Promise.reject(new Error("still down")),
  });
  await wait(30);
  lostHandler2(new Error("connection closed"));
  await wait(10);
  let b2 = banner(w2);
  pass(!!b2 && b2.classList.contains("reconnecting"), "the second banner starts amber 'reconnecting'");
  // Drive failing reconnect attempts; the renderer escalates to red on give-up.
  await wait(400);
  b2 = banner(w2);
  pass(!!b2 && b2.classList.contains("lost"), "the banner escalates to red 'lost' when reconnection keeps failing");
  pass(!!b2 && /connection lost/i.test(b2.textContent), "the escalated banner reads 'Connection lost'");
  pass(transcriptErrors(w2).length === 0, "a failed reconnect still does not pollute the transcript");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: transport drop → amber→red chrome banner, no transcript pollution");
  process.exit(0);
})().catch((err) => { console.error("FAIL: " + (err && err.stack || err)); process.exit(1); });
