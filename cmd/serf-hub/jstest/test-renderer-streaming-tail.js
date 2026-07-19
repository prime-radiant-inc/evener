// Past 4KB the message stops re-parsing: the parsed head FREEZES at the
// crossing (no re-parse, no reflow, no selection loss), the crossing delta
// and everything after streams as plain text in .streaming-tail, and
// finalization parses everything once. The live-tail caret is a real
// aria-hidden span, not a ::after pseudo-element (which screen readers
// announce).
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
  const headNodes = Array.from(el.childNodes);
  const headHTML = el.innerHTML;
  pass(headHTML === "<p>hello head</p>", "head rendered via settle (got " + JSON.stringify(headHTML) + ")");
  const parsesAfterHead = parses;

  // The crossing delta: head must freeze, delta streams raw into the tail.
  const big = "x".repeat(4100);
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: big });
  pass(parses === parsesAfterHead, "NO parse at the crossing (head freezes)");
  const afterNodes = Array.from(el.childNodes);
  pass(afterNodes.length === headNodes.length + 1, "crossing appends exactly one node (the tail)");
  let headUntouched = true;
  for (let i = 0; i < headNodes.length; i++) if (afterNodes[i] !== headNodes[i]) headUntouched = false;
  pass(headUntouched, "head DOM untouched at the crossing (same node references)");
  pass(el.innerHTML.startsWith(headHTML), "head innerHTML unchanged at the crossing");
  const tail = el.querySelector(".streaming-tail");
  pass(!!tail, "streaming-tail node exists past 4KB");
  const rawTailText = () => Array.from(tail.childNodes)
    .filter((n) => !(n.nodeType === 1 && n.classList.contains("streaming-caret")))
    .map((n) => n.textContent).join("");
  pass(tail && rawTailText() === big, "tail holds the CROSSING delta raw, un-parsed");
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
  pass(rawTailText() === big + "**tail**", "post-crossing delta streams raw into the tail");
  pass(tail.lastElementChild === caret, "caret still glued after the newest text");

  R.handleData("ASSISTANT_TEXT_END", { text: "hello head" + big + "**tail**" });
  pass(!el.querySelector(".streaming-tail"), "finalization replaces the tail with parsed output");
  pass(!el.querySelector(".streaming-caret"), "caret disappears with the tail at finalization");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: frozen head at the crossing, raw tail with aria-hidden caret, single final parse");
  process.exit(0);
})();
