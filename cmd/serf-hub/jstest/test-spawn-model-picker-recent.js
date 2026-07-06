// Model picker: Recent group renders above the provider-grouped catalog and
// is filtered by the same search box (kata model-picker-recent). Mocks
// window.fetch("/api/models?...") — the picker's sole data source
// (listModelsWithDiagnosticsForHarness always goes through the REST
// endpoint; the appwire RPC ModelList response has no display_name/badge
// fields to enrich the picker with, see spawn.js's comment on that
// function).
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

  const groups = Array.from(window.document.querySelectorAll(".chip-picker-group")).map(g => g.textContent);
  pass(groups[0] === "Recent", "Recent group renders first, got groups=" + JSON.stringify(groups));
  pass(groups.includes("anthropic") && groups.includes("openai"), "provider groups still render: " + JSON.stringify(groups));

  const recentGroup = window.document.querySelectorAll(".chip-picker-group")[0];
  const rowAfterRecentHeader = recentGroup.nextElementSibling;
  pass(rowAfterRecentHeader && rowAfterRecentHeader.textContent.includes("Claude Opus 4 6"),
    "the row directly after the Recent header is the recent model");

  // Filtering narrows Recent too.
  const search = window.document.querySelector(".chip-picker-search");
  search.value = "gpt";
  search.dispatchEvent(new window.Event("input", { bubbles: true }));
  const groupsAfterFilter = Array.from(window.document.querySelectorAll(".chip-picker-group")).map(g => g.textContent);
  pass(!groupsAfterFilter.includes("Recent"), "Recent group hides when its only entry doesn't match the filter: " + JSON.stringify(groupsAfterFilter));

  if (failures.length === 0) {
    console.log("PASS: spawn.js model picker Recent group");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
