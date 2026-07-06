// Subagent navigation (mockup #9): a subagent's workspace gets a way back to
// its parent. The server renders the breadcrumb banner (.subagent-parent-*);
// this test covers the client accelerator — pressing Escape on a subagent
// workspace navigates up to the parent via the breadcrumb's "↑ Parent" link.
// It must defer to any open overlay and ignore Escape while typing.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const STYLE_PATH = path.resolve(__dirname, "../assets/style.css");
const styleSrc = fs.readFileSync(STYLE_PATH, "utf8");

function newHarness(opts) {
  opts = opts || {};
  const banner = opts.subagent === false ? "" : `
      <nav class="subagent-parent-banner" aria-label="subagent breadcrumb">
        <a class="subagent-parent-up" href="/s/01PARENT">↑ Parent</a>
        <a class="subagent-parent-crumb" href="/s/01PARENT">Refactor auth token cache</a>
        <span class="subagent-parent-sep">/</span>
        <span class="subagent-parent-here">verify-billing</span>
        <span class="subagent-parent-rollup" data-subagent-rollup hidden></span>
        <span class="subagent-parent-esc">Esc <span class="subagent-parent-key">to parent</span></span>
      </nav>`;
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <header class="workspace-header" data-session-id="01CHILD">${banner}</header>
    <div id="conversation" data-session-id="01CHILD" data-state="ended"></div>
    <form data-input-form data-session-id="01CHILD">
      <textarea class="message-input"></textarea>
    </form>
    ${opts.extra || ""}
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
  const { window } = dom;
  window.marked = { parse: t => String(t || "") };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  // Capture hard navigations (jsdom has no real navigation).
  const nav = { href: "" };
  window.SerfRenderer.navigateTo = (href) => { nav.href = href; };
  return { window, conv, nav };
}

function pressEscape(window) {
  window.document.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
}

function spawnDelegate(window, callId, jobId, task) {
  window.SerfRenderer.handleData("TOOL_CALL_START", { call_id: callId, tool_name: "delegate", arguments_json: JSON.stringify({ task }) });
  window.SerfRenderer.handleData("TOOL_CALL_END", { call_id: callId, tool_name: "delegate", output: JSON.stringify({
    job_id: jobId, type: "delegate", status: "running", transcript_ref: "local:" + jobId, task,
  }) });
}

let allPass = true;
async function scenario(name, run) {
  const result = await run();
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) { allPass = false; console.log("  detail: " + result.detail); }
}

(async () => {

await scenario("Esc on a subagent workspace navigates up to the parent", async () => {
  const { window, nav } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  pressEscape(window);
  if (nav.href !== "/s/01PARENT") return { ok: false, detail: "expected navigation to /s/01PARENT, got " + JSON.stringify(nav.href) };
  return { ok: true };
});

await scenario("Esc does NOT navigate while typing in the composer", async () => {
  const { window, nav } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.document.querySelector(".message-input").focus();
  pressEscape(window);
  if (nav.href) return { ok: false, detail: "must not navigate while focus is in the textarea, got " + nav.href };
  return { ok: true };
});

await scenario("Esc defers to an open panel/overlay", async () => {
  const { window, nav } = newHarness({ extra: `<div id="details-panel"></div>` });
  await new Promise(r => setTimeout(r, 30));
  pressEscape(window);
  if (nav.href) return { ok: false, detail: "must defer to the open details-panel and not navigate, got " + nav.href };
  return { ok: true };
});

await scenario("a non-subagent workspace (no breadcrumb) ignores Esc", async () => {
  const { window, nav } = newHarness({ subagent: false });
  await new Promise(r => setTimeout(r, 30));
  pressEscape(window);
  if (nav.href) return { ok: false, detail: "a workspace with no parent breadcrumb must not navigate on Esc, got " + nav.href };
  return { ok: true };
});

await scenario("a failed direct child reddens the breadcrumb worst-state rollup chip", async () => {
  const { window } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("SESSION_START", { session_id: "01CHILD", status: "active" });
  spawnDelegate(window, "d1", "job_A", "ok worker");
  spawnDelegate(window, "d2", "job_B", "billing worker");
  window.SerfRenderer.handleData("JOB_FINISHED", { jobId: "job_A", status: "completed", outputBytes: 5, transcriptRef: "local:job_A" });
  window.SerfRenderer.handleData("JOB_FINISHED", { jobId: "job_B", status: "failed", reason: "boom", transcriptRef: "local:job_B" });
  await new Promise(r => setTimeout(r, 10));
  const chip = window.document.querySelector("[data-subagent-rollup]");
  if (!chip) return { ok: false, detail: "missing rollup chip" };
  if (chip.hidden) return { ok: false, detail: "chip should be visible when a child failed" };
  if (!chip.classList.contains("bad")) return { ok: false, detail: "chip should be reddened (.bad) on a failure" };
  if (!/failed/.test(chip.textContent)) return { ok: false, detail: "chip should say failed: " + chip.textContent };
  if (!chip.querySelector("svg")) return { ok: false, detail: "failed rollup chip should show an svg error icon" };
  if (chip.textContent.includes("✕")) return { ok: false, detail: "rollup chip should not use the literal ✕ glyph" };
  return { ok: true };
});

await scenario("an all-clean session keeps the rollup chip hidden", async () => {
  const { window } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("SESSION_START", { session_id: "01CHILD", status: "active" });
  spawnDelegate(window, "d1", "job_A", "ok worker");
  window.SerfRenderer.handleData("JOB_FINISHED", { jobId: "job_A", status: "completed", outputBytes: 5, transcriptRef: "local:job_A" });
  await new Promise(r => setTimeout(r, 10));
  const chip = window.document.querySelector("[data-subagent-rollup]");
  if (chip && !chip.hidden) return { ok: false, detail: "chip must stay hidden when nothing failed: " + chip.textContent };
  return { ok: true };
});

await scenario("CSS defines the subagent breadcrumb banner and rollup chip", async () => {
  const bannerRule = styleSrc.match(/\.subagent-parent-banner\s*\{[^}]*\}/);
  if (!bannerRule) return { ok: false, detail: "missing .subagent-parent-banner rule" };
  if (!/display\s*:\s*flex/.test(bannerRule[0])) return { ok: false, detail: ".subagent-parent-banner must set display: flex; got: " + bannerRule[0] };
  if (!/\.subagent-parent-up/.test(styleSrc)) return { ok: false, detail: "missing .subagent-parent-up rule" };
  const rollupRule = styleSrc.match(/\.subagent-parent-rollup\s*\{[^}]*\}/);
  if (!rollupRule) return { ok: false, detail: "missing .subagent-parent-rollup rule" };
  const rollupBadRule = styleSrc.match(/\.subagent-parent-rollup\.bad\s*\{[^}]*\}/);
  if (!rollupBadRule) return { ok: false, detail: "missing .subagent-parent-rollup.bad rule" };
  if (!/color\s*:\s*var\(--error\)/.test(rollupBadRule[0])) return { ok: false, detail: ".subagent-parent-rollup.bad must use color: var(--error) as the failure color signal" };
  return { ok: true };
});

if (!allPass) { console.error("FAIL: subagent-nav tests failed"); process.exit(1); }
console.log("OK\ttest-subagent-nav.js");
process.exit(0);

})();
