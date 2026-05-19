const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const MODULE = fs.readFileSync(path.resolve(__dirname, "../assets/pending.js"), "utf8");

function build() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div id="conversation"></div>
    <div class="queue-preview" data-queue-preview hidden>
      <ul data-queue-list></ul>
    </div>
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

(function test_start_reconcile_matches_image_only_submission() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  reg.register({ method: "turn/start", text: "", items: [{ type: "image", name: "shot.png" }] });

  assert.equal(reg.tryReconcile("turn/start", { text: "", items: [{ type: "image", name: "shot.png" }] }), true);
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0);
  console.log("ok start_reconcile_matches_image_only_submission");
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

(function test_failed_entry_reconciles_late_authoritative_update() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  const h = reg.register({ method: "turn/steer", text: "eventually lands" });
  reg.fail(h, "server did not confirm");

  assert.ok(conv.querySelector(".optimistic-failed"), "expected failed placeholder before reconcile");
  assert.equal(reg.tryReconcile("turn/steer", { text: "eventually lands" }), true);
  assert.equal(conv.querySelectorAll(".optimistic-failed").length, 0);
  console.log("ok failed_entry_reconciles_late_authoritative_update");
})();

(function test_reconcile_prefers_live_retry_over_failed_duplicate() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  const failedHandle = reg.register({ method: "turn/steer", text: "same" });
  reg.fail(failedHandle, "server did not confirm");
  reg.register({ method: "turn/steer", text: "same" });

  assert.equal(reg.tryReconcile("turn/steer", { text: "same" }), true);
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0,
    "live retry should reconcile first");
  assert.equal(conv.querySelectorAll(".optimistic-failed").length, 1,
    "older failed duplicate should remain until dismissed or late reconcile");
  console.log("ok reconcile_prefers_live_retry_over_failed_duplicate");
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

// C2 — turn/queue chip lands in queueList, not in conversation,
// and tryReconcileQueue removes any chip whose text appears in the
// authoritative preview list emitted by thread/queueChanged.
(function test_queue_chip_lands_in_queue_list_and_reconciles_via_preview() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const queueList = window.document.querySelector("[data-queue-list]");
  const reg = window.SerfAppwirePending.create({ conversation: conv, queueList });

  reg.register({ method: "turn/queue", text: "q1 text" });

  // Chip lives in the queue-list, not the conversation pane.
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0,
    "queue chip should not appear in conversation pane");
  const inQueue = queueList.querySelectorAll(".optimistic-pending");
  assert.equal(inQueue.length, 1, "expected 1 pending chip in queueList");
  assert.equal(inQueue[0].tagName, "LI", "queue chip should render as <li> for the queue <ul>");
  assert.match(inQueue[0].textContent, /q1 text/);

  // tryReconcileQueue removes chips whose text matches a preview entry.
  const removed = reg.tryReconcileQueue(["q1 text"]);
  assert.equal(removed, 1, "expected one chip reconciled");
  assert.equal(queueList.querySelectorAll(".optimistic-pending").length, 0);
  console.log("ok queue_chip_lands_in_queue_list_and_reconciles_via_preview");
})();

// tryReconcileQueue does not touch chips whose text isn't in the preview.
(function test_queue_reconcile_leaves_chips_not_in_preview() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const queueList = window.document.querySelector("[data-queue-list]");
  const reg = window.SerfAppwirePending.create({ conversation: conv, queueList });

  reg.register({ method: "turn/queue", text: "first" });
  reg.register({ method: "turn/queue", text: "second" });

  const removed = reg.tryReconcileQueue(["first"]);
  assert.equal(removed, 1);
  const remaining = queueList.querySelectorAll(".optimistic-pending");
  assert.equal(remaining.length, 1);
  assert.match(remaining[0].textContent, /second/);
  console.log("ok queue_reconcile_leaves_chips_not_in_preview");
})();

// Duplicate queue texts reconcile one-for-one. A single authoritative preview
// row should not confirm every pending chip with the same text.
(function test_queue_reconcile_consumes_duplicate_texts_once() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const queueList = window.document.querySelector("[data-queue-list]");
  const reg = window.SerfAppwirePending.create({ conversation: conv, queueList });

  reg.register({ method: "turn/queue", text: "same" });
  reg.register({ method: "turn/queue", text: "same" });

  const removed = reg.tryReconcileQueue(["same"]);
  assert.equal(removed, 1);
  const remaining = queueList.querySelectorAll(".optimistic-pending");
  assert.equal(remaining.length, 1);
  assert.match(remaining[0].textContent, /same/);
  console.log("ok queue_reconcile_consumes_duplicate_texts_once");
})();

