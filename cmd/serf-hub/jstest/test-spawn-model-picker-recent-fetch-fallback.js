// Model picker Recent group renders via listModelsWithDiagnosticsForHarness's
// fetch("/api/models?...") call — its sole data source (the appwire RPC
// ModelList response has no display_name/badge fields to enrich the picker
// with, so this function always goes through the REST endpoint instead; see
// spawn.js's comment on that function). Overlaps with
// test-spawn-model-picker-recent.js's coverage now that both mock
// window.fetch; kept separate as it predates that test's fix and exercises
// the same real code path independently.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const spawnSrc = fs.readFileSync(path.resolve(__dirname, "../assets/spawn.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 0));

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <form data-spawn-form>
      <div id="spawn-chips">
        <button class="btn btn-chip" type="button" data-chip="model">
          <span class="chip-value" data-chip-value-model>(pick a model)</span>
        </button>
      </div>
      <textarea name="prompt"></textarea>
      <input type="hidden" name="harness" value="serf">
      <input type="hidden" name="model" value="">
      <input type="hidden" name="working_dir" value="">
      <input type="hidden" name="branch" value="">
      <input type="hidden" name="access_mode" value="full">
      <input type="hidden" name="agent" value="default">
      <input type="hidden" name="reasoning_effort" value="">
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });
  const { window } = dom;

  const recentModel = { provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6" };
  const otherModel = { provider: "openai", model: "gpt-5.2", display_name: "Gpt 5.2" };

  // No window.SerfAppwire defined at all — forces
  // listModelsWithDiagnosticsForHarness down its raw-fetch("/api/models?...")
  // fallback branch.
  window.fetch = (url) => {
    if (String(url).indexOf("/api/models") === 0) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({
          models: [recentModel, otherModel],
          diagnostics: [],
          recent: [recentModel],
        }),
      });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  };

  window.eval(spawnSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  window.document.querySelector('button[data-chip="model"]').click();
  await flush();
  await flush();

  const groups = Array.from(window.document.querySelectorAll(".chip-picker-group")).map(g => g.textContent);
  pass(groups[0] === "Recent", "Recent group renders first via fetch fallback, got groups=" + JSON.stringify(groups));
  pass(groups.includes("anthropic") && groups.includes("openai"), "provider groups still render: " + JSON.stringify(groups));

  const recentGroup = window.document.querySelectorAll(".chip-picker-group")[0];
  const rowAfterRecentHeader = recentGroup.nextElementSibling;
  pass(rowAfterRecentHeader && rowAfterRecentHeader.textContent.includes("Claude Opus 4 6"),
    "the row directly after the Recent header is the recent model");

  if (failures.length === 0) {
    console.log("PASS: spawn.js model picker Recent group (fetch fallback, no SerfAppwire)");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
