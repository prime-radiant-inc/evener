// CSS/layout contract test for the font-size presets (S/M/L/XL): each preset
// must redefine all 8 --text-* tokens, and within each preset the values must
// stay in strictly ascending order (matching the base :root token block's
// order), so a later manual tuning pass can't silently invert the scale.
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

function findBlock(selector) {
  for (const block of parseTopLevelBlocks(css)) {
    if (block.selector.startsWith("@")) continue;
    const selectors = block.selector.split(",").map((s) => s.trim());
    if (selectors.includes(selector)) return block;
  }
  return null;
}

const TOKENS = ["--text-2xs", "--text-xs", "--text-sm", "--text-base", "--text-md", "--text-lg", "--text-xl", "--text-2xl"];
const PRESETS = ["s", "m", "l", "xl"];

for (const preset of PRESETS) {
  const selector = 'body[data-font-size="' + preset + '"]';
  const block = findBlock(selector);
  pass(!!block, selector + " block should exist");
  if (!block) continue;

  const values = [];
  for (const token of TOKENS) {
    const re = new RegExp(token.replace(/[-/\\^$*+?.()|[\]{}]/g, "\\$&") + ":\\s*(\\d+)px");
    const m = block.body.match(re);
    pass(!!m, selector + " should define " + token);
    if (m) values.push(Number(m[1]));
  }

  if (values.length === TOKENS.length) {
    let ascending = true;
    for (let i = 1; i < values.length; i++) {
      if (!(values[i] > values[i - 1])) ascending = false;
    }
    pass(ascending, selector + " token values should be strictly ascending in " + TOKENS.join(", ") + " order (got " + values.join(", ") + ")");
  }
}

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: font-size preset CSS contracts");
process.exit(0);
