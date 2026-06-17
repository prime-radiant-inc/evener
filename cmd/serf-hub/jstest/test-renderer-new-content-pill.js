// New-content pill: when frames append visible content while the reader is
// scrolled up, a floating "↓ N new" pill appears near the bottom of the
// transcript. N counts the transcript entries that actually rendered since the
// reader left the bottom. Clicking it (or scrolling back to bottom) clears it.
// When new content needs the user (awaiting state / agent question), the pill
// reads "↓ needs you" in the attention color.
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <main id="workspace">
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
    </form>
  </main>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
// jsdom has no layout, so fake the scroll metrics. scrollTop is writable.
Object.defineProperty(conv, "scrollHeight", { configurable: true, get: () => 1000 });
Object.defineProperty(conv, "clientHeight", { configurable: true, get: () => 400 });

window.SerfRenderer.init(conv);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const R = window.SerfRenderer;
const doc = window.document;
const pillEl = () => doc.querySelector("[data-new-content-pill]");
const pillVisible = () => { const p = pillEl(); return !!p && !p.hidden; };
// The plain count is committed on a ~300ms trailing debounce, so settle past it
// before asserting on the number.
const settle = () => new Promise((r) => setTimeout(r, 350));

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // flush the cold-load buffer

  // No pill at the start (reader is at the bottom, nothing new).
  pass(!pillVisible(), "no pill before any new content");

  // Reader has scrolled up (far from bottom: 1000 - 100 - 400 = 500 > 50).
  conv.scrollTop = 100;
  // Two rendered entries arrive while the reader is up.
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "first reply" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "second reply" });

  pass(pillVisible(), "pill appears when content arrives while scrolled up");
  await settle(); // let the debounced count commit
  pass(pillEl() && /2/.test(pillEl().textContent), "pill counts 2 new entries (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && /new/.test(pillEl().textContent), "pill reads '↓ N new' (got " + (pillEl() && pillEl().textContent) + ")");
  pass(conv.scrollTop === 100, "appending new content must not move the viewport while scrolled up");

  // Suppressed/no-op events must NOT bump the counter (communicate is dropped).
  const before = pillEl().textContent;
  R.handleData("COMMUNICATE", { message: "" });
  await settle();
  pass(pillEl().textContent === before, "no-op events don't bump the counter (was " + before + ", now " + pillEl().textContent + ")");

  // Clicking the pill scrolls to the bottom and clears it.
  pillEl().click();
  pass(conv.scrollTop === 1000, "clicking the pill scrolls to the bottom (got " + conv.scrollTop + ")");
  pass(!pillVisible(), "clicking the pill clears it");

  // New content while at the bottom: pill stays hidden, viewport sticks.
  conv.scrollTop = 600; // 1000 - 600 - 400 = 0 < 50 -> at bottom
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "third reply" });
  pass(!pillVisible(), "no pill when content arrives while at the bottom");
  pass(conv.scrollTop === 1000, "content sticks to bottom when reader is there");

  // Pill reappears when scrolled up; scrolling back to the bottom clears it.
  conv.scrollTop = 100;
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "fourth reply" });
  pass(pillVisible(), "pill reappears on new content after scrolling up again");
  conv.scrollTop = 600; // reader returns to the bottom on their own
  conv.dispatchEvent(new window.Event("scroll"));
  pass(!pillVisible(), "scrolling back to the bottom clears the pill");

  // Attention-aware: new content under an awaiting state reads "needs you".
  R.updateThreadState("awaiting");
  conv.scrollTop = 100;
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "a question for you?" });
  pass(pillVisible(), "pill appears for attention content while scrolled up");
  pass(pillEl() && /needs you/.test(pillEl().textContent), "pill reads '↓ needs you' under awaiting (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && pillEl().classList.contains("needs-you"), "attention pill carries the needs-you class");

  // The awaiting flip can arrive in a separate frame after the content that
  // rendered the question: the visible "N new" pill must upgrade to "needs you".
  R.scrollToBottom();
  R.updateThreadState("active");
  conv.scrollTop = 100;
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "should I proceed?" });
  pass(pillEl() && /new/.test(pillEl().textContent), "pill is a plain count before the awaiting flip (got " + (pillEl() && pillEl().textContent) + ")");
  R.handleData("THREAD_STATUS_CHANGED", { status: "awaiting" });
  pass(pillEl() && /needs you/.test(pillEl().textContent), "pill upgrades to '↓ needs you' when awaiting arrives after the content (got " + (pillEl() && pillEl().textContent) + ")");

  // ── Glyph-paired plain count (mockup #14) ────────────────────────────────
  R.scrollToBottom();
  R.updateThreadState("active");
  conv.scrollTop = 100;
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "a plain reply" });
  await settle();
  pass(pillEl() && /↓/.test(pillEl().textContent), "plain count is glyph-paired with ↓ (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && /↓ 1 new/.test(pillEl().textContent), "plain count reads '↓ N new' (got " + (pillEl() && pillEl().textContent) + ")");

  // ── Error anchor below the fold → red "✕ error" pill ─────────────────────
  // Place an errored tool row well below the viewport bottom.
  R.scrollToBottom();
  R.updateThreadState("active");
  const errBelow = doc.createElement("div");
  errBelow.className = "tool-call shell";
  errBelow.dataset.attention = "error";
  Object.defineProperty(errBelow, "offsetTop", { configurable: true, get: () => 900 });
  Object.defineProperty(errBelow, "offsetHeight", { configurable: true, get: () => 40 });
  conv.appendChild(errBelow);
  conv.scrollTop = 100; // viewport bottom = 100 + 400 = 500; error at 900 is below
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "patching" });
  pass(pillEl() && /✕/.test(pillEl().textContent), "error below the fold gives a ✕-glyph pill (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && /error/.test(pillEl().textContent), "error pill reads 'error' (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && pillEl().classList.contains("error"), "error pill carries the error class");
  pass(pillEl() && /↓/.test(pillEl().textContent), "arrow points ↓ to an error below the viewport (got " + (pillEl() && pillEl().textContent) + ")");

  // ── Arrow flips to ↑ when the urgent anchor is above the viewport ────────
  conv.scrollTop = 960; // viewport top = 960; error at 900 (+40) ends above it
  conv.dispatchEvent(new window.Event("scroll"));
  // Re-trigger a paint with the reader still off the bottom (1000-960-400<0 → near bottom).
  // Move just off the bottom so the pill stays up but the error is above.
  Object.defineProperty(conv, "scrollHeight", { configurable: true, get: () => 1600 });
  conv.scrollTop = 950; // bottom = 950+400 = 1350; far from 1600 → pill shows; error ends at 940 above top 950
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "more output" });
  pass(pillEl() && /↑/.test(pillEl().textContent), "arrow flips to ↑ when the error is above the viewport (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && /✕/.test(pillEl().textContent), "error pill stays ✕ when scrolled past it (got " + (pillEl() && pillEl().textContent) + ")");

  // restore the original scrollHeight and clear the error anchor for later cases
  Object.defineProperty(conv, "scrollHeight", { configurable: true, get: () => 1000 });
  errBelow.remove();
  R.scrollToBottom();

  // ── Priority: error wins over needs-you ─────────────────────────────────
  R.updateThreadState("awaiting"); // session needs you …
  const errPri = doc.createElement("div");
  errPri.className = "tool-call shell";
  errPri.dataset.attention = "error";
  Object.defineProperty(errPri, "offsetTop", { configurable: true, get: () => 900 });
  Object.defineProperty(errPri, "offsetHeight", { configurable: true, get: () => 40 });
  conv.appendChild(errPri); // … AND an error is below
  conv.scrollTop = 100;
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_END", { text: "a reply" });
  pass(pillEl() && /✕/.test(pillEl().textContent), "error outranks needs-you (got " + (pillEl() && pillEl().textContent) + ")");
  pass(pillEl() && pillEl().classList.contains("error") && !pillEl().classList.contains("needs-you"),
    "priority pill is error, not needs-you");
  errPri.remove();
  R.scrollToBottom();
  R.updateThreadState("active");

  // ── Errored tool rows get the data-attention hook on finalize ───────────
  R.handleData("TOOL_CALL_START", { call_id: "c-err", tool_name: "shell", arguments_json: "{}" });
  R.handleData("TOOL_CALL_END", { call_id: "c-err", error: "boom" });
  const erroredRow = conv.querySelector('.tool-call[data-attention="error"]');
  pass(!!erroredRow, "a finalized errored tool call is marked data-attention=error");
  R.handleData("TOOL_CALL_START", { call_id: "c-ok", tool_name: "shell", arguments_json: "{}" });
  R.handleData("TOOL_CALL_END", { call_id: "c-ok", output: "fine" });
  const okState = conv.querySelectorAll('.tool-call[data-attention="error"]').length;
  pass(okState === 1, "a successful tool call is NOT marked as an error anchor (got " + okState + ")");

  // ── Debounced count settles to one value after a burst ──────────────────
  // Remove the lingering error anchors so this exercises the PLAIN count path.
  conv.querySelectorAll('.tool-call[data-attention="error"]').forEach(n => n.remove());
  R.scrollToBottom();
  R.updateThreadState("active");
  conv.scrollTop = 100;
  // A burst of appends in quick succession.
  for (let i = 0; i < 4; i++) {
    R.handleData("ASSISTANT_TEXT_START", {});
    R.handleData("ASSISTANT_TEXT_END", { text: "burst " + i });
  }
  // Mid-burst (before the trailing debounce fires) the painted number has NOT
  // yet jumped to the final total — that is the anti-jitter guarantee.
  const midBurst = pillEl() && pillEl().textContent;
  pass(!/↓ 4 new/.test(midBurst || ""), "count does not repaint to the final total mid-burst (got " + midBurst + ")");
  // After the debounce window the committed count settles to one value.
  await new Promise((r) => setTimeout(r, 400));
  const settled = pillEl() && pillEl().textContent;
  pass(/↓ 4 new/.test(settled || ""), "debounced count settles to the final burst total (got " + settled + ")");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: new-content pill counts, clears, and goes attention-aware");
  process.exit(0);
})();
