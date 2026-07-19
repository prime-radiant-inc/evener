// test-queue-edit-cancel: verifies each queue-preview row offers per-message
// edit and cancel actions (issue #23), alongside the promote action (issue
// #22). Cancel POSTs the row index + expected entry id to
// /s/<id>/cancel-queued (REST fallback; the appwire path is covered by Go
// relay tests). Edit is cancel-and-recompose: the FULL untruncated text
// (queueState.texts, never the truncated preview line) returns to the
// composer — preserving any text already there — the sticky draft persists
// through the same input-event path as real typing, and only then is the
// queued copy canceled. The stale-edit race is honest: when the removal
// fails (already consumed / queue shifted, review F1) the text stays in the
// composer and the banner says so instead of silently duplicating.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const DRAFTS_SRC = fs.readFileSync(path.resolve(__dirname, "../assets/drafts.js"), "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-active-turn-id="turn_live"
       data-state="active"></div>
  <form class="workspace-input" data-input-form data-session-id="01TEST">
    <div class="composer-attachments" data-composer-attachments data-attachments></div>
    <div class="queue-preview" data-queue-preview hidden>
      <div class="queue-preview-header">
        <span class="queue-preview-label">queued <span data-queue-depth>0</span></span>
      </div>
      <ul class="queue-preview-list" data-queue-list></ul>
    </div>
    <div class="input-card" data-drop-zone>
      <textarea class="message-input" rows="1"></textarea>
    </div>
    <div class="input-controls">
      <div class="controls-right">
        <button type="button" class="btn btn-ghost" data-steer-trigger>steer</button>
        <button type="submit" class="send-btn btn btn-primary"
                data-capability-send="false"
                data-capability-queue="true">send</button>
      </div>
    </div>
    <div class="input-status" id="input-status"></div>
    <input type="file" data-file-picker hidden>
  </form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });

const { window } = dom;
window.marked = { parse: (t) => t };

let fetchBehavior = { ok: true, status: 200, body: { removedText: "", removedImages: 0 } };
let deferredFetchSettle = null;
const fetchLog = [];
window.fetch = (url, opts) => {
  if (typeof url === "string" && url.includes("/tasks")) {
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve([]),
      text: () => Promise.resolve(""),
    });
  }
  fetchLog.push({ url, opts });
  const b = fetchBehavior;
  const respond = () => ({
    ok: b.ok,
    status: b.status,
    json: () => Promise.resolve(b.body || {}),
    text: () => Promise.resolve(b.ok ? JSON.stringify(b.body || {}) : (b.text || "conflict")),
  });
  // defer: hold the response until the test settles it explicitly, so a
  // request can be observed while still in flight (review M1).
  if (b.defer) return new Promise(resolve => { deferredFetchSettle = () => resolve(respond()); });
  return Promise.resolve(respond());
};

Object.defineProperty(window.HTMLTextAreaElement.prototype, "scrollHeight", {
  configurable: true,
  get() { return 36; },
});
Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });

window.eval(DRAFTS_SRC);
require("./load-renderer").evalRenderer(window);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);

const list = window.document.querySelector("[data-queue-list]");
const composer = window.document.querySelector(".message-input");
const wait = (ms) => new Promise(r => setTimeout(r, ms));

function primeQueue(preview, ids, texts) {
  const data = { depth: preview.length, preview, ids: ids || [] };
  if (texts) data.texts = texts;
  window.SerfRenderer.handle("QUEUE_CHANGED", { data: JSON.stringify(data) });
}

function bannerTexts() {
  return Array.from(window.document.querySelectorAll(".banner")).map(b => b.textContent);
}

async function testRowsHaveEditAndCancelButtons() {
  primeQueue(["investigate the failing test", "and then verify the regression"],
    ["q_1_aaa", "q_2_bbb"],
    ["investigate the failing test", "and then verify the regression"]);
  pass(list.children.length === 2, "expected 2 rows, got " + list.children.length);
  for (let i = 0; i < 2; i++) {
    const editBtn = list.children[i].querySelector("[data-edit-index]");
    const cancelBtn = list.children[i].querySelector("[data-cancel-index]");
    pass(editBtn !== null, "row " + i + " should have an edit button");
    pass(cancelBtn !== null, "row " + i + " should have a cancel button");
    pass(editBtn && editBtn.getAttribute("data-edit-index") === String(i),
      "row " + i + " edit button should carry index " + i);
    pass(cancelBtn && cancelBtn.getAttribute("data-cancel-index") === String(i),
      "row " + i + " cancel button should carry index " + i);
    // The promote action (issue #22) must still be there too.
    pass(list.children[i].querySelector("[data-promote-index]") !== null,
      "row " + i + " should still have a promote button");
  }
}

