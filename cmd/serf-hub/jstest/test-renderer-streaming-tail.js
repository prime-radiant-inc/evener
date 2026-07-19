// Past 4KB the message stops re-parsing: at the crossing the head renders
// ONCE as the first ≤4096 chars (one bounded parse — so a >4KB first delta
// still shows its head formatted), then FREEZES; everything past the
// boundary streams as plain text in .streaming-tail, and finalization parses
// everything once. The boundary backs off one code unit when it would split
// a surrogate pair. The live-tail caret is a real aria-hidden span, not a
// ::after pseudo-element (which screen readers announce).
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
let parses = 0;
window.marked = { parse: (t) => { parses++; return "<p>" + t + "</p>"; } };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  // Sub-4KB head first, rendered via handleData so settleFrame parses it.
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "hello head" });
  const el = conv.querySelector(".assistant-message");
  pass(!!el, "message element exists");
  pass(el.dataset.turnId === "t1", "data-turn-id preserved pre-crossing");
  const headHTML = el.innerHTML;
  pass(headHTML === "<p>hello head</p>", "head rendered via settle (got " + JSON.stringify(headHTML) + ")");
  const parsesAfterHead = parses;

  // The crossing delta: the head is rendered ONCE as the first ≤4096 chars
  // (parsed, bounded cost), then frozen; the remainder streams raw into the
  // tail — including the crossing delta's share past the boundary.
  const big = "x".repeat(4100);
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: big });
  pass(parses === parsesAfterHead + 1, "exactly ONE bounded parse at the crossing (head rendered once, then frozen)");
  const buf1 = "hello head" + big;
  const headP = el.querySelector("p");
  pass(headP && headP.textContent === buf1.slice(0, 4096), "head shows the first 4096 chars PARSED at the crossing");
  const tail = el.querySelector(".streaming-tail");
  pass(!!tail, "streaming-tail node exists past 4KB");
  const rawTailText = () => Array.from(tail.childNodes)
    .filter((n) => !(n.nodeType === 1 && n.classList.contains("streaming-caret")))
    .map((n) => n.textContent).join("");
  pass(tail && rawTailText() === buf1.slice(4096), "tail holds everything past the 4096 boundary raw, un-parsed");
  pass(headP && headP.textContent + rawTailText() === buf1, "head + tail reassemble the exact buffer (nothing lost, nothing duplicated)");
  pass(el.classList.contains("streaming"), "message carries .streaming while in tail mode");

  // The caret is a real aria-hidden span, last in the tail — not announced.
  const caret = tail && tail.querySelector(".streaming-caret");
  pass(!!caret, "caret is a real span inside the tail");
  pass(caret && caret.getAttribute("aria-hidden") === "true", "caret is aria-hidden (out of the a11y tree)");
  pass(caret && caret.textContent === "▍", "caret renders the ▍ glyph");
  pass(tail && tail.lastElementChild === caret, "caret stays last in the tail");

  // Post-crossing deltas stream raw, insert AHEAD of the caret, no parse.
  const before = parses;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "**tail**" });
  pass(parses === before, "no parse per delta in tail mode");
  pass(rawTailText() === buf1.slice(4096) + "**tail**", "post-crossing delta streams raw into the tail");
  pass(tail.lastElementChild === caret, "caret still glued after the newest text");

  R.handleData("ASSISTANT_TEXT_END", { text: "hello head" + big + "**tail**" });
  pass(!el.querySelector(".streaming-tail"), "finalization replaces the tail with parsed output");
  pass(!el.querySelector(".streaming-caret"), "caret disappears with the tail at finalization");

  const rawTailTextOf = (t) => Array.from(t.childNodes)
    .filter((n) => !(n.nodeType === 1 && n.classList.contains("streaming-caret")))
    .map((n) => n.textContent).join("");

  // --- Same-flush crossing (batched live path): the last pre-crossing delta
  // and the crossing delta landing in ONE flush must not lose the head — the
  // switch renders the pending head once before freezing it.
  R.handleData("TURN_STARTED", { turnId: "t2" });
  R.handleData("ASSISTANT_TEXT_START", {});
  // Mirror the frame-batching stub: drive the batched path directly.
  window.SerfAppwire = {
    eventsFromNotification: (method, params) =>
      method === "test/delta" ? [["ASSISTANT_TEXT_DELTA", { delta: params.delta }]] : [],
  };
  R.appwireHydrated = true;
  const head2 = "batched pre-crossing head";
  const cross2 = "y".repeat(4100);
  R.enqueueLiveNotification("test/delta", { delta: head2 });
  R.enqueueLiveNotification("test/delta", { delta: cross2 });
  R.flush();
  const el2 = conv.querySelectorAll(".assistant-message")[1];
  pass(!!el2, "second message element exists");
  const buf2 = head2 + cross2;
  const head2El = el2 && el2.querySelector("p");
  pass(head2El && head2El.textContent === buf2.slice(0, 4096),
    "head rendered once at the switch inside one flush (first 4096 chars, parsed)");
  const tail2 = el2 && el2.querySelector(".streaming-tail");
  pass(!!tail2, "tail exists after same-flush crossing");
  pass(tail2 && rawTailTextOf(tail2) === buf2.slice(4096), "tail holds the post-boundary remainder raw after same-flush crossing");
  pass(el2 && el2.textContent.includes(head2) && el2.textContent.includes(cross2.slice(-100)),
    "no content missing after same-flush crossing");
  R.handleData("ASSISTANT_TEXT_END", { text: head2 + cross2 });
  pass(!el2.querySelector(".streaming-tail"), "finalization replaces the same-flush tail");

  // --- First-delta crossing: a single >4KB FIRST delta renders its first
  // ~4KB PARSED at the switch (the old behavior left the whole message raw
  // until finalization); the remainder streams raw in the tail.
  R.handleData("TURN_STARTED", { turnId: "t3" });
  R.handleData("ASSISTANT_TEXT_START", {});
  const parsesBefore3 = parses;
  const first3 = "**bold** intro " + "z".repeat(5000 - 15);
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: first3 });
  const el3 = conv.querySelectorAll(".assistant-message")[2];
  const tail3 = el3 && el3.querySelector(".streaming-tail");
  pass(!!tail3, "tail exists for first-delta crossing");
  pass(parses === parsesBefore3 + 1, "marked stub called once at the first-delta switch (head is parsed, not raw)");
  const head3 = el3 && el3.querySelector("p");
  pass(head3 && head3.textContent === first3.slice(0, 4096), "head shows the first ~4KB of the first delta PARSED");
  pass(head3 && head3.textContent.includes("**bold** intro"), "formatted head carries the delta's leading content");
  pass(tail3 && rawTailTextOf(tail3) === first3.slice(4096), "tail holds the remainder of the first delta raw");
  R.handleData("ASSISTANT_TEXT_END", { text: first3 });
  pass(el3.innerHTML === "<p>" + first3 + "</p>", "finalization parses the first-delta buffer");

  // --- Surrogate pair astride the 4096 boundary: the boundary backs off one
  // code unit so the pair stays whole in the tail — no U+FFFD in either part.
  R.handleData("TURN_STARTED", { turnId: "t4" });
  R.handleData("ASSISTANT_TEXT_START", {});
  const pre4 = "a".repeat(4095);
  const emoji = "\u{1F600}"; // surrogate pair: high half lands on index 4095
  const delta4 = pre4 + emoji + "b".repeat(50);
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: delta4 });
  const el4 = conv.querySelectorAll(".assistant-message")[3];
  const head4 = el4 && el4.querySelector("p");
  pass(head4 && head4.textContent === pre4, "head ends BEFORE the split surrogate pair");
  pass(head4 && !head4.textContent.includes("�"), "no U+FFFD in the frozen head");
  const tail4 = el4 && el4.querySelector(".streaming-tail");
  pass(!!tail4, "tail exists for the surrogate-boundary crossing");
  pass(tail4 && rawTailTextOf(tail4).startsWith(emoji), "tail starts with the WHOLE surrogate pair");
  pass(tail4 && !rawTailTextOf(tail4).includes("�"), "no U+FFFD in the tail");
  pass(head4 && tail4 && head4.textContent + rawTailTextOf(tail4) === delta4, "head + tail reassemble the exact buffer across the surrogate boundary");
  R.handleData("ASSISTANT_TEXT_END", { text: delta4 });
  pass(el4.innerHTML === "<p>" + delta4 + "</p>", "finalization parses the surrogate-boundary buffer intact");

  // --- Selection-safe crossing: the head already shows EXACTLY headLen chars
  // (settled clean — not dirty) when the crossing delta arrives, so no new
  // content would enter the head. The crossing must NOT re-render it: a
  // re-parse replaces the head DOM and destroys any reader selection for zero
  // visual change.
  R.handleData("TURN_STARTED", { turnId: "t5" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "q".repeat(4096) });
  const el5 = conv.querySelectorAll(".assistant-message")[4];
  pass(!!el5, "fifth message element exists");
  const headP5 = el5 && el5.querySelector("p");
  pass(headP5 && headP5.textContent === "q".repeat(4096), "head settled at exactly 4096 chars pre-crossing");
  const headHTML5 = el5.innerHTML;
  const parsesBefore5 = parses;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "r".repeat(10) });
  pass(parses === parsesBefore5, "no head re-parse at the crossing when the head gains no content (selection survives)");
  pass(el5.querySelector("p") === headP5, "head DOM node untouched at the no-content crossing");
  pass(el5.innerHTML.indexOf(headHTML5) === 0, "head innerHTML identical across the no-content crossing");
  const tail5 = el5.querySelector(".streaming-tail");
  pass(!!tail5, "tail still opens at the no-content crossing");
  pass(tail5 && rawTailTextOf(tail5) === "r".repeat(10), "the whole crossing delta streams into the tail");
  R.handleData("ASSISTANT_TEXT_END", { text: "q".repeat(4096) + "r".repeat(10) });
  pass(el5.innerHTML === "<p>" + "q".repeat(4096) + "r".repeat(10) + "</p>", "finalization parses the no-content-crossing buffer");

  // --- Parse-failure fallback: marked.parse throwing at finalization must
  // leave the message as plain text (textContent set) with no throw escaping.
  const realParse = window.marked.parse;
  window.marked.parse = () => { throw new Error("boom: parser exploded"); };
  let endThrew = null;
  R.handleData("TURN_STARTED", { turnId: "t6" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "plain fallback text" });
  try {
    R.handleData("ASSISTANT_TEXT_END", { text: "plain fallback text" });
  } catch (err) {
    endThrew = err;
  }
  window.marked.parse = realParse;
  const el6 = conv.querySelectorAll(".assistant-message")[5];
  pass(endThrew === null, "no throw escapes finalization when marked.parse fails: " + (endThrew && endThrew.message));
  pass(!!el6, "sixth message element exists after the parse failure");
  pass(el6 && el6.textContent === "plain fallback text", "message stays as plain text when marked.parse throws");
  pass(el6 && el6.children.length === 0, "plain-text fallback set textContent (no parsed children)");

  // --- Streaming caret breathe (stylesheet): the caret rule references the
  // one sanctioned soft breathe (--pulse-cycle, think-breathe idiom) and the
  // reduced-motion collapse covers it.
  const fs = require("fs");
  const path = require("path");
  const css = fs.readFileSync(path.join(__dirname, "..", "assets", "style.css"), "utf8");
  const caretRule = css.match(/\.assistant-message \.streaming-tail \.streaming-caret \{([^}]*)\}/);
  pass(!!caretRule, "stylesheet has a .streaming-caret rule");
  pass(caretRule && /animation:\s*think-breathe var\(--pulse-cycle\) infinite/.test(caretRule[1]),
    "caret breathes via the think-breathe keyframe on --pulse-cycle (got: " + (caretRule && caretRule[1].trim()) + ")");
  const reducedMotion = css.match(/@media \(prefers-reduced-motion: reduce\) \{\s*\*, \*::before, \*::after \{\s*animation-duration: 1ms !important;/);
  pass(!!reducedMotion, "the universal reduced-motion collapse covers the caret breathe");

  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: frozen head at the crossing, raw tail with aria-hidden caret, single final parse");
  process.exit(0);
})();
