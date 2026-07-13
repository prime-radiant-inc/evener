// System / skill churn (mockup #7): lifecycle events are bookkeeping that must
// recede under the conversation. This covers the three recommended alternatives:
//   A — quiet dim one-liner: no divider weight, no meaningless "N chars" count.
//   B — coalesce a run of 3+ adjacent lifecycle events into one quiet line.
//   C — ✓-only silent success: a write/touch that returns nothing still states
//       what it changed (a ✓ + path), not an empty expandable.
const { JSDOM } = require("jsdom");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="active"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });
  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

let allPass = true;
async function scenario(name, fn) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  let result;
  try { result = await fn({ window, conv, R: window.SerfRenderer }); }
  catch (e) { result = { ok: false, detail: String(e && e.stack || e) }; }
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) { allPass = false; console.log("  detail: " + result.detail); console.log("  HTML: " + conv.innerHTML); }
}

(async () => {

  // A — a single lifecycle block renders as a quiet one-liner: NO divider rules
  // baked into the summary, NO "N chars"/"N KB" payload count.
  await scenario("single lifecycle block is a quiet one-liner without 'N chars'", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Skill activated", text: "x".repeat(56) });
    const block = conv.querySelector(".system-message");
    if (!block) return { ok: false, detail: "no system-message block" };
    if (!block.textContent.includes("Skill activated")) return { ok: false, detail: "missing verb" };
    if (/\bchars\b/.test(block.textContent)) return { ok: false, detail: "still shows 'N chars': " + block.textContent };
    if (/\bKB\b/.test(block.textContent)) return { ok: false, detail: "still shows 'N KB': " + block.textContent };
    const detail = block.querySelector(".steering-detail");
    if (detail && /\d/.test(detail.textContent)) return { ok: false, detail: "detail still carries a count: " + detail.textContent };
    return { ok: true };
  });

  // B — a run of 3+ adjacent lifecycle events coalesces into one quiet
  // collapsible line; the individual blocks are hidden until expanded.
  await scenario("3+ adjacent lifecycle events coalesce into one line", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Skill activated", text: "release skill" });
    R.handleData("SYSTEM_MESSAGE", { title: "Hook completed", text: "pre-tool hook" });
    R.handleData("SYSTEM_MESSAGE", { title: "Tools (2)", text: "- read_file\n- apply_patch" });
    const runs = conv.querySelectorAll(".system-run");
    if (runs.length !== 1) return { ok: false, detail: "expected one .system-run, got " + runs.length };
    const run = runs[0];
    if (!run.classList.contains("coalesced")) return { ok: false, detail: "run of 3 should be coalesced" };
    const toggle = run.querySelector(".system-run-toggle");
    if (!toggle) return { ok: false, detail: "no coalesce toggle" };
    if (!/\b3\b/.test(toggle.textContent) || !/system events/.test(toggle.textContent)) return { ok: false, detail: "toggle should read 'N system events': " + toggle.textContent };
    if (!/Skill activated/.test(toggle.textContent)) return { ok: false, detail: "toggle should name the first event: " + toggle.textContent };
    const blocks = conv.querySelectorAll(".system-run .system-message");
    if (blocks.length !== 3) return { ok: false, detail: "expected 3 blocks inside the run, got " + blocks.length };
    // Expanding shows the individual blocks.
    toggle.click();
    if (!run.classList.contains("open")) return { ok: false, detail: "click should open the run" };
    return { ok: true };
  });

  // B — fewer than 3 adjacent events do NOT coalesce (no toggle, blocks shown).
  await scenario("2 adjacent lifecycle events do not coalesce", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Skill activated", text: "release skill" });
    R.handleData("SYSTEM_MESSAGE", { title: "Tools (2)", text: "- read_file" });
    const run = conv.querySelector(".system-run");
    if (run && run.classList.contains("coalesced")) return { ok: false, detail: "2 events should not coalesce" };
    const blocks = conv.querySelectorAll(".system-message");
    if (blocks.length !== 2) return { ok: false, detail: "expected 2 visible blocks, got " + blocks.length };
    return { ok: true };
  });

  // B — a non-lifecycle entry between events breaks the run (no coalescing).
  await scenario("interleaved prose breaks the lifecycle run", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Skill activated", text: "a" });
    R.handleData("ASSISTANT_TEXT_START", {});
    R.handleData("ASSISTANT_TEXT_DELTA", { delta: "Working on it." });
    R.handleData("SYSTEM_MESSAGE", { title: "Prompt loaded", text: "b" });
    R.handleData("SYSTEM_MESSAGE", { title: "Tools (2)", text: "c" });
    const runs = conv.querySelectorAll(".system-run");
    // Two separate runs (one before the prose, one after) — neither coalesced
    // because each has fewer than 3 members.
    for (const run of runs) {
      if (run.classList.contains("coalesced")) return { ok: false, detail: "interleaved prose must break the run" };
    }
    return { ok: true };
  });

  // Plugin-loaded events form their own disclosure run. The collapsed line names
  // the plugins; expanding reveals the per-plugin loaded details.
  await scenario("consecutive plugin-loaded events group into plugin disclosure", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin slack (2 skills, 0 agents, 1 MCP server)", eventKind: "plugin_loaded", text: "Loaded plugin slack (2 skills, 0 agents, 1 MCP server)", raw: { pluginLoaded: { name: "slack", skillCount: 2, agentCount: 0, mcpCount: 1 } } });
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin github (3 skills, 1 agent, 0 MCP servers)", eventKind: "plugin_loaded", text: "Loaded plugin github (3 skills, 1 agent, 0 MCP servers)", raw: { pluginLoaded: { name: "github", skillCount: 3, agentCount: 1, mcpCount: 0 } } });
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin superpowers (14 skills, 0 agents, 0 MCP servers)", eventKind: "plugin_loaded", text: "Loaded plugin superpowers (14 skills, 0 agents, 0 MCP servers)", raw: { pluginLoaded: { name: "superpowers", skillCount: 14, agentCount: 0, mcpCount: 0 } } });
    const pluginRun = conv.querySelector(".plugin-run");
    if (!pluginRun) return { ok: false, detail: "no plugin-run disclosure" };
    const toggle = pluginRun.querySelector(".system-run-toggle");
    if (!toggle) return { ok: false, detail: "no plugin disclosure toggle" };
    const want = "Loaded plugins: slack, github, superpowers";
    if (!toggle.textContent.includes(want)) return { ok: false, detail: "toggle should read " + JSON.stringify(want) + ": " + toggle.textContent };
    const blocks = pluginRun.querySelectorAll(".system-message");
    if (blocks.length !== 3) return { ok: false, detail: "expected 3 plugin detail rows, got " + blocks.length };
    toggle.click();
    if (!pluginRun.classList.contains("open")) return { ok: false, detail: "click should open the plugin disclosure" };
    for (const name of ["slack", "github", "superpowers"]) {
      if (!pluginRun.textContent.includes("Loaded plugin " + name)) return { ok: false, detail: "expanded details should include " + name + ": " + pluginRun.textContent };
    }
    return { ok: true };
  });

  // Plugin-loaded runs are split from surrounding lifecycle runs instead of
  // being folded into "N system events".
  await scenario("plugin-loaded events break out from surrounding system events", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Skill activated", text: "release skill" });
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin slack (2 skills, 0 agents, 1 MCP server)", eventKind: "plugin_loaded", text: "Loaded plugin slack (2 skills, 0 agents, 1 MCP server)", raw: { pluginLoaded: { name: "slack" } } });
    R.handleData("SYSTEM_MESSAGE", { title: "Tools (2)", text: "- read_file\n- apply_patch" });
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin github (3 skills, 1 agent, 0 MCP servers)", eventKind: "plugin_loaded", text: "Loaded plugin github (3 skills, 1 agent, 0 MCP servers)", raw: { pluginLoaded: { name: "github" } } });
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin superpowers (14 skills, 0 agents, 0 MCP servers)", eventKind: "plugin_loaded", text: "Loaded plugin superpowers (14 skills, 0 agents, 0 MCP servers)", raw: { pluginLoaded: { name: "superpowers" } } });
    R.handleData("SYSTEM_MESSAGE", { title: "Hook completed", text: "post-plugin hook" });
    const pluginRuns = Array.from(conv.querySelectorAll(".plugin-run"));
    if (pluginRuns.length !== 2) return { ok: false, detail: "expected two plugin runs split by system events, got " + pluginRuns.length };
    const systemRuns = Array.from(conv.querySelectorAll(".system-run:not(.plugin-run)"));
    if (systemRuns.length !== 3) return { ok: false, detail: "expected three non-plugin system runs, got " + systemRuns.length };
    const firstToggle = pluginRuns[0].querySelector(".system-run-toggle");
    const secondToggle = pluginRuns[1].querySelector(".system-run-toggle");
    if (!firstToggle || !firstToggle.textContent.includes("Loaded plugin: slack")) return { ok: false, detail: "single-plugin toggle wrong: " + (firstToggle && firstToggle.textContent) };
    if (!secondToggle || !secondToggle.textContent.includes("Loaded plugins: github, superpowers")) return { ok: false, detail: "multi-plugin toggle wrong: " + (secondToggle && secondToggle.textContent) };
    for (const run of systemRuns) {
      if (run.querySelector("[data-system-plugin-name]")) return { ok: false, detail: "plugin row leaked into system run: " + run.innerHTML };
    }
    return { ok: true };
  });

  // The renderer must use structured plugin metadata, not a non-empty display
  // name, to keep plugin-loaded events out of generic system-event runs.
  await scenario("plugin-loaded event without a name still gets plugin disclosure", async ({ conv, R }) => {
    R.handleData("SYSTEM_MESSAGE", { title: "Skill activated", text: "release skill" });
    R.handleData("SYSTEM_MESSAGE", { title: "Loaded plugin (3 skills, 0 agents, 0 MCP servers)", eventKind: "plugin_loaded", text: "Loaded plugin (3 skills, 0 agents, 0 MCP servers)", raw: { pluginLoaded: { name: "" } } });
    R.handleData("SYSTEM_MESSAGE", { title: "Tools (2)", text: "- read_file\n- apply_patch" });
    const pluginRuns = conv.querySelectorAll(".plugin-run");
    if (pluginRuns.length !== 1) return { ok: false, detail: "expected one nameless plugin run, got " + pluginRuns.length };
    const toggle = pluginRuns[0].querySelector(".system-run-toggle");
    if (!toggle || !toggle.textContent.includes("Loaded plugin")) return { ok: false, detail: "nameless plugin toggle wrong: " + (toggle && toggle.textContent) };
    const systemRuns = conv.querySelectorAll(".system-run:not(.plugin-run)");
    if (systemRuns.length !== 2) return { ok: false, detail: "expected plugin load to split surrounding system events, got " + systemRuns.length + " system runs" };
    return { ok: true };
  });

  if (!allPass) process.exit(1);
  console.log("PASS: system churn — quiet one-liner, coalescing, no 'N chars'");
  process.exit(0);
})();
