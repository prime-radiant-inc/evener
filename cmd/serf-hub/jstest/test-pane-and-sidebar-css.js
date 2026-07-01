// CSS/layout contract tests for compact side panes and full-border sidebar resizing.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// Parse top-level { selector, body } blocks from cssText, counting nested braces
// so that multi-rule @media/@supports bodies are correctly bounded.
function parseTopLevelBlocks(cssText) {
  const blocks = [];
  let i = 0;
  const n = cssText.length;
  while (i < n) {
    const start = i;
    // Advance to the next '{', skipping /* … */ comments.
    while (i < n && cssText[i] !== "{") {
      if (cssText[i] === "/" && cssText[i + 1] === "*") {
        const end = cssText.indexOf("*/", i + 2);
        i = end === -1 ? n : end + 2;
      } else {
        i++;
      }
    }
    if (i >= n) break;
    // Strip inline comments from the selector fragment so they don't corrupt
    // the selector string we compare against.
    const selector = cssText.slice(start, i).replace(/\/\*[\s\S]*?\*\//g, "").trim();
    i++; // skip '{'
    // Read the body, counting nested braces to find the matching '}'.
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

// Recursively search cssText for a rule whose comma-separated selectors include
// `selector` and whose declaration block matches `pattern`.  Descends into
// @media / @supports / @layer blocks so inner rules are reachable.
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

// Right-pane compact footer/input: no extra rule or full-line gap above metadata.
pass(
  ruleContains(".thread-document .input-status", /border-top:\s*0\b/) ||
    ruleContains(".pane-compact .input-status", /border-top:\s*0\b/),
  "thread/pane input status should remove the extra top border"
);
pass(
  ruleContains(".pane-compact .workspace-input", /padding:\s*var\(--space-1\)\s+var\(--space-3\)/),
  "pane workspace input should use very compact vertical padding"
);
pass(
  ruleContains(".pane-compact .input-card", /padding:\s*var\(--space-2\)/),
  "pane input card should use compact padding"
);
pass(
  ruleContains(".pane-compact .message-input", /min-height:\s*(24|26|28)px/),
  "pane message input should have a short single-line min-height"
);
pass(
  ruleContains(".pane-compact .send-btn", /align-self:\s*stretch/) === false,
  "pane send button should not be stretched tall"
);
pass(
  ruleContains(".pane-compact .controls-right", /margin-left:\s*auto/),
  "pane composer action buttons should be pushed to the right edge"
);

// Left sidebar: full-height border affordance, not the browser's bottom-corner handle.
pass(!ruleContains("#sidebar", /resize:\s*horizontal/), "left sidebar must not use browser corner resize");
pass(ruleContains("#sidebar", /width:\s*var\(--sidebar-w,\s*260px\)/), "left sidebar width should be driven by --sidebar-w");
pass(ruleContains("#sidebar", /min-width:/), "left sidebar should have a CSS min-width");
pass(ruleContains("#sidebar", /max-width:/), "left sidebar should have a CSS max-width");
pass(ruleContains(".sidebar-resizer", /cursor:\s*col-resize/), "left sidebar resizer should expose a full-border column-resize affordance");
pass(ruleContains(".sidebar-resizer", /height:\s*100vh/), "left sidebar resizer should span the full viewport height");
pass(
  ruleContains("body.app[data-sidebar-rail] .sidebar-resizer", /display:\s*none/),
  "rail-mode sidebar should hide the full-border resizer"
);

// Drag handles should present the same visual/click target thickness.
const sidebarWidth = (css.match(/\.sidebar-resizer\s*\{[^}]*width:\s*([^;]+);/m) || [])[1];
const paneWidth = (css.match(/\.pane-splitter\s*\{[^}]*width:\s*([^;]+);/m) || [])[1];
pass(!!sidebarWidth && !!paneWidth && sidebarWidth.trim() === paneWidth.trim(), "left and right drag handles should use the same thickness");

// The input metadata row should not be separated from the composer by an hr-like rule.
pass(
  ruleContains(".input-status", /border-top:\s*(0|none)\b/),
  "input status below the main composer should not draw a horizontal rule"
);

// Standalone thread documents are body.thread-document, not body.app; the legacy
// landing-page main padding must not apply to their <main> element.
pass(
  css.includes("body:not(.app):not(.thread-document) main"),
  "legacy body:not(.app) main padding should exclude thread-document pages"
);
pass(
  ruleContains(".thread-document main", /padding:\s*0\b/) && ruleContains(".thread-document main", /max-width:\s*none\b/),
  "thread-document main should remove legacy page padding and max-width"
);

// Transcript tool timing metadata is readable by default on desktop; it must not
// rely on hover/focus-only reveal behavior.
pass(
  ruleContains(".tool-call .tool-meta", /opacity:\s*1\b/) &&
    !ruleContains(".tool-call .tool-meta", /opacity:\s*0\b/) &&
    !ruleContains(".tool-call .tool-meta", /visibility:\s*hidden\b/),
  "tool timing metadata should be readable by default without visibility:hidden"
);
pass(
  !ruleContains(".tool-call:hover .tool-meta", /opacity:\s*1\b/) &&
    !ruleContains(".tool-call:focus-within .tool-meta", /opacity:\s*1\b/),
  "desktop tool timing metadata should not depend on hover/focus reveal rules"
);

// Job notification communicate output is already HTML rendered from markdown.
// Preserving parser-inserted whitespace on that HTML turns marked's formatting
// newlines into visible blank rows between every block/list item. Raw excerpts
// still preserve original text newlines through .notification-card-excerpt.
pass(
  !ruleContains(".notification-card-message", /white-space:\s*pre-wrap/),
  "job notification markdown messages should not preserve parser whitespace as visible blank lines"
);
pass(
  ruleContains(".notification-card-excerpt", /white-space:\s*pre-wrap/),
  "raw notification excerpts should still preserve output newlines"
);

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: pane compact and full-border sidebar resize CSS contracts");
process.exit(0);
