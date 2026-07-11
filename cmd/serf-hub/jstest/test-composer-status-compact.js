const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const workspace = fs.readFileSync(path.resolve(__dirname, "../templates/partials/workspace.html"), "utf8");
const inputStrip = fs.readFileSync(path.resolve(__dirname, "../templates/partials/input_strip.html"), "utf8");

function extractMediaContents(src, query) {
  const contents = [];
  let searchFrom = 0;
  while (searchFrom < src.length) {
    const start = src.indexOf(query, searchFrom);
    if (start === -1) break;
    const open = src.indexOf("{", start + query.length);
    if (open === -1) break;
    let depth = 1;
    let i = open + 1;
    while (i < src.length && depth > 0) {
      if (src[i] === "{") depth++;
      else if (src[i] === "}") depth--;
      i++;
    }
    if (depth !== 0) break;
    contents.push(src.slice(open + 1, i - 1));
    searchFrom = i;
  }
  return contents;
}

const mobile = extractMediaContents(css, "@media (max-width: 767px)").join("\n");
const shortLandscape = extractMediaContents(css, "@media (max-width: 900px) and (max-height: 560px)").join("\n");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(/class="input-telemetry" data-input-telemetry/.test(inputStrip),
  "input status must expose one [data-input-telemetry] rail");
pass(/data-status-location/.test(inputStrip) && /status-location-part branch/.test(inputStrip) &&
  /status-location-part worktree/.test(inputStrip) && /status-location-part cwd/.test(inputStrip),
  "rail must group branch, worktree, and cwd location parts");
pass(/data-status-context/.test(inputStrip) && /CompactContextNumbers/.test(inputStrip),
  "rail must render compact used/window context telemetry");
for (const removed of ["status-item source", "status-item turns", "status-item work", "status-item tokens", "status-item cost", "status-item goal"]) {
  pass(!inputStrip.includes(removed), `compact rail must not render ${removed}`);
}
pass(!/task-status-row/.test(workspace), "task trigger must not consume a standalone task-status row");
pass(/controls-left[\s\S]*data-tasks-trigger/.test(workspace),
  "task trigger must live inside the composer controls-left group");

const telemetryRule = (css.match(/\.input-telemetry\s*\{[^}]*\}/g) || [])[0];
pass(telemetryRule && /display:\s*flex/.test(telemetryRule) && /flex-wrap:\s*nowrap/.test(telemetryRule) && /min-width:\s*0/.test(telemetryRule),
  "telemetry rail must be a nonwrapping flex line with min-width:0");
const cardRule = (css.match(/\.input-card\s*\{[^}]*\}/g) || [])[0];
pass(cardRule && /background:/.test(cardRule) && !/border:\s*1px/.test(cardRule),
  "input card must retain a raised background without a default 1px border");

pass(/\.input-telemetry \.location\s*\{[^}]*min-width:\s*0[^}]*overflow:\s*hidden/.test(css),
  "location group must be shrinkable and clipped before telemetry wraps");
pass(/\.input-telemetry \.status-location-part \.status-value\s*\{[^}]*min-width:\s*0[^}]*text-overflow:\s*ellipsis/.test(css),
  "location values must be shrinkable so text-overflow produces ellipsis");
// Borderless phone composer: the dock surface is the ONE containment device.
// The inner card must be transparent, unpadded, and must not redraw a focus
// box around the textarea+controls block (focus reads from the dock's
// top-rule tint instead).
const phoneCardRule = (mobile.match(/\.input-card\s*\{[^}]*\}/g) || [])[0];
pass(phoneCardRule && /background:\s*transparent/.test(phoneCardRule) && /border:\s*0/.test(phoneCardRule) && /padding:\s*0/.test(phoneCardRule),
  "phone .input-card must be a transparent, borderless, unpadded pass-through");
pass(/\.input-card:focus-within\s*\{[^}]*outline:\s*none/.test(mobile),
  "phone must suppress the card focus-within outline (dock top rule carries focus)");

// De-cluttered phone rail: no "branch" label word, no current-task prose,
// and the tasks trigger only surfaces while the task list is unfinished.
pass(/\.input-telemetry \.status-location-part\.branch \.status-key\s*\{[^}]*display:\s*none/.test(mobile),
  "phone rail must drop the 'branch' label word");
pass(/\.tasks-status \.status-value\s*\{[^}]*display:\s*none/.test(mobile),
  "phone tasks trigger must drop the current-task prose (count badge carries it)");
pass(/\.tasks-status\[data-tasks-signal="none"\],\s*\.tasks-status\[data-tasks-signal="done"\]\s*\{[^}]*display:\s*none/.test(mobile),
  "phone tasks trigger must hide at rest (no tasks / all complete)");
pass(/data-tasks-trigger data-tasks-signal="none"/.test(workspace),
  "tasks trigger must boot in the no-signal state until the badge updater runs");
pass(/\.input-controls \.stop-btn\[disabled\],\s*\.input-controls \.steer-btn\[disabled\]\s*\{[^}]*display:\s*none/.test(mobile),
  "phone must hide inert stop/steer controls instead of dimming them");

pass(/\.input-telemetry\s*\{[^}]*flex-wrap:\s*nowrap/.test(mobile),
  "phone media aggregate must include the compact nonwrapping telemetry override");
pass(!/\.input-telemetry\s*\{[^}]*flex-wrap:\s*wrap/.test(mobile),
  "phone telemetry rail must not wrap into a second metadata line");
pass(!/\.input-telemetry\s*\{[^}]*flex-wrap:\s*wrap/.test(shortLandscape),
  "short-landscape telemetry rail must not wrap into a second metadata line");

if (failures.length > 0) {
  for (const failure of failures) console.log(failure);
  process.exit(1);
}
console.log("PASS: composer/status compact rail contract");