async function testCancelPostsIndexAndEntryId() {
  fetchLog.length = 0;
  fetchBehavior = { ok: true, status: 200, body: { removedText: "and then verify the regression", removedImages: 0 } };
  const btn = list.children[1].querySelector("[data-cancel-index]");
  btn.click();
  await wait(20);

  const calls = fetchLog.map(c => c.url);
  pass(calls.length === 1 && calls[0].includes("/s/01TEST/cancel-queued"),
    "expected one /cancel-queued call, got " + JSON.stringify(calls));
  const body = JSON.parse((fetchLog[0] && fetchLog[0].opts && fetchLog[0].opts.body) || "{}");
  pass(body.index === 1, "expected body.index=1, got " + JSON.stringify(body));
  pass(body.entryId === "q_2_bbb",
    "expected body.entryId=q_2_bbb (review F1 identity), got " + JSON.stringify(body));

  // No local mirror: the row stays until the daemon's authoritative
  // thread/queueChanged lands with the canceled entry removed.
  primeQueue(["investigate the failing test"], ["q_1_aaa"], ["investigate the failing test"]);
  pass(list.children.length === 1, "expected 1 row after daemon queueChanged, got " + list.children.length);
  pass(list.children[0].querySelector("[data-cancel-index]").getAttribute("data-cancel-index") === "0",
    "re-rendered row should re-key its cancel index to 0");
}

async function testCancelFailureSurfacesBanner() {
  primeQueue(["still queued"], ["q_3_ccc"], ["still queued"]);
  fetchLog.length = 0;
  fetchBehavior = { ok: false, status: 409, text: "cancel: queue entry at index 0 no longer matches the snapshot (queue changed)" };
  const before = bannerTexts().length;
  list.children[0].querySelector("[data-cancel-index]").click();
  await wait(20);

  pass(fetchLog.length === 1, "expected one cancel attempt, got " + fetchLog.length);
  pass(window.SerfRenderer.queueState.depth === 1,
    "queue state must be unchanged on cancel failure, got depth " + window.SerfRenderer.queueState.depth);
  pass(list.children.length === 1, "row must stay queued on cancel failure");
  const news = bannerTexts().slice(before);
  pass(news.some(t => /cancel failed/i.test(t)),
    "expected new banner mentioning 'cancel failed'; new banners: " + news.join(", "));
}

async function testEditRestoresFullTextPreservingComposer() {
  primeQueue(["line one", "second message"], ["q_4_ddd", "q_5_eee"],
    ["line one\nline two\nline three", "second message"]);
  composer.value = "draft in progress";
  composer.dispatchEvent(new window.Event("input", { bubbles: true }));

  fetchLog.length = 0;
  fetchBehavior = { ok: true, status: 200, body: { removedText: "line one\nline two\nline three", removedImages: 0 } };
  list.children[0].querySelector("[data-edit-index]").click();
  await wait(20);

  // The FULL multi-line text — not the truncated preview line — is appended
  // after the existing composer text, which is preserved.
  pass(composer.value === "draft in progress\n\nline one\nline two\nline three",
    "composer should preserve existing text and append the full queued text, got " + JSON.stringify(composer.value));
  // The sticky draft persisted through the same input-event path as typing.
  const draft = window.localStorage.getItem("serf-hub.draft.01TEST");
  pass(draft === "draft in progress\n\nline one\nline two\nline three",
    "sticky draft should hold the recomposed text, got " + JSON.stringify(draft));
  // The queued copy is canceled with its review-F1 identity.
  const calls = fetchLog.map(c => c.url);
  pass(calls.length === 1 && calls[0].includes("/s/01TEST/cancel-queued"),
    "edit should cancel the queued copy, got " + JSON.stringify(calls));
  const body = JSON.parse((fetchLog[0] && fetchLog[0].opts && fetchLog[0].opts.body) || "{}");
  pass(body.index === 0 && body.entryId === "q_4_ddd",
    "edit cancel should carry index 0 + q_4_ddd, got " + JSON.stringify(body));
  pass(window.document.activeElement === composer, "composer should be focused after edit");

  // Daemon confirms the removal; composer text is untouched by the re-render.
  primeQueue(["second message"], ["q_5_eee"], ["second message"]);
  pass(list.children.length === 1, "expected 1 row after daemon queueChanged, got " + list.children.length);
  pass(composer.value === "draft in progress\n\nline one\nline two\nline three",
    "composer text must survive the queue re-render, got " + JSON.stringify(composer.value));

  // Clean the composer for the next test.
  composer.value = "";
  composer.dispatchEvent(new window.Event("input", { bubbles: true }));
}

