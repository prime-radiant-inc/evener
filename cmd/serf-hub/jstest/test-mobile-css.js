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

function blockWith(selector, property, cssText) {
  const blocks = cssText.match(/[^{}]*\{[^}]*\}/g) || [];
  return blocks.find((b) => {
    const sel = b.split("{")[0];
    return sel.split(",").map((s) => s.trim()).includes(selector) && property.test(b);
  });
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
// tool's purpose is flex-basis:100%, so it must never carry a left margin —
// a margin pushes it past the row's right edge, a horizontal overflow iOS
// scrolls to on keyboard focus, leaving the page shifted and un-anchored.
// (Since the 2026-07-11 one-column redesign it carries no indent at all.)
const purposeCmdRule = (css.match(/\.tool-call\.has-purpose \.tool-command\s*\{[^}]*\}/g) || [])[0];
pass(purposeCmdRule && !/margin-left:/.test(purposeCmdRule),
  ".tool-call.has-purpose .tool-command must not use margin-left (flex-basis:100% + margin overflows the row)");

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

// On phone the composer is regular flex content, not sticky. Round 4: the
// home indicator and corner curves only constrain the EDGES of the bottom
// band, so the action row is inset horizontally (side safe-area insets + a
// fixed gutter) and keeps only a small fixed bottom pad — it does NOT stack
// env(safe-area-inset-bottom) under itself (that stacked the row ~80px above
// the physical bottom with a dead band below it).
const phoneWorkspaceInputRule = (mobile.match(/\.workspace-input\s*\{[^}]*\}/g) || [])
  .find((block) => /flex:\s*0\s+0\s+auto/.test(block));
pass(phoneWorkspaceInputRule && /margin-top:\s*auto/.test(phoneWorkspaceInputRule) && !/position:\s*sticky/.test(phoneWorkspaceInputRule),
  "phone .workspace-input must be non-sticky flex dock content");
pass(phoneWorkspaceInputRule && !/safe-area-inset-bottom/.test(phoneWorkspaceInputRule),
  "phone .workspace-input must not reserve the safe-area gutter as dock padding");
for (const [name, responsiveCSS] of [["mobile", mobile], ["short-landscape", shortLandscape]]) {
  const controlsRules = responsiveCSS.match(/\.input-controls\s*\{[^}]*\}/g) || [];
  pass(controlsRules.every((block) => !/safe-area-inset-bottom/.test(block)),
    `${name} .input-controls must not stack env(safe-area-inset-bottom) under the action row`);
  const insetRule = controlsRules.find((block) =>
    /safe-area-inset-left/.test(block) && /safe-area-inset-right/.test(block));
  pass(!!insetRule,
    `${name} .input-controls must inset its sides with env(safe-area-inset-left/right) plus a fixed gutter`);
}

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
pass(/\.workspace-input\[data-response-mode="ask"\]\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*max-height:\s*min\(56dvh,\s*560px\)/s.test(css),
  "ask mode is a bounded flex column");
pass(/\.ask-response-dock\s*\{[^}]*flex:\s*1\s+1\s+auto[^}]*min-height:\s*0[^}]*overflow-y:\s*auto/s.test(css),
  "only the question dock is the bounded scroll region");
pass(/\.workspace-input\s*>\s*\[data-composer-surface\]\[hidden\]\s*\{[^}]*display:\s*none/s.test(css),
  "the hidden composer surface cannot occupy ask-mode layout space");
pass(/@media\s*\([^)]*max-width:[^)]*\)[\s\S]*?\.ask-footer\s*\{[^}]*safe-area-inset-left[^}]*safe-area-inset-right/s.test(css),
  "the mobile ask footer clears horizontal safe areas");
const mobileAskFooterRule = (mobile.match(/\.ask-footer\s*\{[^}]*\}/g) || [])[0];
pass(mobileAskFooterRule && /padding-(?:block-end|bottom):\s*calc\(var\(--space-\d+\)\s*\+\s*env\(safe-area-inset-bottom\)\)/.test(mobileAskFooterRule),
  "the bottommost mobile ask footer must combine fixed spacing with env(safe-area-inset-bottom)");

