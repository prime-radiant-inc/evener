const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const MODULE = fs.readFileSync(path.resolve(__dirname, "../assets/pending.js"), "utf8");

function build() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="conversation"></div>
    <div id="queue-preview"></div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.eval(MODULE);
  return window;
}

(function test_register_renders_pending_chip() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });

  const h = reg.register({ method: "turn/steer", text: "look at this" });

  const chips = conv.querySelectorAll(".steering.optimistic-pending");
  assert.equal(chips.length, 1, "expected 1 pending steering chip");
  assert.match(chips[0].textContent, /look at this/);
  assert.ok(h.id > 0);
  console.log("ok register_renders_pending_chip");
})();

(function test_fail_marks_failed_with_retry() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  const h = reg.register({ method: "turn/steer", text: "x" });

  reg.fail(h, "steer is not available for this session");

  assert.ok(!conv.querySelector(".optimistic-pending"));
  const failed = conv.querySelector(".optimistic-failed");
  assert.ok(failed, "expected failed element");
  assert.match(failed.querySelector(".optimistic-failed-reason").textContent, /not available/);
  assert.ok(failed.querySelector(".optimistic-retry"), "retry link missing");
  console.log("ok fail_marks_failed_with_retry");
})();

(function test_try_reconcile_removes_match() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  reg.register({ method: "turn/steer", text: "look at this" });

  const matched = reg.tryReconcile("turn/steer", { text: "look  at  this" });
  assert.equal(matched, true);
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0);
  console.log("ok try_reconcile_removes_match");
})();

(function test_try_reconcile_no_match_returns_false() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  reg.register({ method: "turn/steer", text: "look at this" });
  assert.equal(reg.tryReconcile("turn/steer", { text: "completely different" }), false);
  console.log("ok try_reconcile_no_match_returns_false");
})();

(function test_timeout_marks_failed() {
  const window = build();
  const fakeNow = { v: 0 };
  const timers = [];
  const fakeSetTimeout = (fn, ms) => { timers.push({ fire: fakeNow.v + ms, fn, cancelled: false }); return timers.length - 1; };
  const fakeClearTimeout = (id) => { if (timers[id]) timers[id].cancelled = true; };
  const advance = (ms) => {
    fakeNow.v += ms;
    for (const t of timers) {
      if (!t.cancelled && t.fire <= fakeNow.v) { t.cancelled = true; t.fn(); }
    }
  };

  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({
    conversation: conv,
    setTimeout: fakeSetTimeout,
    clearTimeout: fakeClearTimeout,
  });
  reg.register({ method: "turn/steer", text: "x" });
  advance(11000);

  const failed = conv.querySelector(".optimistic-failed");
  assert.ok(failed);
  assert.match(failed.querySelector(".optimistic-failed-reason").textContent, /did not confirm/);
  console.log("ok timeout_marks_failed");
})();

(function test_drain_reconciles_first_match() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  reg.register({ method: "turn/drainAsSteer", text: "" });

  // drain-special: first matching method reconciles regardless of text.
  assert.equal(reg.tryReconcile("turn/drainAsSteer", { text: "anything joined here" }), true);
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0);
  console.log("ok drain_reconciles_first_match");
})();

console.log("PASS test-pending-registry.js");
