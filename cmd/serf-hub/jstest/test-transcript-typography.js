const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.join(__dirname, "../assets/style.css"), "utf8");
const failures = [];
const pass = (condition, message) => { if (!condition) failures.push(message); };
const rule = (selector) => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const matches = [...css.matchAll(new RegExp("(?:^|,)\\s*" + escaped + "\\s*(?:,|\\{)([^}]*)\\}", "gm"))];
  return matches.map((match) => match[1]).join("\n");
};

const toolCall = rule(".tool-call");
pass(/font-family:\s*var\(--font-sans\)/.test(toolCall),
  ".tool-call must use the sans base for human-facing tool-row text");

const command = rule(".tool-call .tool-command");
pass(/font-family:\s*var\(--font-mono\)/.test(command),
  ".tool-command must retain monospace for command detail");

const result = rule(".tool-call .result-detail");
pass(/font-family:\s*var\(--font-sans\)/.test(result),
  ".result-detail must use sans for a human-facing result summary");

const meta = rule(".tool-call .tool-meta");
pass(/font-family:\s*var\(--font-sans\)/.test(meta),
  ".tool-meta must use sans for timing/status chrome");

const disclosure = rule(".tool-disclosure");
pass(/font-family:\s*var\(--font-sans\)/.test(disclosure),
  ".tool-disclosure must use sans for human-facing control chrome");

const steering = rule(".steering .steering-body");
pass(/font-family:\s*var\(--font-sans\)/.test(steering),
  ".steering-body must use sans for human-authored steering text");
pass(!/white-space:\s*pre-wrap/.test(steering),
  ".steering-body must not preserve whitespace as a default prose treatment");

const shell = rule(".shell-output");
pass(/font-family:\s*var\(--font-mono\)/.test(shell) && /white-space:\s*pre-wrap/.test(shell),
  ".shell-output must remain preformatted monospace machine output");

const args = rule(".cheap-tool-args");
pass(/font-family:\s*var\(--font-mono\)/.test(args) && /white-space:\s*pre-wrap/.test(args),
  ".cheap-tool-args must remain preformatted monospace JSON/arguments");

const settingsDD = rule(".transcript-settings dd");
pass(/font-family:\s*var\(--font-sans\)/.test(settingsDD),
  "transcript settings value cells must default to sans instead of inherited monospace");

const settingsHelp = rule(".transcript-settings .help");
pass(/font-family:\s*var\(--font-sans\)/.test(settingsHelp) && /line-height:\s*var\(--leading-snug\)/.test(settingsHelp),
  "transcript settings help must be readable sans secondary prose");

const settingsState = rule(".transcript-settings .val-toggle .state");
pass(/font-family:\s*var\(--font-mono\)/.test(settingsState),
  "compact ON/OFF state text remains a monospace exception");

pass(css.includes("@media (max-width: 900px) and (max-height: 560px)"),
  "short-landscape media query must exist");
pass(css.includes("#sidebar,\n  #sidebar-resizer,\n  .side-panes,\n  .pane-splitter { display: none !important; }"),
  "short-landscape mode must remove persistent sidebar and pane chrome");
pass(css.includes(".message-input { min-height: 24px; max-height: 20dvh; }"),
  "short-landscape composer must use a bounded composing height");
pass(css.includes(".app-nav-toggle {\n    display: inline-flex;\n    position: fixed;"),
  "short-landscape mode must retain a fixed app navigation control to open the sidebar drawer");

// ── One text column + marker gutter (2026-07-11 typography redesign) ──────
// Every transcript entry's text starts at x=0; markers/rails hang into the
// --gutter. No entry may reintroduce a per-kind indent via --tool-indent.
pass(/\.conversation\s*\{[^}]*--gutter:/m.test(css),
  ".conversation must define the shared --gutter marker-gutter token");
pass(!css.includes("--tool-indent"),
  "the per-kind --tool-indent scheme is retired; use the --gutter marker gutter");
for (const railed of [".tool-body", ".think-body", ".task-list-body", ".fetch-body", ".search-body", ".subs", ".task-card"]) {
  const body = rule(railed);
  pass(/margin[^;]*calc\(-1 \* var\(--gutter\)\)/.test(body) && /padding[^;]*var\(--gutter\)/.test(body),
    railed + " must hang its rail in the marker gutter (negative margin) and keep its text on the x=0 column");
}
const toolStatus = rule(".tool-call .tool-status");
pass(/margin-left:\s*calc\(-1 \* var\(--gutter\)\)/.test(toolStatus),
  ".tool-status glyph must hang in the marker gutter, not indent the row text");
const thinkGlyph = rule(".think .think-glyph");
pass(/margin-left:\s*calc\(-1 \* var\(--gutter\)\)/.test(thinkGlyph),
  ".think-glyph marker must hang in the marker gutter like .tool-status");