// Image-only queue chips use the same synthetic preview text as the daemon.
(function test_queue_reconcile_image_only_preview() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const queueList = window.document.querySelector("[data-queue-list]");
  const reg = window.SerfAppwirePending.create({ conversation: conv, queueList });

  reg.register({ method: "turn/queue", text: "", items: [{ type: "image", name: "shot.png" }] });
  const chip = queueList.querySelector(".optimistic-pending");
  assert.ok(chip, "expected pending image-only queue chip");
  assert.match(chip.textContent, /\[image\]/);

  const removed = reg.tryReconcileQueue(["[image]"]);
  assert.equal(removed, 1);
  assert.equal(queueList.querySelectorAll(".optimistic-pending").length, 0);
  console.log("ok queue_reconcile_image_only_preview");
})();

// Backward-compat: callers without queueList still get queue chips
// (they fall back to the conversation pane rather than crashing).
(function test_queue_chip_falls_back_to_conversation_without_queue_list() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const reg = window.SerfAppwirePending.create({ conversation: conv });
  reg.register({ method: "turn/queue", text: "q1" });
  assert.equal(conv.querySelectorAll(".optimistic-pending").length, 1);
  console.log("ok queue_chip_falls_back_to_conversation_without_queue_list");
})();

// I4 — clicking Retry on a failed chip removes the failed DOM element
// before invoking onRetry, so the caller can re-issue without two
// chips stacking on screen.
(function test_retry_click_removes_failed_chip_before_invoking_onRetry() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const calls = [];
  const reg = window.SerfAppwirePending.create({
    conversation: conv,
    onRetry: (intent) => {
      // Capture both the intent and the DOM state at the moment onRetry runs,
      // so we can assert the failed chip was removed before re-issue.
      calls.push({
        intent,
        failedCount: conv.querySelectorAll(".optimistic-failed").length,
      });
    },
  });

  const h = reg.register({ method: "turn/steer", text: "redo this" });
  reg.fail(h, "server refused");
  const failed = conv.querySelector(".optimistic-failed");
  assert.ok(failed, "expected a failed chip");
  const retry = failed.querySelector(".optimistic-retry");
  assert.ok(retry, "expected a retry link");

  // Simulate a real click event (jsdom dispatches click handlers).
  const ev = new window.MouseEvent("click", { bubbles: true, cancelable: true });
  retry.dispatchEvent(ev);

  assert.equal(calls.length, 1, "onRetry should have been called once");
  assert.deepEqual(calls[0].intent, { method: "turn/steer", text: "redo this", items: [] });
  assert.equal(calls[0].failedCount, 0,
    "failed chip must be removed from DOM before onRetry runs");
  assert.equal(conv.querySelectorAll(".optimistic-failed").length, 0,
    "failed chip should remain absent after retry");
  console.log("ok retry_click_removes_failed_chip_before_invoking_onRetry");
})();

(function test_retry_preserves_attachment_items() {
  const window = build();
  const conv = window.document.getElementById("conversation");
  const queueList = window.document.querySelector("[data-queue-list]");
  const calls = [];
  const reg = window.SerfAppwirePending.create({
    conversation: conv,
    queueList,
    onRetry: (intent) => calls.push(intent),
  });
  const item = { type: "image", name: "shot.png" };
  const items = [item];
  const h = reg.register({ method: "turn/queue", text: "with image", items });
  reg.fail(h, "server refused");

  const retry = queueList.querySelector(".optimistic-retry");
  assert.ok(retry, "expected queue retry link");
  retry.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));

  assert.equal(calls.length, 1, "onRetry should have been called once");
  assert.deepEqual(calls[0], { method: "turn/queue", text: "with image", items: [item] });
  assert.notEqual(calls[0].items, items, "items should be a snapshot copy");
  console.log("ok retry_preserves_attachment_items");
})();

console.log("PASS test-pending-registry.js");
