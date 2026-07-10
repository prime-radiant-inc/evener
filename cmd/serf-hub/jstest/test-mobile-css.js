const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const mobileStart = css.indexOf("@media (max-width: 767px)");

// Extract only CSS text that lives INSIDE @media (max-width: 767px) { ... } blocks,
// tracking brace depth so nested @keyframes / @media blocks don't confuse the
// boundary detection. Without this, global rules that happen to follow the FIRST
// mobile query (line 511) are included in `mobile` and can satisfy mobile-specific
// assertions even when the real mobile rule is absent or overridden.
function extractMobileContent(src) {
  const QUERY = "@media (max-width: 767px)";
  let result = "";
  let i = 0;
  while (i < src.length) {
    const qi = src.indexOf(QUERY, i);
    if (qi === -1) break;
    const open = src.indexOf("{", qi);
    if (open === -1) break;
    let depth = 1;
    let j = open + 1;
    while (j < src.length && depth > 0) {
      if (src[j] === "{") depth++;
      else if (src[j] === "}") depth--;
      j++;
    }
    result += src.slice(open + 1, j - 1) + "\n";
    i = j;
  }
  return result;
}

function extractMediaContent(src, query) {
  const start = src.indexOf(query);
  if (start === -1) return "";
  const open = src.indexOf("{", start);
  if (open === -1) return "";
  let depth = 1;
  let i = open + 1;
  while (i < src.length && depth > 0) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}") depth--;
    i++;
  }
  return src.slice(open + 1, i - 1);
}

const mobile = extractMobileContent(css);

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

// Visual viewport coordination sets this variable while a workspace is active;
// retain a legacy viewport unit immediately before the dynamic custom-property
// height so older engines reject only the latter and keep a full-height shell.
const workspaceViewportRule = (css.match(/#workspace\s*\{[^}]*\}/g) || [])
  .find((block) => /--workspace-visible-height\s*:/.test(block));
pass(workspaceViewportRule && /height:\s*100vh\s*;\s*height:\s*var\(--workspace-visible-height,\s*100dvh\)/.test(workspaceViewportRule),
  "#workspace visible-height rule must declare height: 100vh immediately before height: var(--workspace-visible-height, 100dvh)");

// The renderer writes --workspace-visible-height from visualViewport. Responsive
// #workspace rules must inherit that base height rather than resetting it to
// 100dvh, which would cover the keyboard-shrunken viewport on phone or in short
// landscape.
const shortLandscape = extractMediaContent(css, "@media (max-width: 900px) and (max-height: 560px)");
for (const [name, responsiveCSS] of [["mobile", mobile], ["short-landscape", shortLandscape]]) {
  const responsiveWorkspaceRules = responsiveCSS.match(/#workspace\s*\{[^}]*\}/g) || [];
  pass(responsiveWorkspaceRules.length > 0, `${name} media query must retain a #workspace layout rule`);
  pass(responsiveWorkspaceRules.every((block) => !/\bheight\s*:/.test(block)),
    `${name} #workspace layout rule must not override the --workspace-visible-height runtime height`);
}

// A connection banner still leaves the workspace sized by the visual viewport.
// The static 100vh subtraction remains as a fallback for browsers that reject
// the custom-property calculation, but the later declaration must preserve the
// renderer's keyboard-aware --workspace-visible-height value.
const bannerWorkspaceRule = (css.match(/body\.has-connection-banner #sidebar,\s*body\.has-connection-banner #workspace\s*\{[^}]*\}/g) || [])[0];
pass(bannerWorkspaceRule && /height:\s*calc\(100vh\s*-\s*32px\)\s*;\s*height:\s*calc\(var\(--workspace-visible-height,\s*100dvh\)\s*-\s*32px\)/.test(bannerWorkspaceRule),
  "connection-banner workspace height must retain a 100vh fallback then subtract its offset from --workspace-visible-height");

// The transcript is the only vertically scrollable flex child. A zero flex basis
// and min-height:0 prevent the composer from being pushed below the visual viewport.
pass(convScrollRule && /flex:\s*1\s+1\s+0/.test(convScrollRule) && /min-height:\s*0/.test(convScrollRule),
  ".conversation scroll container must be the flexible min-height:0 transcript region");

// On phone the safe-area-aware composer is regular flex content, not sticky.
const phoneWorkspaceInputRule = (mobile.match(/\.workspace-input\s*\{[^}]*\}/g) || [])
  .find((block) => /safe-area-inset-bottom/.test(block));
pass(phoneWorkspaceInputRule && /flex:\s*0\s+0\s+auto/.test(phoneWorkspaceInputRule) && /margin-top:\s*auto/.test(phoneWorkspaceInputRule) && !/position:\s*sticky/.test(phoneWorkspaceInputRule),
  "phone safe-area .workspace-input must be non-sticky flex dock content");

// Compact telemetry is a single rail: responsive overrides may hide location
// parts, but must never restore the old wrapping status grid.
const phoneTelemetryRule = (mobile.match(/\.input-telemetry\s*\{[^}]*\}/g) || [])[0];
pass(phoneTelemetryRule && /flex-wrap:\s*nowrap/.test(phoneTelemetryRule),
  "phone telemetry rail must remain nonwrapping");
pass(/\.input-telemetry \.location \.cwd\s*\{[^}]*display:\s*none/.test(css),
  "narrow layouts must hide cwd through the semantic telemetry location selector");
const shortLandscapeTelemetryRule = (shortLandscape.match(/\.input-telemetry\s*\{[^}]*\}/g) || [])[0];
pass(shortLandscapeTelemetryRule && /flex-wrap:\s*nowrap/.test(shortLandscapeTelemetryRule),
  "short-landscape telemetry rail must remain nonwrapping");

pass(/\.workspace-input\[data-response-mode="ask"\]/.test(css),
  "stylesheet must provide a .workspace-input[data-response-mode=\"ask\"] response-mode hook");

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
// Negative guard: a mobile rule that overrides .tool-call to any size other than
// --text-sm (e.g. --text-base !important) inverts the reading hierarchy — tool
// machine text becomes larger than assistant prose. Catch that mutation here.
const toolCallSizeOverride = blocks.find(
  (b) => /\.tool-call\b/.test(b.split("{")[0]) && /font-size:\s*var\(--text-(?!sm\b)/.test(b)
);
pass(!toolCallSizeOverride, "no mobile rule must override .tool-call font-size away from --text-sm (inverts reading hierarchy)");

// Compact phone layout hides timing meta narrowly to avoid reserving ~112px and
// squeezing command/path text into a narrow, mid-word-wrapping column. This is a
// mobile overflow guard, not a desktop hover-only metadata contract.
const metaHidden = blocks.find((b) => /\.tool-call \.tool-meta/.test(b.split("{")[0]) && /display:\s*none/.test(b));
pass(!!metaHidden, "compact mobile may hide .tool-call .tool-meta so the command gets full width");

if (failures.length === 0) {
  console.log("PASS: mobile search palette CSS contract + layout guards");
  process.exit(0);
}
for (const failure of failures) console.log(failure);
process.exit(1);
