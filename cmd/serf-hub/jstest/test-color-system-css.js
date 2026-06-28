// Encodes the redesign grammar from docs/web-ui/mockups (01-color-system,
// 02-chrome-labels, 03-user-message-steering, 04-assistant-hero) as a CSS
// contract over style.css: the four-meaning color system, sentence-case
// chrome labels, the demoted user message + neutral steering, and the hero
// assistant prose with quiet-underline inline code. Pure regex assertions.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// Slice the :root[data-theme="dark"] token block so token-value assertions
// are unambiguous (the file ships dark + light + media-query copies).
// Use a balanced-brace counter so values containing '}' don't truncate the block.
const darkStart = css.indexOf(':root[data-theme="dark"]');
let darkRoot = "";
if (darkStart >= 0) {
  const openBrace = css.indexOf("{", darkStart);
  if (openBrace >= 0) {
    let depth = 1, i = openBrace + 1;
    while (i < css.length && depth > 0) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}") depth--;
      i++;
    }
    darkRoot = css.slice(darkStart, i);
  }
}
const tokenVal = (name) => {
  const m = darkRoot.match(new RegExp("--" + name + ":\\s*([^;]+);"));
  return m ? m[1].trim() : null;
};

// ── Mockup #1 — four-meaning color system ────────────────────────────────
// amber = needs-you / awaiting input (NOT red).
pass(tokenVal("state-awaiting") === "#e2b06a", "--state-awaiting is amber #e2b06a (needs-you), got " + tokenVal("state-awaiting"));
// red = error, its own token (not an alias of awaiting).
pass(tokenVal("error") === "#f7768e", "--error is its own red #f7768e, got " + tokenVal("error"));
pass(!/--error:\s*var\(--state-awaiting\)/.test(darkRoot), "--error must not alias --state-awaiting");
// done/idle/user are neutral (no color). user no longer maps to green idle.
pass(!/--user:\s*var\(--state-idle\)/.test(darkRoot), "--user must not map to green --state-idle");
pass(tokenVal("state-idle") !== "#9ece6a", "--state-idle must not be green #9ece6a (idle/user are neutral)");
pass(tokenVal("state-idle") === "#7a7a86", "--state-idle should be neutral muted #7a7a86, got " + tokenVal("state-idle"));
// purple subagent retired everywhere.
pass(!/--state-subagent:\s*#bb9af7/.test(css), "--state-subagent purple #bb9af7 must be retired");
// success token exists for genuine good-news only.
pass(tokenVal("success") != null, "--success token defined for genuine good-news highlights");

// --text-dim must not be used as body text. It is a hairline/non-text token.
// Assert known body-text call sites moved off --text-dim (Pass 7 contrast pass).
pass(!/\.project-name\s*\{[^}]*color:\s*var\(--text-dim\)/s.test(css), "project-name body text must not use --text-dim");
pass(!/\.session-tier-label\s*\{[^}]*color:\s*var\(--text-dim\)/s.test(css), "session-tier-label text must not use --text-dim");
pass(!/\.test-date-header\s*\{[^}]*color:\s*var\(--text-dim\)/s.test(css), "test-date-header text must not use --text-dim");
pass(!/\.think\.think-tier-short\s*\{[^}]*color:\s*var\(--text-dim\)/s.test(css), "short-think tier text must not use --text-dim (readable words need AA)");
pass(!/\.plan-item\.cancelled \.plan-step\s*\{[^}]*color:\s*var\(--text-dim\)/s.test(css), "cancelled plan-step text must not use --text-dim (line-through carries the meaning)");

// Radius scale consolidated to two (design-system §3): the retired
// sm/lg/xl/full names must be gone; only --radius-md + --radius-pill remain.
// Match real token forms only (var(...) reference or `name:` definition), not prose.
pass(!/(var\(\s*--radius-(sm|lg|xl|full)\b|--radius-(sm|lg|xl|full)\s*:)/.test(css), "retired radius tokens (sm/lg/xl/full) must be gone");
pass(/--radius-md:\s*5px/.test(css), "--radius-md is the golden 5px rectangle radius");
pass(/--radius-pill:\s*999px/.test(css), "--radius-pill is a true 999px pill");

// The retired paper-grain --noise texture must not ship (design-system §3).
pass(!/--noise\b/.test(css), "retired paper-grain --noise token must be gone");
pass(!/feTurbulence|fractalNoise/.test(css), "retired paper-grain noise SVG must be gone");

// Error call sites must use --error (red), not amber --state-awaiting.
pass(/\.tool-call \.tool-status-bad\s*\{[^}]*var\(--error\)/s.test(css), "tool-status-bad must use red --error");
pass(/\.tool-call \.result-bad\s*\{[^}]*var\(--error\)/s.test(css), "result-bad must use red --error");

// ── Mockup #2 — chrome labels: sentence-case sans, mono for machine only ──
// Find all rule bodies where the given class is a DIRECT target (appears
// immediately before ',' or '{' in the selector, so descendant selectors
// like ".sidebar-section-header .count { font-family: mono }" are excluded).
// Works whether the two selectors appear combined or in separate rules.
function directRuleBodies(css, cls) {
  const esc = cls.replace(/\./g, '\\.').replace(/[[\]]/g, '\\$&');
  const re = new RegExp(esc + '\\s*[,{]', 'g');
  const bodies = [];
  let m;
  while ((m = re.exec(css)) !== null) {
    const openBrace = m[0].endsWith('{')
      ? m.index + m[0].length - 1
      : css.indexOf('{', m.index + m[0].length);
    if (openBrace < 0) continue;
    const closeBrace = css.indexOf('}', openBrace + 1);
    if (closeBrace < 0) continue;
    bodies.push(css.slice(openBrace + 1, closeBrace));
  }
  return bodies;
}
const sshBodies = directRuleBodies(css, '.sidebar-section-header');
const phBodies  = directRuleBodies(css, '.project-header');
pass(sshBodies.length > 0 || phBodies.length > 0, "section/project header rule exists");
const headerBody = [...sshBodies, ...phBodies].join('\n');
pass(!/text-transform:\s*uppercase/.test(headerBody), "section/project headers must not be uppercase");
pass(!/font-family:\s*var\(--font-mono\)/.test(headerBody), "section/project headers must be sans, not mono");
const statusBadge = css.match(/\.status-badge\s*\{[^}]*\}/s);
pass(statusBadge != null, "status-badge rule exists");
if (statusBadge) {
  pass(!/text-transform:\s*uppercase/.test(statusBadge[0]), "status-badge must be sentence-case, not uppercase");
  pass(!/font-family:\s*var\(--font-mono\)/.test(statusBadge[0]), "status-badge must be sans (human label), not mono");
}

// ── Mockup #3 — demoted user message + neutral steering ──────────────────
pass(!/\.user-message\s*\{[^}]*justify-content:\s*flex-end/s.test(css), "user-message must not be right-aligned");
const pillRule = css.match(/\.user-message \.pill\s*\{[^}]*\}/s);
pass(pillRule != null, "user-message .pill rule exists");
if (pillRule) {
  pass(!/border-radius:\s*var\(--radius-pill\)/.test(pillRule[0]), "user-message pill must not be a rounded bubble");
  pass(!/background:\s*var\(--bg-raised\)/.test(pillRule[0]), "user-message pill must not have a raised-bubble background");
}
// A "You" tag exists for the demoted user message.
pass(/\.user-message-tag/.test(css), "user-message-tag (the quiet 'You' label) is styled");
// Steering tick must be neutral, not amber (--state-warning).
pass(!/\.steering \.steering-verb\s*\{[^}]*color:\s*var\(--state-warning\)/s.test(css), "steering-verb must not use amber --state-warning (steering is neutral)");

// ── Mockup #4 — assistant prose hero; inline code quiet underline ────────
const assistantRule = css.match(/\.assistant-message\s*\{[^}]*\}/s);
pass(assistantRule != null, "assistant-message rule exists");
if (assistantRule) {
  // Hero wins via size: the reading layer is the largest prose in the
  // transcript — at least --text-md, and lifted to --text-lg in the overhaul.
  pass(/font-size:\s*var\(--text-(md|lg)\)/.test(assistantRule[0]), "assistant prose is hero size (--text-md or larger)");
}
const codeRule = css.match(/\.assistant-message code\s*\{[^}]*\}/s);
pass(codeRule != null, "assistant-message code rule exists");
if (codeRule) {
  pass(/border-bottom:/.test(codeRule[0]), "inline code uses a quiet underline (border-bottom)");
  pass(!/background:\s*var\(--bg-raised\)/.test(codeRule[0]), "inline code must not be a filled chip background");
}

if (failures.length === 0) {
  console.log("PASS: color/chrome/user-message/hero CSS contract");
  process.exit(0);
}
for (const failure of failures) console.log(failure);
process.exit(1);
