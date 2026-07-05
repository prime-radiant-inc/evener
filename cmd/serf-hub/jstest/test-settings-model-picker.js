// Settings model picker: envelope fetch (diagnostics=1), Recent pinned-first
// provider tab, prettified names + badges (kata settings-model-picker).
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const settingsPickersSrc = fs.readFileSync(path.resolve(__dirname, "../assets/settings-pickers.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 0));

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="sp-model-wrap">
      <button data-settings-model-picker type="button">choose</button>
      <input type="hidden" name="cheap_model" value="">
      <span class="sp-model-display"></span>
    </div>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/settings",
  });
  const { window } = dom;

  const recentModel = { provider: "anthropic", model: "claude-opus-4-6", display_name: "Claude Opus 4 6", supports_tools: true };
  const otherModel = { provider: "openai", model: "gpt-5.2", display_name: "Gpt 5.2" };
  let requestedURL = null;
  window.fetch = (url) => {
    requestedURL = url;
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ models: [recentModel, otherModel], diagnostics: [], recent: [recentModel] }),
    });
  };

  window.eval(settingsPickersSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await flush();

  pass(requestedURL && requestedURL.includes("diagnostics=1"), "settings picker fetches the diagnostics envelope, got " + requestedURL);

  window.document.querySelector("button[data-settings-model-picker]").click();
  await flush();

  const providerTabs = Array.from(window.document.querySelectorAll(".chip-picker-provider")).map(p => p.textContent);
  pass(providerTabs[0] === "Recent", "Recent is the first provider tab, got " + JSON.stringify(providerTabs));

  const modelRows = window.document.querySelectorAll(".chip-picker-model");
  pass(modelRows.length === 1 && modelRows[0].textContent.includes("Claude Opus 4 6"),
    "Recent tab is active by default and shows the recent model");
  pass(modelRows[0].querySelector(".chip-picker-badge") && modelRows[0].textContent.includes("tools"),
    "badges render in the settings picker too");

  if (failures.length === 0) {
    console.log("PASS: settings-pickers.js model picker");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
