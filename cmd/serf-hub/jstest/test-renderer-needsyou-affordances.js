// Regression: the needs-you affordances (scroll-nudge pill, off-screen dock,
// Esc-collapsed ask chip, settled-ask summary line, ask-header glyph) used
// literal "◆"/"✕" characters instead of the unified SerfIcons set adopted
// elsewhere in renderer.js (sidebar dots, notifications, connection banner,
// subagent glyphs, plan/task glyphs — see Task 7/9-14). Each site's
// .textContent assignment moves to .innerHTML with the matching icon key,
// except the settled-ask line: askedSummary/echo carry raw, unescaped
// question-header and reply text, so that site must build the icon and text
// as separate DOM nodes rather than string-concatenate into innerHTML.
const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div class="conversation" id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
window.HTMLElement.prototype.contains = window.HTMLElement.prototype.contains || function () { return false; };

require("./load-renderer").evalRenderer(window);

const conv = window.document.getElementById("conversation");
Object.defineProperty(conv, "scrollHeight", { configurable: true, get: () => 1000 });
Object.defineProperty(conv, "clientHeight", { configurable: true, get: () => 400 });

const R = window.SerfRenderer;
R.init(conv);

(async () => {
  await new Promise((r) => setTimeout(r, 30)); // flush the cold-load buffer

  // ── Scroll-nudge pill: needs-you branch (formerly "↓ ◆ needs you") ────────
  conv.scrollTop = 100; // scrolled away from the bottom
  R.newContentCount = 1;
  R.newContentNeedsYou = true;
  R.renderNewContentPill();
  const pill = window.document.querySelector("[data-new-content-pill]");
  assert.ok(pill.innerHTML.includes("<svg"), "needs-you scroll pill must render an icon, not ◆ (got " + pill.innerHTML + ")");
  assert.ok(/needs you/.test(pill.textContent), "needs-you pill keeps its label text");

  // ── Scroll-nudge pill: urgent-error branch (formerly "↓ ✕ error") ─────────
  R.newContentNeedsYou = false;
  const errBelow = window.document.createElement("div");
  errBelow.className = "tool-call shell";
  errBelow.dataset.attention = "error";
  Object.defineProperty(errBelow, "offsetTop", { configurable: true, get: () => 900 });
  Object.defineProperty(errBelow, "offsetHeight", { configurable: true, get: () => 40 });
  conv.appendChild(errBelow);
  R.newContentCount = 1;
  R.renderNewContentPill();
  assert.ok(pill.innerHTML.includes("<svg"), "error scroll pill must render an icon, not ✕ (got " + pill.innerHTML + ")");
  assert.ok(/error/.test(pill.textContent), "error pill keeps its label text");
  errBelow.remove();
  R.newContentCount = 0;
  R.renderNewContentPill();

  // ── Off-screen dock (formerly "◆ The agent is waiting on your answer…") ──
  const questionEl = window.document.createElement("div");
  conv.appendChild(questionEl);
  R.state = "awaiting";
  R.markAgentQuestion(questionEl);
  const dock = window.document.querySelector("[data-needs-you-dock]");
  assert.ok(dock.innerHTML.includes("<svg"), "needs-you dock must render an icon, not ◆ (got " + dock.innerHTML + ")");
  assert.ok(/waiting on your answer/.test(dock.textContent), "dock keeps its label text");

  // ── Ask-header glyph (formerly plain "◆") ─────────────────────────────────
  const glyph = questionEl.querySelector(".agent-question-glyph");
  assert.ok(glyph && glyph.innerHTML.includes("<svg"), "ask-header glyph must render an icon, not ◆ (got " + (glyph && glyph.innerHTML) + ")");

  // ── Esc-collapsed ask chip (formerly "◆ question waiting") ────────────────
  const card = R.buildAskCardEl();
  const chip = card.querySelector("[data-ask-collapsed-chip]");
  assert.ok(chip.innerHTML.includes("<svg"), "ask-collapsed chip must render the question-waiting icon, not ◆ (got " + chip.innerHTML + ")");
  assert.ok(/question waiting/.test(chip.textContent), "ask-collapsed chip keeps its label text");

  // ── Settled-ask summary line (formerly "◆ asked …") ───────────────────────
  // XSS check: askedSummary comes from toolRendererFor("ask_user").target(),
  // which joins raw question `header` text with no escaping, and echo is the
  // user's own raw reply text (clipped/whitespace-collapsed, not escaped).
  // Neither is safe to string-concatenate into innerHTML, so this site must
  // build the icon and text as separate DOM nodes.
  const container = window.document.createElement("div");
  window.document.body.appendChild(container);
  const placeholder = window.document.createElement("div");
  container.appendChild(placeholder);
  const maliciousHeader = "<img src=x onerror=alert(1)>";
  const pa = { el: placeholder, items: [{ header: maliciousHeader }] };
  R.renderAskSettledLine(pa, "<b>reply</b>");
  const line = container.querySelector(".ask-settled-line");
  assert.ok(line, "settled line must be inserted in place of the ask card");
  assert.ok(line.innerHTML.includes("<svg"), "settled line must render an icon, not ◆ (got " + line.innerHTML + ")");
  assert.strictEqual(line.querySelectorAll("img").length, 0, "raw HTML in the question header must not be parsed into a live element (XSS)");
  assert.strictEqual(line.querySelectorAll("b").length, 0, "raw HTML in the reply text must not be parsed into a live element (XSS)");
  assert.ok(line.textContent.includes(maliciousHeader), "the header text still renders literally, just not as markup");
  assert.ok(line.textContent.includes("<b>reply</b>"), "the reply text still renders literally, just not as markup");

  console.log("test-renderer-needsyou-affordances.js: OK");
  process.exit(0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
