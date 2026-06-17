// Blocking "needs-you" — the agent question (mockup #16, Alt A + Alt C).
//
// When the agent calls communicate with await_reply=true, the projected
// agentMessage carries awaitReply. The web must:
//   • render that message inside an amber ◆ "Needs you" container (Alt A) —
//     the literal question, framed so it can't be mistaken for hero prose;
//   • a NORMAL agentMessage must stay a plain assistant block;
//   • when the unanswered question is off-screen OR the session is awaiting,
//     show a docked amber bar above the composer ("◆ … waiting on your
//     answer") that scrolls to the question and focuses the composer (Alt C);
//   • coordinate with the Pass-4 new-content pill: while the dock owns the
//     needs-you signal, the pill must NOT also paint "↓ ◆ needs you".
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <main id="workspace">
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form class="workspace-input" data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
      <button class="send-btn" type="submit">send</button>
    </form>
  </main>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

// Load the appwire wire-translation layer so we can assert the per-item flag
// is forwarded onto the ASSISTANT_TEXT_END event payload.
const appwireSrc = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");
window.eval(appwireSrc);

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
// jsdom has no layout; fake the scroll metrics. scrollTop is writable.
Object.defineProperty(conv, "scrollHeight", { configurable: true, get: () => 1000 });
Object.defineProperty(conv, "clientHeight", { configurable: true, get: () => 400 });

window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;
const doc = window.document;
const dockEl = () => doc.querySelector("[data-needs-you-dock]");
const dockVisible = () => { const d = dockEl(); return !!d && !d.hidden; };
const pillEl = () => doc.querySelector("[data-new-content-pill]");

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // flush the cold-load buffer

  // ── appwire forwards the per-item awaitReply flag ────────────────────────
  const evs = window.SerfAppwire.eventsFromNotification("item/completed", {
    item: { type: "agentMessage", id: "a1", text: "Which DB?", status: "completed", awaitReply: true },
  });
  const end = evs.find((e) => e[0] === "ASSISTANT_TEXT_END");
  pass(!!end && end[1] && end[1].awaitReply === true,
    "appwire forwards awaitReply onto ASSISTANT_TEXT_END (got " + JSON.stringify(evs) + ")");

  const evsPlain = window.SerfAppwire.eventsFromNotification("item/completed", {
    item: { type: "agentMessage", id: "a2", text: "Done.", status: "completed" },
  });
  const endPlain = evsPlain.find((e) => e[0] === "ASSISTANT_TEXT_END");
  pass(!!endPlain && !endPlain[1].awaitReply,
    "appwire omits awaitReply for an ordinary message (got " + JSON.stringify(evsPlain) + ")");

  // ── Alt A: an agent question renders inside an amber ◆ container ──────────
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "Which database should the migration target?", awaitReply: true });
  const q = conv.querySelector(".assistant-message.agent-question");
  pass(!!q, "an awaitReply message renders as an .agent-question container");
  pass(!!q && /◆/.test(q.textContent), "the agent-question container is glyph-paired with ◆");
  pass(!!q && /migration target/.test(q.textContent), "the literal question text is shown verbatim");

  // ── A normal agent message is a plain assistant block, not a question ────
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "Here is the plan." });
  const plains = conv.querySelectorAll(".assistant-message:not(.agent-question)");
  const lastPlain = plains[plains.length - 1];
  pass(!!lastPlain && /Here is the plan/.test(lastPlain.textContent),
    "a normal agentMessage stays a plain assistant block");
  pass(conv.querySelectorAll(".assistant-message.agent-question").length === 1,
    "a normal agentMessage does NOT become an agent-question (got " +
    conv.querySelectorAll(".assistant-message.agent-question").length + ")");

  // ── Alt C: docked bar appears when the question is off-screen / awaiting ──
  // Mark the question element as off-screen above the viewport.
  if (q) {
    Object.defineProperty(q, "offsetTop", { configurable: true, get: () => 10 });
    Object.defineProperty(q, "offsetHeight", { configurable: true, get: () => 40 });
  }
  R.updateThreadState("awaiting");
  conv.scrollTop = 600; // viewport top 600 > question bottom 50 → off-screen above
  conv.dispatchEvent(new window.Event("scroll"));
  pass(dockVisible(), "the docked needs-you bar appears when the question is off-screen / awaiting");
  pass(!!dockEl() && /◆/.test(dockEl().textContent), "the dock is glyph-paired with ◆");
  pass(!!dockEl() && /waiting/i.test(dockEl().textContent), "the dock says the agent is waiting on your answer");

  // The dock is the authoritative blocking signal: the pill must NOT also
  // claim the needs-you label while the dock is showing.
  pass(!pillEl() || !pillEl().classList.contains("needs-you"),
    "the new-content pill defers to the dock (no duplicate needs-you signal)");

  // ── Clicking the dock scrolls to the question and focuses the composer ────
  if (dockEl()) dockEl().click();
  pass(conv.scrollTop <= 50, "clicking the dock scrolls to the question (got " + conv.scrollTop + ")");
  const ta = doc.querySelector(".message-input");
  pass(doc.activeElement === ta, "clicking the dock focuses the composer to reply");

  // ── Answering (a user message) clears the live dock ──────────────────────
  // The in-flow amber frame stays as a record of the resolved exchange, but the
  // "awaiting your answer" dock is torn down.
  R.handleData("USER_INPUT", { text: "The primary Postgres." });
  R.updateThreadState("active");
  pass(!dockVisible(), "the dock clears once the user has answered");
  // Scrolling away from the (now-resolved) frame must NOT resurrect the dock.
  conv.scrollTop = 600;
  conv.dispatchEvent(new window.Event("scroll"));
  pass(!dockVisible(), "the resolved question does not re-trigger the dock when scrolled away");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: agent-question amber container + docked needs-you bar");
  process.exit(0);
})();
