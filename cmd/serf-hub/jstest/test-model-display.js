const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/model-display.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

function makeWindow(bodyHTML) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>${bodyHTML}</body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/",
  });
  return dom.window;
}

(function main() {
  const window = makeWindow('<span data-model-display>anthropic/claude-haiku-4-5-20251001</span>');
  window.SerfSpawn = { abbreviateModel: (full) => full.replace(/^[^/]+\//, "").replace(/-\d{8}$/, "") };
  window.eval(SRC);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));

  const el = window.document.querySelector("[data-model-display]");
  assert(el.textContent === "claude-haiku-4-5", "model chip abbreviates on load");
  assert(el.dataset.fullModel === "anthropic/claude-haiku-4-5-20251001", "full model id anchored in data-full-model");

  // A later htmx swap must not re-abbreviate an already-shortened string,
  // and must keep using the anchored full id (not the now-abbreviated textContent).
  window.document.body.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: window.document.body } }));
  assert(el.textContent === "claude-haiku-4-5", "re-swap keeps the abbreviated text stable");
  assert(el.dataset.fullModel === "anthropic/claude-haiku-4-5-20251001", "re-swap does not overwrite the anchored full id");

  // No SerfSpawn yet (script loaded before spawn.js resolves) — must no-op,
  // not throw.
  const early = makeWindow('<span data-model-display>anthropic/claude-opus-4-20250101</span>');
  early.eval(SRC);
  early.document.dispatchEvent(new early.Event("DOMContentLoaded", { bubbles: true }));
  assert(early.document.querySelector("[data-model-display]").textContent === "anthropic/claude-opus-4-20250101", "missing SerfSpawn leaves the raw id untouched, no throw");

  console.log("PASS — model-display abbreviation wiring survives extraction from settings.js");
})();