const alternativeRowRule = (mobile.match(/\.ask-alternative-row\s*\{[^}]*\}/g) || [])[0];
pass(alternativeRowRule && /display:\s*flex/.test(alternativeRowRule) &&
    /flex-wrap:\s*wrap/.test(alternativeRowRule) && /min-width:\s*0/.test(alternativeRowRule),
  "mobile alternative rows wrap and cannot impose an intrinsic minimum width");
const alternativeOptionRule = (mobile.match(/\.ask-alternative-row\s+\.ask-option\s*\{[^}]*\}/g) || [])[0];
pass(alternativeOptionRule && /max-width:\s*100%/.test(alternativeOptionRule),
  "mobile alternative option labels stay within the narrow response dock");
const alternativeEditorRule = (mobile.match(/\.ask-alternative-row\s+\.(?:ask-free-input|ask-decide-leaning)[^\{]*\{[^}]*\}/g) || [])[0];
pass(alternativeEditorRule && /min-width:\s*0/.test(alternativeEditorRule) &&
    /flex:\s*1\s+1\s+\d+px/.test(alternativeEditorRule) && /max-width:\s*100%/.test(alternativeEditorRule),
  "mobile alternative editors shrink and wrap without overflowing a 320px viewport");

// iOS zoom guard: a focused field whose text is < 16px makes iOS Safari zoom
// the page in (and never zoom back out on blur). Editable fields must be 16px
// on phone — checked via the rule whose selector includes .message-input.
const inputZoomRule = (mobile.match(/[^{}]*\{[^}]*\}/g) || [])
  .find((block) => /\.message-input/.test(block.split("{")[0]) && /font-size:\s*16px/.test(block));
pass(!!inputZoomRule,
  "mobile must pin editable fields (incl .message-input) to font-size: 16px so iOS does not auto-zoom on focus");

pass(mobile.indexOf(".spawn-mobile-rows") !== -1, "mobile stylesheet must include .spawn-mobile-rows");
pass(/\.spawn-mobile-rows\s*\{[^}]*display:\s*flex/s.test(mobile), "mobile must show spawn-mobile-rows as flex");
pass(/\.spawn-chips\s*\{[^}]*display:\s*none/s.test(mobile), "mobile must hide desktop .spawn-chips");
pass(/\.spawn-row\s*\{[^}]*min-height:\s*48px/s.test(mobile), "mobile spawn rows must be 48px tall");
pass(/\.spawn-row\s*\{[^}]*font-family:\s*var\(--font-sans\)/s.test(mobile), "mobile spawn rows must use sans font");
pass(/\.spawn-input\s*\{[^}]*min-height:\s*96px/s.test(mobile), "mobile spawn textarea must have a 96px min-height");
pass(/\.spawn-input\s*\{[^}]*resize:\s*none/s.test(mobile), "mobile spawn textarea must hide the resize handle");
pass(!/\.spawn-advanced summary\s*\{[^}]*text-transform:\s*uppercase/s.test(mobile), "mobile advanced summary must not be uppercase");
pass(!/\.spawn-advanced summary\s*\{[^}]*font-family:\s*var\(--font-mono\)/s.test(mobile), "mobile advanced summary must not use mono font");

pass(!!blockWith(".spawn-recent-row", /min-height:\s*44px/, css), "mobile recent-prompt rows must be at least 44px tall");
pass(!!blockWith(".spawn-input", /max-height:\s*min\(40vh,\s*8lh\)/, mobile), "mobile spawn textarea must cap height at 40vh or 8 lines");
pass(!!blockWith(".spawn-btn kbd", /display:\s*none/, mobile), "mobile spawn buttons must hide keyboard-hint labels");
pass(!!blockWith(".chip-picker-sheet .chip-picker-option", /font-family:\s*var\(--font-sans\)/, mobile), "mobile picker sheet options must use sans font");
pass(/\.spawn-pane\s*\{[^}]*calc\(68px\s*\+\s*env\(safe-area-inset-bottom\)\)/s.test(mobile), "mobile scrollable pane must reserve space for the fixed action band");
pass(!!blockWith(".spawn-attach-btn .spawn-attach-label", /display:\s*none/, mobile), "mobile attach button must hide the verbose label");
pass(!!blockWith(".spawn-recent-row", /font-size:\s*var\(--text-base\)/, mobile), "mobile recent-prompt rows must use base font size");

