// CSS grammar contract for context pressure & compaction (mockup #17 Alt A).
//   • the context gauge stays NEUTRAL until ~80%, then turns BLUE
//     (--state-awaiting) with a ⚠ glyph — never red;
//   • compaction renders as a quiet NEUTRAL lifecycle line (settled = neutral),
//     never colored/alarming.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── Gauge threshold: blue near the edge, paired with a glyph ───────────────
const fillWarn = css.match(/\.input-status \.context-fill\.context-warn\s*\{[^}]*\}/);
pass(!!fillWarn, "context-fill.context-warn rule exists (gauge turns blue near limit)");
if (fillWarn) {
  pass(/var\(--state-awaiting\)/.test(fillWarn[0]), "near-limit fill uses blue --state-awaiting");
  pass(!/var\(--error\)/.test(fillWarn[0]), "near-limit fill must NOT be red --error");
}
const glyph = css.match(/\.input-status \.context-warn-glyph\s*\{[^}]*\}/);
pass(!!glyph, "context-warn-glyph rule exists (colorblind-safe ⚠ pairing)");
if (glyph) pass(/var\(--state-awaiting\)/.test(glyph[0]), "warn glyph is blue --state-awaiting");

// The neutral default fill must NOT be blue (it is the settled state).
const fillDefault = css.match(/\.input-status \.context-fill\s*\{[^}]*\}/);
pass(!!fillDefault, "default context-fill rule exists");
if (fillDefault) pass(!/var\(--state-awaiting\)/.test(fillDefault[0]), "default fill is neutral, not blue");

// ── Compaction line: quiet neutral, never colored ──────────────────────────
const compLabel = css.match(/\.context-compaction-label\s*\{[^}]*\}/);
pass(!!compLabel, "context-compaction-label rule exists");
const compStat = css.match(/\.context-compaction-stat\s*\{[^}]*\}/);
pass(!!compStat, "context-compaction-stat (expanded math) rule exists");
// Check neutral-color constraints on each compaction rule individually (not as a
// span) so that an unrelated blue/error rule added between them doesn't cause a
// spurious failure here.
const compLine = css.match(/\.context-compaction-line\s*\{[^}]*\}/);
if (compLine) {
  pass(!/var\(--error\)/.test(compLine[0]), "context-compaction-line must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compLine[0]), "context-compaction-line is neutral, not blue");
}
if (compLabel) {
  pass(!/var\(--error\)/.test(compLabel[0]), "context-compaction-label must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compLabel[0]), "context-compaction-label is neutral, not blue (it is a settled DONE event)");
}
if (compStat) {
  pass(!/var\(--error\)/.test(compStat[0]), "context-compaction-stat must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compStat[0]), "context-compaction-stat is neutral, not blue (it is a settled DONE event)");
}

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: context-pressure gauge + compaction CSS grammar");
