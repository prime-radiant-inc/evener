// M7 in-UI sandbox-escalation card: (1) appwire.js maps the wire notification to
// a SANDBOX_ESCALATION_REQUESTED client event, and (2) the renderer renders a
// HARNESS-framed approval card (never a model message) whose Allow/Deny buttons
// post serf/sandbox/escalation/resolve with the right decision.
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

// --- 1. Wire mapping: appwire.js's eventsFromNotification ------------------
{
  const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
  const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, {
    runScripts: "outside-only",
    url: "http://127.0.0.1:9180/s/01S",
  });
  dom.window.eval(appwireSrc);

  const out = dom.window.SerfAppwire.eventsFromNotification("serf/sandbox/escalation/requested", {
    escalationId: "esc_1", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/etc/hosts",
  });
  assert.strictEqual(out.length, 1, "notification must map to exactly one event, got " + JSON.stringify(out));
  assert.strictEqual(out[0][0], "SANDBOX_ESCALATION_REQUESTED", "wrong event kind: " + JSON.stringify(out[0]));
  assert.strictEqual(out[0][1].escalationId, "esc_1", "must carry escalationId");
  assert.strictEqual(out[0][1].deniedPath, "/etc/hosts", "must carry the full deniedPath");
  assert.strictEqual(out[0][1].kind, "file_tool", "must carry the card kind");
}