// One-size reading column (2026-07-11 round 2): compact phone density shrinks
// only the app chrome; the transcript keeps the shared --text-base flowing
// size and must NOT re-tier the column with per-kind size overrides.
const blocks = mobile.match(/[^{}]*\{[^}]*\}/g) || [];
const convRule = blocks.find((b) => /\.conversation/.test(b.split("{")[0]) && /font-size:\s*var\(--text-base\)/.test(b));
pass(!!convRule, "compact phone density must keep the transcript column at --text-base against the smaller chrome font");
const perKindOverride = blocks.find(
  (b) => /\.(assistant-message|tool-call\b|user-message)/.test(b.split("{")[0]) && /font-size:/.test(b) && !/16px/.test(b)
);
pass(!perKindOverride, "no compact phone rule may re-tier the reading column with per-kind font-size overrides");

// The timing meta is fixed top-right (position: absolute) on every breakpoint
// (issue #37), so it never reserves width and the command/path text always gets
// the full row — the old compact-phone display:none carve-out (which reflowed
// text on tap) is gone. This is the no-reflow metadata contract, not a mobile
// overflow guard.
const metaFixed = /\.tool-call \.tool-meta\s*\{[^}]*position:\s*absolute/.test(css);
pass(metaFixed, ".tool-call .tool-meta is position:absolute so the command gets full width on every breakpoint");
const metaHidden = blocks.find((b) => /\.tool-call \.tool-meta/.test(b.split("{")[0]) && /display:\s*none/.test(b));
pass(!metaHidden, "no breakpoint hides .tool-call .tool-meta with display:none (display toggling reflows text)");

// iOS standalone runs edge-to-edge (black-translucent status bar), so every
// full-height fixed overlay must pad its screen-edge sides with the safe-area
// insets or its content renders under the clock / home indicator.
const detailsBaseRule = (css.match(/\.details-panel\s*\{[^}]*\}/g) || [])
  .find((block) => /position:\s*fixed/.test(block));
pass(detailsBaseRule && /safe-area-inset-top/.test(detailsBaseRule) && /safe-area-inset-bottom/.test(detailsBaseRule),
  ".details-panel (fixed, top:0/bottom:0) must pad with env(safe-area-inset-top) and -bottom");
const detailsMobileRules = (mobile.match(/\.details-panel\s*\{[^}]*\}/g) || []);
pass(detailsMobileRules.every((block) => !/padding\s*:/.test(block) || (/safe-area-inset-top/.test(block) && /safe-area-inset-bottom/.test(block))),
  "mobile .details-panel padding overrides must retain the top and bottom safe-area insets");

// The full-screen search palette's sticky header sits at the very top edge.
const searchHeaderMobileRule = (mobile.match(/\.search-dialog-header\s*\{[^}]*\}/g) || [])[0];
pass(searchHeaderMobileRule && /safe-area-inset-top/.test(searchHeaderMobileRule),
  "mobile .search-dialog-header must pad with env(safe-area-inset-top)");

// The composer children (status rail, input card) live inside the
// [data-composer-surface] wrapper. It must be a flex column, or the mobile
// `.input-status { order: -1 }` rule silently stops moving the status rail
// above the input.
pass(/\.workspace-input\s*>\s*\[data-composer-surface\]\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column/s.test(css),
  ".workspace-input > [data-composer-surface] must be a flex column so .input-status order:-1 applies");

if (failures.length === 0) {
  console.log("PASS: mobile search palette CSS contract + layout guards");
  process.exit(0);
}
for (const failure of failures) console.log(failure);
process.exit(1);
