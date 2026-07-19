// #27 — CSS contract for the mobile ⋯ menu modal: a fixed full-viewport
// scrim on the modal layer, and inside @media (max-width: 767px) a
// full-width bottom sheet whose items meet the phone touch-target floor
// (var(--tap-min) = 44px there). The desktop popover rules must be
// untouched. Pure regex assertions over style.css.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };

function ruleBody(selector, hay) {
  const i = (hay || css).indexOf(selector);
  if (i < 0) return "";
  const h = hay || css;
  return h.slice(i, h.indexOf("}", i));
}

// Collect the contents of every @media (max-width: 767px) { ... } block
// (balanced braces, same approach as test-mobile-css.js).
function mobileBlocks() {
  const QUERY = "@media (max-width: 767px)";
  let out = "";
  let from = 0;
  for (;;) {
    const i = css.indexOf(QUERY, from);
    if (i < 0) break;
    const open = css.indexOf("{", i);
    let depth = 1, j = open + 1;
    while (j < css.length && depth > 0) {
      if (css[j] === "{") depth++;
      else if (css[j] === "}") depth--;
      j++;
    }
    out += css.slice(open + 1, j - 1) + "\n";
    from = j;
  }
  return out;
}
const mobile = mobileBlocks();

// ── Scrim (backdrop) ─────────────────────────────────────────────────────
const scrim = ruleBody(".sb-menu-scrim");
pass(scrim !== "", ".sb-menu-scrim rule must exist");
pass(/position:\s*fixed/.test(scrim), "scrim must be position:fixed");
pass(/inset:\s*0/.test(scrim), "scrim must cover the full viewport (inset:0)");
pass(/z-index:\s*var\(--z-modal\)/.test(scrim), "scrim must sit on the modal layer (var(--z-modal))");
pass(!/background:\s*#[0-9a-fA-F]/.test(scrim), "scrim must not introduce a raw hex color");

// ── Mobile sheet ─────────────────────────────────────────────────────────
const sheet = ruleBody(".sb-menu-modal {", mobile) || ruleBody(".sb-menu-modal{", mobile);
pass(sheet !== "", "mobile media query must style .sb-menu-modal");
pass(/width:\s*100%/.test(sheet), "mobile sheet must be full-width (width:100%)");
pass(/max-height/.test(sheet), "mobile sheet must cap its height (scroll, not cover the screen)");
const sheetItems = ruleBody(".sb-menu-modal .sb-menu-item", mobile);
pass(sheetItems !== "", "mobile media query must style .sb-menu-modal .sb-menu-item");
pass(/min-height:\s*var\(--tap-min\)/.test(sheetItems) || /min-height:\s*44px/.test(sheetItems),
  "mobile menu items must meet the 44px touch floor (var(--tap-min) or 44px)");

// ── Desktop popover untouched ────────────────────────────────────────────
const desktopMenu = ruleBody(".sb-menu {");
pass(/min-width:\s*160px/.test(desktopMenu), "desktop .sb-menu popover keeps min-width:160px");
const desktopItem = ruleBody(".sb-menu-item {");
pass(/min-height:\s*24px/.test(desktopItem), "desktop .sb-menu-item keeps its 24px density");

if (failures.length) {
  failures.forEach((f) => console.log(f));
  process.exit(1);
}
console.log("ok mobile ⋯ menu modal CSS (scrim + full-width sheet + 44px items; desktop untouched)");
process.exit(0);