// --- 2. Renderer card + Allow/Deny post the resolve -----------------------
(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01S"></header>
    <div id="conversation" data-session-id="01S" data-state="active"></div>
    <form data-input-form data-session-id="01S"><textarea class="message-input"></textarea></form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/s/01S",
  });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });

  let notify = null;
  const resolveCalls = [];
  window.SerfAppwire = {
    refForSession: (id) => "local:" + id,
    onNotification: (cb) => { notify = cb; return () => {}; },
    onConnectionLost: () => () => {},
    readThread: () => Promise.resolve({
      thread: { id: "01S", sessionId: "01S", serf: { ref: "local:01S" }, status: { type: "active" }, queue: { depth: 0, preview: [] }, turns: [] },
    }),
    eventsFromThread: () => [],
    eventsFromNotification: (method, params) => {
      if (method === "serf/sandbox/escalation/requested") return [["SANDBOX_ESCALATION_REQUESTED", params]];
      return [];
    },
    activeTurnIDFromThread: () => "",
    tasks: () => Promise.resolve([]),
    resolveSandboxEscalation: (sessionId, escalationId, approve) => {
      resolveCalls.push({ sessionId, escalationId, approve });
      // esc_conflict: a genuine conflict (serfErrorInfo="conflict") — already resolved
      // / not pending → terminal "expired".
      if (escalationId === "esc_conflict") {
        const e = new Error("not pending"); e.code = -32013; e.serfErrorInfo = "conflict"; return Promise.reject(e);
      }
      // esc_unavailable: a CODED but NON-conflict error (daemon temporarily
      // unavailable) — the escalation is still pending → retry, NOT terminal.
      if (escalationId === "esc_unavailable") {
        const e = new Error("daemon unavailable"); e.code = -32001; e.serfErrorInfo = "sessionUnavailable"; return Promise.reject(e);
      }
      // esc_transport: a TRANSPORT error (no code, no serfErrorInfo) → retry.
      if (escalationId === "esc_transport") return Promise.reject(new Error("connection lost"));
      return Promise.resolve();
    },
  };

  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.ok(notify, "renderer did not subscribe to AppWire notifications");

  // Deliver a file-tool escalation and inspect the card.
  notify("serf/sandbox/escalation/requested", {
    escalationId: "esc_1", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/etc/hosts",
  });
  const card = window.document.querySelector(".sandbox-escalation");
  assert.ok(card, "escalation card must render");
  assert.ok(/requested by serf/i.test(card.textContent),
    "card must be labelled a HARNESS prompt (not a model message): " + JSON.stringify(card.textContent));
  assert.ok(card.textContent.includes("/etc/hosts"), "card must show the FULL denied path for informed consent");
  assert.ok(card.textContent.includes("read_file"), "card must name the denied tool");
  // The file-tool card must NOT carry the shell "partially ran" caveat.
  assert.ok(!/partially ran/i.test(card.textContent), "a file-tool card must not show the shell partial-run caveat");

  const allow = card.querySelector(".sandbox-escalation-allow");
  const deny = card.querySelector(".sandbox-escalation-deny");
  assert.ok(allow && deny, "card must have Allow and Deny controls");

  allow.click();
  assert.strictEqual(resolveCalls.length, 1, "Allow must post exactly one resolve");
  assert.strictEqual(resolveCalls[0].escalationId, "esc_1", "resolve must carry the escalation id");
  assert.strictEqual(resolveCalls[0].approve, true, "Allow must post approve:true");
  // The card settles only AFTER the resolve request SUCCEEDS (not optimistically).
  await Promise.resolve();
  await Promise.resolve();
  assert.ok(/Allowed once/.test(card.textContent), "card must settle to the decision on resolve success");
  assert.ok(!card.querySelector(".sandbox-escalation-allow"), "the Allow/Deny controls must be gone after settling");

  // A second escalation, denied, must post approve:false (never silently dropped).
  notify("serf/sandbox/escalation/requested", {
    escalationId: "esc_2", mode: "read-only", tool: "write_file", kind: "file_tool", deniedPath: "/etc/passwd",
  });
  const cards = window.document.querySelectorAll(".sandbox-escalation");
  const card2 = cards[cards.length - 1];
  card2.querySelector(".sandbox-escalation-deny").click();
  assert.strictEqual(resolveCalls.length, 2, "Deny must also post a resolve");
  assert.strictEqual(resolveCalls[1].escalationId, "esc_2");
  assert.strictEqual(resolveCalls[1].approve, false, "Deny must post approve:false");

  // A CONFLICT rejection (daemon error response, already resolved) renders a DISTINCT
  // terminal expired state — not a confirmation for a no-op.
  notify("serf/sandbox/escalation/requested", {
    escalationId: "esc_conflict", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/etc/shadow",
  });
  let allCards = window.document.querySelectorAll(".sandbox-escalation");
  const cardC = allCards[allCards.length - 1];
  cardC.querySelector(".sandbox-escalation-allow").click();
  await Promise.resolve();
  await Promise.resolve();
  assert.ok(cardC.querySelector(".sandbox-escalation-expired"), "a conflict rejection must render the terminal expired state");
  assert.ok(!/Allowed once/.test(cardC.textContent), "a conflict rejection must NOT show a success confirmation");

  // A TRANSPORT rejection (no error code) must NOT settle — it re-enables the
  // buttons and shows a transient note so the still-pending escalation stays answerable.
  notify("serf/sandbox/escalation/requested", {
    escalationId: "esc_transport", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/etc/gshadow",
  });
  allCards = window.document.querySelectorAll(".sandbox-escalation");
  const cardT = allCards[allCards.length - 1];
  cardT.querySelector(".sandbox-escalation-allow").click();
  await Promise.resolve();
  await Promise.resolve();
  assert.ok(!cardT.querySelector(".sandbox-escalation-expired"), "a transport error must NOT mark the card expired");
  const allowT = cardT.querySelector(".sandbox-escalation-allow");
  assert.ok(allowT && !allowT.disabled, "a transport error must re-enable the Allow button for retry");
  assert.ok(cardT.querySelector(".sandbox-escalation-note"), "a transport error must show a transient retry note");

  // A CODED but NON-conflict rejection (daemon temporarily unavailable) must ALSO
  // route to retry — NOT terminally expired (the escalation is still pending).
  notify("serf/sandbox/escalation/requested", {
    escalationId: "esc_unavailable", mode: "read-only", tool: "read_file", kind: "file_tool", deniedPath: "/etc/group",
  });
  allCards = window.document.querySelectorAll(".sandbox-escalation");
  const cardU = allCards[allCards.length - 1];
  cardU.querySelector(".sandbox-escalation-allow").click();
  await Promise.resolve();
  await Promise.resolve();
  assert.ok(!cardU.querySelector(".sandbox-escalation-expired"), "a daemon-unavailable (coded non-conflict) error must NOT be terminal");
  const allowU = cardU.querySelector(".sandbox-escalation-allow");
  assert.ok(allowU && !allowU.disabled, "a daemon-unavailable error must re-enable the Allow button for retry");

  console.log("PASS test-sandbox-escalation.js");
  process.exit(0);
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
