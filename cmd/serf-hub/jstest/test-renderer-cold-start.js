// Cold-start / empty session (mockup #21: A optimistic echo + B skeleton +
// C welcome pane). Uses ONLY real signals:
//   - empty session (no messages) → crafted welcome pane w/ example prompts;
//   - clicking an example prefills the composer;
//   - on send (appendLocalUserMessage) the welcome dissolves and a faint
//     skeleton placeholder + calm "starting…" liveness stand in the gap;
//   - ASSISTANT_TEXT_START (first real frame) removes the skeleton.
// No invented multi-stage narration (no "Connecting…→Thinking…" stage labels).
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST">
    <span class="status-dot" data-state="idle"></span>
  </header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const doc = window.document;
const conv = doc.getElementById("conversation");
window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;
const ta = doc.querySelector(".message-input");

function feed(kind, data) {
  R.handleData ? R.handleData(kind, data || {}) : R.handle(kind, { data: JSON.stringify(data || {}) });
}

(async () => {
  await new Promise((r) => setTimeout(r, 40)); // let cold-load hydration drain

  // ── C. Empty session renders a crafted welcome pane ──────────────────────
  let welcome = conv.querySelector(".cold-start-welcome");
  pass(welcome, "an empty session renders the welcome pane (not a void)");
  const examples = welcome ? welcome.querySelectorAll(".cold-start-example") : [];
  pass(examples.length >= 2, "the welcome offers 2+ example-prompt buttons (got " + examples.length + ")");

  // Clicking an example prefills the composer textarea.
  if (examples.length) {
    const expected = examples[0].dataset.prompt || examples[0].textContent.trim();
    examples[0].click();
    pass(ta.value === expected,
      "clicking an example prefills the composer (got '" + ta.value + "')");
  }

  // ── A + skeleton wiring. On send, the welcome dissolves and a faint ──────
  // skeleton placeholder + calm "starting…" liveness stand in the gap.
  R.appendLocalUserMessage("audit error handling", [], "turn-1", R.userMessageCount());
  pass(!conv.querySelector(".cold-start-welcome"),
    "the welcome pane is gone after a user send");
  const userMsg = conv.querySelector(".user-message");
  pass(userMsg, "the user's message is echoed immediately (optimistic echo)");

  let skeleton = conv.querySelector(".cold-start-skeleton");
  pass(skeleton, "a skeleton placeholder stands in the send→first-frame gap");
  // The skeleton reuses the EXISTING skeleton grammar (.skeleton-line classes).
  pass(skeleton && skeleton.querySelector(".skeleton-line"),
    "the skeleton reuses the existing skeleton-line grammar");
  // A calm neutral "starting…" liveness line with the single breathing dot —
  // NOT prose, NOT a multi-stage narration.
  const starting = skeleton && skeleton.querySelector(".cold-start-starting");
  pass(starting, "a calm 'starting…' liveness line stands where the first frame lands");
  pass(starting && /starting/i.test(starting.textContent),
    "the liveness line reads 'starting…' (got '" + (starting && starting.textContent) + "')");
  // Honesty: no fabricated stage labels.
  pass(skeleton && !/connecting/i.test(skeleton.textContent),
    "no invented 'Connecting…' stage label (honest: only real signals)");

  // ── TURN_STARTED is a real signal and keeps the gap state ────────────────
  feed("TURN_STARTED", { turnId: "turn-1" });
  pass(conv.querySelector(".cold-start-skeleton"),
    "the skeleton persists through TURN_STARTED (still no first frame)");

  // ── B. The first real frame (ASSISTANT_TEXT_START) removes the skeleton ──
  feed("ASSISTANT_TEXT_START", {});
  pass(!conv.querySelector(".cold-start-skeleton"),
    "the skeleton is removed when the first real frame arrives (ASSISTANT_TEXT_START)");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: cold-start welcome pane + skeleton gap + optimistic echo");
  process.exit(0); // renderer pollers keep the loop alive otherwise
})();
