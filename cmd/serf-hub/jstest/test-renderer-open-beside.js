// "Open beside" affordance on subagent rows: each completed subagent row
// gains an ⇲ button that calls window.SerfPanes.open("/thread/<ref>", title)
// without triggering the row's hard-navigation onclick.
// Guard: when window.SerfPanes is absent the button must not be added.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const STYLE_PATH = "../assets/style.css";
const styleSrc = fs.readFileSync(STYLE_PATH, "utf8");

function newHarness(opts) {
  opts = opts || {};
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01HOST"></header>
    <div id="conversation" data-session-id="01HOST" data-state="active"></div>
    <form data-input-form data-session-id="01HOST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  // Inject SerfPanes stub before evalRenderer so guard check works at render time.
  if (opts.withPanes !== false) {
    window.SerfPanes = {
      open: opts.panesOpen || (() => {}),
      threadHref: ref => "/thread/" + encodeURIComponent(ref),
    };
  }
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  // Capture hard navigations.
  const nav = { href: "" };
  window.SerfRenderer.navigateTo = (href) => { nav.href = href; };
  return { window, conv, nav };
}

function spawnDelegate(window, callId, jobId, task) {
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: callId, tool_name: "delegate", arguments_json: JSON.stringify({ task }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: callId, tool_name: "delegate", output: JSON.stringify({
    job_id: jobId, type: "delegate", status: "running", transcript_ref: "local:" + jobId, task,
  }) });
}

function finishDelegate(window, jobId, transcriptRef) {
  window.SerfRenderer.handleData("JOB_FINISHED", {
    jobId, status: "completed", outputBytes: 42,
    transcriptRef: transcriptRef || ("local:" + jobId),
  });
}

let allPass = true;
async function scenario(name, run) {
  const result = await run();
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) { allPass = false; console.log("  detail: " + result.detail); }
}

(async () => {

await scenario("open-beside button appears on a subagent row that has a transcriptRef", async () => {
  const { window } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  spawnDelegate(window, "c1", "job1", "do work");
  finishDelegate(window, "job1", "local:job1");
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".open-beside-btn");
  if (!btn) return { ok: false, detail: "no .open-beside-btn found on subagent row" };
  return { ok: true };
});

await scenario("clicking open-beside calls SerfPanes.open with correct href", async () => {
  let openedWith = null;
  const { window, nav } = newHarness({
    panesOpen: (href, title) => { openedWith = { href, title }; },
  });
  await new Promise(r => setTimeout(r, 30));
  spawnDelegate(window, "c2", "job2", "some task");
  finishDelegate(window, "job2", "sub-xyz");
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".open-beside-btn");
  if (!btn) return { ok: false, detail: "no .open-beside-btn found" };
  const beforeHref = window.location.href;
  btn.click();
  if (!openedWith) return { ok: false, detail: "SerfPanes.open was not called" };
  if (openedWith.href !== "/thread/sub-xyz") return { ok: false, detail: "wrong href: " + JSON.stringify(openedWith.href) };
  if (window.location.href !== beforeHref) return { ok: false, detail: "open-beside must not navigate, got: " + window.location.href };
  return { ok: true };
});

await scenario("subagent open-beside uses thread document route for source-qualified refs", async () => {
  let openedWith = null;
  const { window } = newHarness({
    panesOpen: (href, title) => { openedWith = { href, title }; },
  });
  await new Promise(r => setTimeout(r, 30));
  spawnDelegate(window, "c2b", "job_A", "source qualified task");
  finishDelegate(window, "job_A", "local:child-A");
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".open-beside-btn");
  if (!btn) return { ok: false, detail: "missing open-beside button" };
  btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  if (!openedWith) return { ok: false, detail: "SerfPanes.open was not called" };
  if (openedWith.href !== "/thread/local%3Achild-A") return { ok: false, detail: "wrong href: " + openedWith.href };
  return { ok: true };
});

await scenario("clicking open-beside does NOT trigger the row's hard navigation", async () => {
  const { window, nav } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  spawnDelegate(window, "c3", "job3", "another task");
  finishDelegate(window, "job3", "local:job3");
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".open-beside-btn");
  if (!btn) return { ok: false, detail: "no .open-beside-btn found" };
  btn.click();
  if (nav.href) return { ok: false, detail: "open-beside must not trigger navigateTo, got: " + nav.href };
  return { ok: true };
});

await scenario("open-beside button is absent when window.SerfPanes is not present", async () => {
  const { window } = newHarness({ withPanes: false });
  await new Promise(r => setTimeout(r, 30));
  spawnDelegate(window, "c4", "job4", "task no panes");
  finishDelegate(window, "job4", "local:job4");
  await new Promise(r => setTimeout(r, 10));
  const btn = window.document.querySelector(".open-beside-btn");
  if (btn) return { ok: false, detail: "open-beside-btn should be absent when SerfPanes unavailable (iframe guard)" };
  return { ok: true };
});

await scenario("CSS defines .open-beside-btn (hover-revealed quiet button)", async () => {
  if (!/.open-beside-btn/.test(styleSrc)) return { ok: false, detail: "missing .open-beside-btn CSS rule" };
  return { ok: true };
});

if (!allPass) { console.error("FAIL: open-beside tests failed"); process.exit(1); }
console.log("OK\ttest-renderer-open-beside.js");
process.exit(0);

})();
