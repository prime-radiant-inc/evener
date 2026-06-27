const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const mobileStart = css.indexOf("@media (max-width: 767px)");
const mobile = mobileStart >= 0 ? css.slice(mobileStart) : "";

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(mobileStart >= 0, "mobile media query exists");
pass(/\.search-dialog-inner\s*\{[^}]*height:\s*100vh/s.test(mobile), "mobile palette is full-height");
pass(/\.search-dialog-header\s*\{[^}]*position:\s*sticky[^}]*top:\s*0/s.test(mobile), "mobile palette header is sticky");
pass(/\.search-results\s*\{[^}]*max-height:\s*calc\(100vh - 64px\)/s.test(mobile), "mobile results are viewport-height constrained");
pass(/\.search-row\s*\{[^}]*min-height:\s*48px/s.test(mobile), "mobile command rows have touch-sized targets");
pass(/\.search-cmd-pill\s*\{[^}]*flex-wrap:\s*wrap[^}]*max-width:\s*100%/s.test(mobile), "mobile args pill wraps within viewport");

// Blank-screen fix: the in-flow, full-height #sidebar-resizer must be hidden on
// phone, or it shoves #workspace a whole viewport below the fold.
pass(/#sidebar-resizer\s*\{[^}]*display:\s*none/s.test(mobile),
  "mobile must hide #sidebar-resizer (else the workspace is pushed off the fold)");

// Column-scrollbar guard: the transcript never scrolls sideways. There are
// several `.conversation { … }` rules (e.g. a one-line motion reveal); the one
// that matters is the scroll-container block, identified by overflow-y: auto.
const convScrollRule = (css.match(/\.conversation\s*\{[^}]*\}/g) || [])
  .find((block) => /overflow-y:\s*auto/.test(block));
pass(convScrollRule && /overflow-x:\s*clip/.test(convScrollRule),
  ".conversation scroll container must set overflow-x: clip so no stray-wide element forces a horizontal scrollbar");

// Horizontal-anchor guard (iOS keyboard): the demoted command line under a
// tool's purpose is flex-basis:100%, so it MUST indent with padding, not a
// margin — a margin pushes it past the row's right edge, a horizontal overflow
// iOS scrolls to on keyboard focus, leaving the page shifted and un-anchored.
const purposeCmdRule = (css.match(/\.tool-call\.has-purpose \.tool-command\s*\{[^}]*\}/g) || [])[0];
pass(purposeCmdRule && /padding-left:/.test(purposeCmdRule) && !/margin-left:/.test(purposeCmdRule),
  ".tool-call.has-purpose .tool-command must indent with padding-left, not margin-left (flex-basis:100% + margin overflows the row)");

// Belt-and-suspenders: the workspace clips horizontal overflow so no stray-wide
// descendant can shift the page sideways during keyboard focus.
pass(/#workspace\s*\{[^}]*overflow-x:\s*clip/s.test(mobile),
  "mobile #workspace must set overflow-x: clip as a horizontal-anchor guard");

if (failures.length === 0) {
  console.log("PASS: mobile search palette CSS contract + layout guards");
  process.exit(0);
}
for (const failure of failures) console.log(failure);
process.exit(1);
