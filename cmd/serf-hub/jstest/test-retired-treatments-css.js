// Retired treatments (design-system §3 "Retired from the old UI"): no
// ALL-CAPS label treatment; radius is the documented two tokens.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(!/text-transform:\s*uppercase/.test(css), "no ALL-CAPS label treatment anywhere");

// Radius literals: only 0 (square), the two tokens, and the one documented
// squircle (30%, its own shape) may remain.
// Note: the match captures the declaration up to (not including) the `;`, so
// a plain `0` literal has no trailing `;`/whitespace in the matched string —
// the zero allow-check anchors on end-of-string, not on a terminator char.
const literals = css.match(/border-radius:\s*[^v][^;]*/g) || [];
const bad = literals.filter((l) => !/border-radius:\s*var\(--radius-(md|pill)\)/.test(l)
  && !/^border-radius:\s*0(\s+0)*\s*$/.test(l)
  && !/^border-radius:\s*30%\s*$/.test(l));
assert(bad.length === 0, "literal border-radius values snapped to tokens: " + JSON.stringify(bad));
console.log("ok retired treatments");
