// Contrast: --ink-4 never colors words. It may appear ONLY in the
// documented decorative keep-list (fills and borders, not color:).
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const keepPatterns = [
  /\.project-rollup-dot \{[^}]*background:\s*var\(--ink-4\)/,
  /\.assistant-message code \{[^}]*border-bottom:\s*1px solid var\(--ink-4\)/,
  /\.subs\[data-stale="true"\] \{[^}]*border-left-color:\s*var\(--ink-4\)/,
  /\.task-card-meter-fill \{[^}]*background:\s*var\(--ink-4\)/,
  /\.details-meter-fill \{[^}]*background:\s*var\(--ink-4\)/,
  // 10px radio-dot border (border-radius: 50% in the post-Task-7 sheet).
  /width:\s*10px;[^}]*height:\s*10px;[^}]*border:\s*1px solid var\(--ink-4\)/,
  // Phone sheet grab-handle (36px × 4px pill inside the phone media band).
  /width:\s*36px;[^}]*height:\s*4px;[^}]*background:\s*var\(--ink-4\)/,
];
for (const re of keepPatterns) {
  assert(re.test(css), "keep-list site intact: " + re);
}
// No color: declaration may use --ink-4 anywhere. The lookbehind excludes
// longhand border properties (e.g. border-left-color) from matching.
assert(!/(?<![\w-])color:\s*var\(--ink-4\)/.test(css), "--ink-4 never colors words");
// Known word/glyph sites moved to --ink-3.
for (const sel of [".project-count", ".ask-question-num", ".tool-status-pending",
  ".notification-card-sub", ".tasks-list .task-row-chevron",
  "button.composer-model-value .caret", ".plan-item.pending .plan-glyph"]) {
  const block = css.match(new RegExp(sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + " \\{[^}]*\\}"));
  assert(block && block[0].includes("var(--ink-3)"), sel + " uses --ink-3");
}
console.log("ok contrast: --ink-4 is non-text only");
