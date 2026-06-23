// CSS/layout contract tests for compact side panes and native sidebar resizing.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function ruleContains(selector, pattern) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(escaped + "\\s*\\{([^}]*)\\}", "m");
  const m = css.match(re);
  return !!(m && pattern.test(m[1]));
}

// Right-pane compact footer/input: no extra rule or full-line gap above metadata.
pass(
  ruleContains(".thread-document .input-status", /border-top:\s*0\b/) ||
    ruleContains(".pane-compact .input-status", /border-top:\s*0\b/),
  "thread/pane input status should remove the extra top border"
);
pass(
  ruleContains(".pane-compact .workspace-input", /padding:\s*var\(--space-2\)/) ||
    ruleContains(".thread-document .workspace-input", /padding:\s*var\(--space-2\)/),
  "pane workspace input should use compact padding"
);
pass(
  ruleContains(".pane-compact .input-card", /padding:\s*var\(--space-2\)/),
  "pane input card should use compact padding"
);
pass(
  ruleContains(".pane-compact .message-input", /min-height:\s*(28|30|32)px/),
  "pane message input should have a shorter min-height"
);
pass(
  ruleContains(".pane-compact .send-btn", /align-self:\s*stretch/) === false,
  "pane send button should not be stretched tall"
);
pass(
  ruleContains(".pane-compact .controls-right", /margin-left:\s*auto/),
  "pane composer action buttons should be pushed to the right edge"
);

// Left sidebar: use native browser resizing, not custom JS.
pass(ruleContains("#sidebar", /resize:\s*horizontal/), "left sidebar should use native horizontal resize");
pass(ruleContains("#sidebar", /min-width:/), "left sidebar should have a CSS min-width");
pass(ruleContains("#sidebar", /max-width:/), "left sidebar should have a CSS max-width");
pass(
  ruleContains("body\.app\[data-sidebar-rail\] #sidebar", /resize:\s*none/),
  "rail-mode sidebar should disable native resize"
);

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: pane compact and native sidebar resize CSS contracts");
process.exit(0);
