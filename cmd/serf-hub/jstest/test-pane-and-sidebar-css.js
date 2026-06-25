// CSS/layout contract tests for compact side panes and full-border sidebar resizing.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function ruleContains(selector, pattern) {
  const re = /([^{}]+)\{([^}]*)\}/gm;
  for (const m of css.matchAll(re)) {
    const selectors = m[1].split(",").map((s) => s.trim());
    if (selectors.includes(selector) && pattern.test(m[2])) return true;
  }
  return false;
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

// Transcript tool timing metadata should stay out of the visual scan path until
// the user shows row-level intent, while remaining available to assistive tech.
pass(
  ruleContains(".tool-call .tool-meta", /opacity:\s*0\b/) &&
    !ruleContains(".tool-call .tool-meta", /visibility:\s*hidden\b/),
  "tool timing metadata should be visually hidden by default without visibility:hidden"
);
pass(
  ruleContains(".tool-call:hover .tool-meta", /opacity:\s*1\b/),
  "tool timing metadata should reveal on row hover"
);
pass(
  ruleContains(".tool-call:focus-within .tool-meta", /opacity:\s*1\b/),
  "tool timing metadata should reveal on keyboard focus within the row"
);

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: pane compact and full-border sidebar resize CSS contracts");
process.exit(0);
