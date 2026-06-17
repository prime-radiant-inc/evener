// CSS grammar contract for context pressure & compaction (mockup #17 Alt A).
//   • the context gauge stays NEUTRAL until ~80%, then turns AMBER
//     (--state-awaiting) with a ⚠ glyph — never red;
//   • compaction renders as a quiet NEUTRAL lifecycle line (settled = neutral),
//     never colored/alarming.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── Gauge threshold: amber near the edge, paired with a glyph ───────────────
const fillWarn = css.match(/\.input-status \.context-fill\.context-warn\s*\{[^}]*\}/);
pass(!!fillWarn, "context-fill.context-warn rule exists (gauge turns amber near limit)");
if (fillWarn) {
  pass(/var\(--state-awaiting\)/.test(fillWarn[0]), "near-limit fill uses amber --state-awaiting");
  pass(!/var\(--error\)/.test(fillWarn[0]), "near-limit fill must NOT be red --error");
}
const glyph = css.match(/\.input-status \.context-warn-glyph\s*\{[^}]*\}/);
pass(!!glyph, "context-warn-glyph rule exists (colorblind-safe ⚠ pairing)");
if (glyph) pass(/var\(--state-awaiting\)/.test(glyph[0]), "warn glyph is amber --state-awaiting");

// The neutral default fill must NOT be amber (it is the settled state).
const fillDefault = css.match(/\.input-status \.context-fill\s*\{[^}]*\}/);
pass(!!fillDefault, "default context-fill rule exists");
if (fillDefault) pass(!/var\(--state-awaiting\)/.test(fillDefault[0]), "default fill is neutral, not amber");

// ── Compaction line: quiet neutral, never colored ──────────────────────────
const compLabel = css.match(/\.context-compaction-label\s*\{[^}]*\}/);
pass(!!compLabel, "context-compaction-label rule exists");
const compStat = css.match(/\.context-compaction-stat\s*\{[^}]*\}/);
pass(!!compStat, "context-compaction-stat (expanded math) rule exists");
const compBlock = css.match(/\.context-compaction-line[\s\S]*?\.context-compaction-stat\s*\{[^}]*\}/);
pass(!!compBlock, "compaction CSS block found");
if (compBlock) {
  pass(!/var\(--error\)/.test(compBlock[0]), "compaction styling must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compBlock[0]), "compaction styling is neutral, not amber (it is a settled DONE event)");
}

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: context-pressure gauge + compaction CSS grammar");
