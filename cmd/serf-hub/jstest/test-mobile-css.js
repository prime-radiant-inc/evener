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

// iOS zoom guard: a focused field whose text is < 16px makes iOS Safari zoom
// the page in (and never zoom back out on blur). Editable fields must be 16px
// on phone — checked via the rule whose selector includes .message-input.
const inputZoomRule = (mobile.match(/[^{}]*\{[^}]*\}/g) || [])
  .find((block) => /\.message-input/.test(block.split("{")[0]) && /font-size:\s*16px/.test(block));
pass(!!inputZoomRule,
  "mobile must pin editable fields (incl .message-input) to font-size: 16px so iOS does not auto-zoom on focus");

// Phone reading hierarchy: the hero prose must be a larger step than the tool
// machine text. An inverted scale (a small hero under a larger tool purpose)
// reads as "the tool matters more than the answer". Lock the compact sizes.
const blocks = mobile.match(/[^{}]*\{[^}]*\}/g) || [];
const heroRule = blocks.find((b) => /\.assistant-message/.test(b.split("{")[0]) && /font-size:\s*var\(--text-md\)/.test(b));
pass(!!heroRule, "compact phone density must size the assistant hero prose at --text-md (largest reading text)");
const machineRule = blocks.find((b) => /\.tool-call\b/.test(b.split("{")[0]) && /font-size:\s*var\(--text-sm\)/.test(b));
pass(!!machineRule, "compact phone density must size tool/machine text at --text-sm (legible, not the old 10px)");

if (failures.length === 0) {
  console.log("PASS: mobile search palette CSS contract + layout guards");
  process.exit(0);
}
for (const failure of failures) console.log(failure);
process.exit(1);
