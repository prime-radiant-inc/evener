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

if (failures.length) {
  console.error("FAIL: transcript typography contract");
  for (const failure of failures) console.error("- " + failure);
  process.exit(1);
}
console.log("PASS: transcript typography contract");
