// Model picker: prettified display name + raw id secondary line + capability
// badges (kata model-picker-badges). Loads spawn.js in JSDOM, stubs
// SerfAppwire.listModelsWithDiagnostics to return one catalogued and one
// uncatalogued model, opens the model chip picker, asserts on the rendered
// DOM.
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

  window.SerfAppwire = {
    listModels(params) {
      return Promise.resolve({
        models: [
          {
            provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6",
            supports_tools: true, supports_vision: true, supports_reasoning: true,
            reasoning_effort_levels: ["low", "medium", "high", "max"], supports_web_search: true,
            context_window: 1000000, max_output_tokens: 128000,
            input_cost_per_million: 5, output_cost_per_million: 25,
          },
          { provider: "mycompany", model: "unknown-model", display_name: "Unknown Model" },
        ],
      });
    },
    listModelsWithDiagnostics(params) {
      return Promise.resolve({
        models: [
          {
            provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6",
            supports_tools: true, supports_vision: true, supports_reasoning: true,
            reasoning_effort_levels: ["low", "medium", "high", "max"], supports_web_search: true,
            context_window: 1000000, max_output_tokens: 128000,
            input_cost_per_million: 5, output_cost_per_million: 25,
          },
          { provider: "mycompany", model: "unknown-model", display_name: "Unknown Model" },
        ],
        diagnostics: [],
      });
    },
  };

  window.eval(spawnSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  window.document.querySelector('button[data-chip="model"]').click();
  await flush();

  const rows = window.document.querySelectorAll(".chip-picker-model");
  pass(rows.length === 2, "both models rendered, got " + rows.length);

  const catalogued = Array.from(rows).find(r => r.textContent.includes("Claude Opus 4 6"));
  pass(catalogued, "catalogued model row rendered with prettified name");
  pass(catalogued && catalogued.querySelector(".chip-picker-model-id").textContent === "claude-opus-4-6",
    "secondary line shows the raw id");
  const badges = catalogued ? Array.from(catalogued.querySelectorAll(".chip-picker-badge")).map(b => b.textContent) : [];
  pass(badges.includes("tools"), "tools badge present: " + badges.join(","));
  pass(badges.includes("vision"), "vision badge present: " + badges.join(","));
  pass(badges.some(b => b.startsWith("reasoning")), "reasoning badge present: " + badges.join(","));
  pass(badges.includes("web search"), "web search badge present: " + badges.join(","));
  pass(catalogued && catalogued.textContent.includes("1M ctx"), "context window meta present");
  pass(catalogued && catalogued.textContent.includes("$5.00/M in"), "input cost meta present");

  const uncatalogued = Array.from(rows).find(r => r.textContent.includes("Unknown Model"));
  pass(uncatalogued, "uncatalogued model still renders");
  pass(uncatalogued && uncatalogued.querySelectorAll(".chip-picker-badge").length === 0,
    "uncatalogued model has no badges");

  if (failures.length === 0) {
    console.log("PASS: spawn.js model picker badges");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