async function testEditFailureKeepsTextAndIsHonest() {
  // The stale-edit race: the entry was consumed between the snapshot and the
  // click, so the cancel comes back 409. The text must STILL be in the
  // composer (it was restored before the removal attempt) and the banner
  // must say the queued copy could not be removed — no silent duplication.
  primeQueue(["about to be consumed"], ["q_6_fff"], ["about to be consumed"]);
  fetchLog.length = 0;
  fetchBehavior = { ok: false, status: 409, text: "cancel: queue index 0 out of range (depth 0)" };
  const before = bannerTexts().length;
  list.children[0].querySelector("[data-edit-index]").click();
  await wait(20);

  pass(fetchLog.length === 1, "expected one cancel attempt, got " + fetchLog.length);
  pass(composer.value === "about to be consumed",
    "edited text must land in the composer even when removal fails, got " + JSON.stringify(composer.value));
  pass(window.localStorage.getItem("serf-hub.draft.01TEST") === "about to be consumed",
    "sticky draft should hold the restored text even when removal fails");
  const news = bannerTexts().slice(before);
  pass(news.some(t => /composer/i.test(t) && /could not be removed/i.test(t)),
    "expected honest banner: text in composer + queued copy not removed; new banners: " + news.join(", "));
  pass(window.SerfRenderer.queueState.depth === 1,
    "queue state must be unchanged on failed edit-removal, got depth " + window.SerfRenderer.queueState.depth);

  composer.value = "";
  composer.dispatchEvent(new window.Event("input", { bubbles: true }));
}

async function testEditWarnsAboutDroppedImages() {
  primeQueue(["look at these screenshots"], ["q_7_ggg"], ["look at these screenshots"]);
  fetchLog.length = 0;
  fetchBehavior = { ok: true, status: 200, body: { removedText: "look at these screenshots", removedImages: 2 } };
  const before = bannerTexts().length;
  list.children[0].querySelector("[data-edit-index]").click();
  await wait(20);

  pass(composer.value === "look at these screenshots",
    "composer should hold the restored text, got " + JSON.stringify(composer.value));
  const news = bannerTexts().slice(before);
  pass(news.some(t => /re-attach/i.test(t)),
    "expected a warning about dropped image attachments; new banners: " + news.join(", "));

  composer.value = "";
  composer.dispatchEvent(new window.Event("input", { bubbles: true }));
}

async function testEditImageOnlyEntryDisabled() {
  // An image-only queued entry has no text to recompose; its edit button is
  // disabled with an honest hint while cancel keeps working.
  primeQueue(["[image]"], ["q_8_hhh"], [""]);
  const editBtn = list.children[0].querySelector("[data-edit-index]");
  pass(editBtn.disabled === true, "edit should be disabled for an image-only queued message");
  fetchLog.length = 0;
  fetchBehavior = { ok: true, status: 200, body: { removedText: "", removedImages: 1 } };
  editBtn.click();
  await wait(20);
  pass(fetchLog.length === 0, "disabled edit must not call the daemon, got " + fetchLog.length + " calls");
  pass(composer.value === "", "disabled edit must not touch the composer");

  list.children[0].querySelector("[data-cancel-index]").click();
  await wait(20);
  pass(fetchLog.length === 1 && fetchLog[0].url.includes("/cancel-queued"),
    "cancel should still work for an image-only entry, got " + fetchLog.length + " calls");
}