const demotedCommand = rule(".tool-call.has-purpose .tool-command");
pass(!/padding-left/.test(demotedCommand),
  "the demoted command must sit on the same text column as the intent (no sub-indent)");

// ── Timing metadata is hover/focus-revealed, but stays accessible ─────────
pass(/opacity:\s*0\b/.test(rule(".tool-call .tool-meta")),
  ".tool-meta rests hidden (opacity, so it stays in the accessibility tree)");
pass(/\.tool-call:hover \.tool-meta,\s*\.tool-call:focus-within \.tool-meta\s*\{\s*opacity:\s*1/.test(css),
  ".tool-meta must reveal on row hover and keyboard focus-within");
pass(/opacity:\s*0\b/.test(rule(".assistant-message .turn-meta")),
  ".turn-meta rests hidden (opacity, so it stays in the accessibility tree)");
pass(/\.assistant-message:hover \.turn-meta,\s*\.assistant-message:focus-within \.turn-meta\s*\{\s*opacity:\s*1/.test(css),
  ".turn-meta must reveal on message hover and keyboard focus-within");

// ── Reduced type scale: transcript text uses lg/base/sm/xs only ───────────
pass(!/font-family:\s*var\(--font-mono\)/.test(rule(".assistant-message .turn-meta")),
  "durations/tokens chrome is sans (tabular-nums), not mono");
for (const sized of [".task-list-body .task-type", ".diagnostic-badge", ".diagnostic-detail-pre", ".task-card-note"]) {
  pass(!/--text-2xs/.test(rule(sized)),
    sized + " must not use the sub-scale --text-2xs size in the transcript");
}

// ── Round 2 (2026-07-11): ONE flowing reading size + one disclosure idiom ─
// All flowing reading text (assistant prose, tool intents/results, thinking,
// user messages, system asides) sits at --text-base; mono machine text is the
// --text-sm exception; true meta (timestamps, YOU, durations) is --text-xs.
for (const flowing of [".assistant-message", ".tool-call", ".think", ".system-line", ".system-message", ".steering", ".user-message .pill", ".tool-body", ".fetch-body", ".search-body"]) {
  pass(/font-size:\s*var\(--text-base\)/.test(rule(flowing)),
    flowing + " must sit at the one --text-base flowing reading size");
}
pass(!/font-size:\s*var\(--text-lg\)/.test(rule(".assistant-message")),
  "assistant prose dropped from --text-lg to --text-base (round 2 decision)");
pass(/font-size:\s*var\(--text-sm\)/.test(rule(".tool-call .tool-command")),
  "mono command text is the --text-sm machine exception");
pass(!/font-size/.test(rule(".tool-call.has-purpose .tool-command")),
  "the demoted command inherits the mono --text-sm step (color, not size, carries the demotion)");
for (const consistent of [".assistant-message", ".tool-call", ".think", ".user-message .pill", ".system-line"]) {
  pass(/line-height:\s*var\(--leading-normal\)/.test(rule(consistent)),
    consistent + " must share the column line-height (--leading-normal)");
}

// ── Round 2: one disclosure idiom — › glyph, muted, rotate-on-open, ≥24px ─
const disc = rule(".tool-disclosure");
pass(/width:\s*24px/.test(disc) && /height:\s*24px/.test(disc),
  ".tool-disclosure must have a 24px hit target");
pass(/color:\s*var\(--text-muted\)/.test(disc) && /font-size:\s*var\(--text-md\)/.test(disc),
  ".tool-disclosure glyph must be comfortably visible (muted, --text-md)");
pass(/\.tool-disclosure\[aria-expanded="true"\]\s*\{\s*transform:\s*rotate\(90deg\)/.test(css),
  ".tool-disclosure must rotate its › 90° when expanded");
pass(/\.fold-chevron\.open\s*\{\s*transform:\s*rotate\(90deg\)/.test(css),
  "the shared .fold-chevron must rotate 90° when its fold opens");
pass(/content:\s*"›"/.test(rule(".notification-card-raw > summary::after")),
  "notification/diagnostic fold summaries must use the shared › disclosure glyph");
pass(/content:\s*"›"/.test(rule(".system-message > summary::before")),
  "system-message folds must use the shared › disclosure glyph");
pass(!css.includes('content: "▸"') || !/(notification-card|system-message|diagnostic)[^{}]*\{[^}]*content:\s*"▸"/.test(css),
  "no transcript fold may keep the tiny ▸ triangle glyph");

if (failures.length) {
  console.error("FAIL: transcript typography contract");
  for (const failure of failures) console.error("- " + failure);
  process.exit(1);
}
console.log("PASS: transcript typography contract");
