// CSS/layout contract test for the Show-cost toggle: when
// body[data-show-cost="false"], all three cost-bearing surfaces must be
// hidden — the status-item.cost (Phase R, input strip), [data-row="cost"]
// (Phase V, details panel), and .turn-meta .cost (Task T1, per-turn badge).
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// Parse top-level { selector, body } blocks from cssText, counting nested braces
// so that multi-rule @media/@supports bodies are correctly bounded. Mirrors
// test-pane-and-sidebar-css.js's parser.
function parseTopLevelBlocks(cssText) {
  const blocks = [];
  let i = 0;
  const n = cssText.length;
  while (i < n) {
    const start = i;
    while (i < n && cssText[i] !== "{") {
      if (cssText[i] === "/" && cssText[i + 1] === "*") {
        const end = cssText.indexOf("*/", i + 2);
        i = end === -1 ? n : end + 2;
      } else {
        i++;
      }
    }
    if (i >= n) break;
    const selector = cssText.slice(start, i).replace(/\/\*[\s\S]*?\*\//g, "").trim();
    i++; // skip '{'
    let depth = 1;
    const bodyStart = i;
    while (i < n && depth > 0) {
      if (cssText[i] === "{") depth++;
      else if (cssText[i] === "}") depth--;
      i++;
    }
    const body = cssText.slice(bodyStart, i - 1);
    if (selector) blocks.push({ selector, body });
  }
  return blocks;
}

function searchInBlocks(cssText, selector, pattern) {
  for (const block of parseTopLevelBlocks(cssText)) {
    if (block.selector.startsWith("@")) {
      if (searchInBlocks(block.body, selector, pattern)) return true;
    } else {
      const selectors = block.selector.split(",").map((s) => s.trim());
      if (selectors.includes(selector) && pattern.test(block.body)) return true;
    }
  }
  return false;
}

function ruleContains(selector, pattern) {
  return searchInBlocks(css, selector, pattern);
}

const HIDE = /display:\s*none/;
pass(
  ruleContains('body[data-show-cost="false"] .status-item.cost', HIDE),
  'body[data-show-cost="false"] should hide .status-item.cost (input strip)'
);
pass(
  ruleContains('body[data-show-cost="false"] [data-row="cost"]', HIDE),
  'body[data-show-cost="false"] should hide [data-row="cost"] (details panel)'
);
pass(
  ruleContains('body[data-show-cost="false"] .turn-meta .cost', HIDE),
  'body[data-show-cost="false"] should hide .turn-meta .cost (per-turn badge)'
);

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: show-cost CSS gating contracts");
process.exit(0);
