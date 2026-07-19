// scheduleFrame must work where window.requestAnimationFrame is undefined
// (plain jsdom, and as a proxy for exotic embedded webviews).
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only" });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;
R.init(window.document.getElementById("conversation"));

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  pass(typeof R.scheduleFrame === "function", "scheduleFrame exists");
  pass(typeof window.requestAnimationFrame === "undefined", "test env really has no rAF");
  let ran = 0;
  R.scheduleFrame(() => { ran++; });
  await new Promise((r) => setTimeout(r, 50));
  pass(ran === 1, "scheduleFrame falls back to a timer when rAF is missing");
  // cancelFrame suppresses a pending callback.
  R.scheduleFrame(() => { ran++; });
  R.cancelFrame();
  await new Promise((r) => setTimeout(r, 50));
  pass(ran === 1, "cancelFrame drops the pending callback");
  // Keyed slots: two different keys both fire — neither cancels the other.
  let a = 0, b = 0;
  R.scheduleFrame(() => { a++; }, "scroll");
  R.scheduleFrame(() => { b++; }, "prepend-settle");
  await new Promise((r) => setTimeout(r, 50));
  pass(a === 1 && b === 1, "different keys keep independent pending slots (a=" + a + ", b=" + b + ")");
  // Same-key re-schedule cancels the earlier callback only.
  let first = 0, second = 0;
  R.scheduleFrame(() => { first++; }, "viewport");
  R.scheduleFrame(() => { second++; }, "viewport");
  await new Promise((r) => setTimeout(r, 50));
  pass(first === 0 && second === 1, "same-key re-schedule cancels the earlier callback (first=" + first + ", second=" + second + ")");
  // Bare cancelFrame() cancels every keyed slot.
  let x = 0, y = 0;
  R.scheduleFrame(() => { x++; }, "scroll");
  R.scheduleFrame(() => { y++; }, "prepend-settle");
  R.cancelFrame();
  await new Promise((r) => setTimeout(r, 50));
  pass(x === 0 && y === 0, "bare cancelFrame() cancels all keyed slots (x=" + x + ", y=" + y + ")");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: scheduleFrame works without requestAnimationFrame; keyed slots stay independent");
  process.exit(0);
})();