async function testEditWithoutTextsDegradesHonestly() {
  // An old daemon sends no texts. Edit must say it is unavailable rather
  // than restoring the truncated preview line as if it were the message.
  primeQueue(["only the first line is shown"], ["q_9_iii"]); // no texts
  fetchLog.length = 0;
  const before = bannerTexts().length;
  list.children[0].querySelector("[data-edit-index]").click();
  await wait(20);
  pass(fetchLog.length === 0, "edit without full texts must not call the daemon");
  pass(composer.value === "", "edit without full texts must not touch the composer");
  const news = bannerTexts().slice(before);
  pass(news.some(t => /not available/i.test(t)),
    "expected an honest 'edit not available' banner; new banners: " + news.join(", "));
  // Cancel is unaffected by the missing texts.
  fetchBehavior = { ok: true, status: 200, body: { removedText: "only the first line is shown", removedImages: 0 } };
  list.children[0].querySelector("[data-cancel-index]").click();
  await wait(20);
  pass(fetchLog.length === 1, "cancel should still work without texts, got " + fetchLog.length + " calls");
}

async function testInFlightGuardPreventsDoubleFire() {
  // Review M1: while a cancel/edit request for an entry is in flight, the
  // row's edit + cancel buttons are disabled — a double-click must not
  // append the text to the composer twice or fire a second cancel that
  // Conflicts confusingly.
  primeQueue(["guard me"], ["q_a_jjj"], ["guard me"]);
  fetchLog.length = 0;
  fetchBehavior = { defer: true };
  const row = list.children[0];
  const editBtn = row.querySelector("[data-edit-index]");
  const cancelBtn = row.querySelector("[data-cancel-index]");

  editBtn.click();
  await wait(10);
  pass(fetchLog.length === 1, "expected one in-flight cancel, got " + fetchLog.length);
  pass(editBtn.disabled === true, "edit button should be disabled while the request is in flight");
  pass(cancelBtn.disabled === true, "cancel button should be disabled while the request is in flight");

  // Double-clicks on the disabled buttons are no-ops.
  editBtn.click();
  cancelBtn.click();
  await wait(10);
  pass(fetchLog.length === 1, "double-click must not fire a second request, got " + fetchLog.length);
  pass(composer.value === "guard me",
    "composer should hold the text exactly once, got " + JSON.stringify(composer.value));

  // Settle: the buttons re-enable for the (still-queued, in this test) row.
  deferredFetchSettle();
  await wait(20);
  pass(cancelBtn.disabled === false, "cancel button should re-enable after the request settles");
  pass(editBtn.disabled === false, "edit button should re-enable after the request settles");

  composer.value = "";
  composer.dispatchEvent(new window.Event("input", { bubbles: true }));
}

async function testInFlightGuardKeepsImageOnlyEditDisabled() {
  // Re-enabling after a settle must not re-enable the edit button of an
  // image-only entry (nothing to recompose).
  primeQueue(["[image]"], ["q_b_kkk"], [""]);
  fetchLog.length = 0;
  fetchBehavior = { defer: true };
  const row = list.children[0];
  const editBtn = row.querySelector("[data-edit-index]");
  const cancelBtn = row.querySelector("[data-cancel-index]");
  cancelBtn.click();
  await wait(10);
  pass(fetchLog.length === 1, "expected one in-flight cancel, got " + fetchLog.length);
  pass(cancelBtn.disabled === true, "cancel button should be disabled in flight");
  deferredFetchSettle();
  await wait(20);
  pass(cancelBtn.disabled === false, "cancel button should re-enable after settle");
  pass(editBtn.disabled === true, "image-only edit button must stay disabled after settle");
}

(async () => {
  await testRowsHaveEditAndCancelButtons();
  await testCancelPostsIndexAndEntryId();
  await testCancelFailureSurfacesBanner();
  await testEditRestoresFullTextPreservingComposer();
  await testEditFailureKeepsTextAndIsHonest();
  await testEditWarnsAboutDroppedImages();
  await testEditImageOnlyEntryDisabled();
  await testEditWithoutTextsDegradesHonestly();
  await testInFlightGuardPreventsDoubleFire();
  await testInFlightGuardKeepsImageOnlyEditDisabled();

  if (failures.length === 0) {
    console.log("PASS: queue-preview per-message edit and cancel");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
