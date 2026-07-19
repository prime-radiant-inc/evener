// CSS grammar contract for compact context pressure (mockup #17 Alt A).
//   • the compact context text stays NEUTRAL until ~80%, then turns AMBER
//     (--state-awaiting) with a ⚠ glyph — never red;
//   • compaction renders as a quiet NEUTRAL lifecycle line (settled = neutral),
//     never colored/alarming.
const fs = require("fs");
const path = require("path");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// ── Compact threshold: amber near the edge, paired with a glyph ─────────────
const warnNumbers = css.match(/\.input-telemetry \.context\.context-warn \.context-numbers\s*\{[^}]*\}/);
pass(!!warnNumbers, "compact context-warn numbers rule exists (context turns amber near limit)");
if (warnNumbers) {
	pass(/var\(--state-awaiting\)/.test(warnNumbers[0]), "near-limit context uses amber --state-awaiting");
	pass(!/var\(--error\)/.test(warnNumbers[0]), "near-limit context must NOT be red --error");
}
const glyph = css.match(/\.input-telemetry \.context-warn-glyph\s*\{[^}]*\}/);
pass(!!glyph, "context-warn-glyph rule exists (colorblind-safe ⚠ pairing)");
if (glyph) {
  pass(/var\(--state-awaiting\)/.test(glyph[0]), "warn glyph is amber --state-awaiting");
  pass(!/var\(--error\)/.test(glyph[0]), "warn glyph must NOT be red --error");
}

// Default compact context numbers must state their neutral color directly;
// only the warning selector above applies the awaiting color.
const defaultNumbers = css.match(/\.input-telemetry \.context \.context-numbers\s*\{[^}]*\}/);
pass(!!defaultNumbers, "default compact context value rule exists");
if (defaultNumbers) {
  pass(/color\s*:\s*var\(--ink\)/.test(defaultNumbers[0]), "default compact context uses neutral --ink");
  pass(!/var\(--state-awaiting\)/.test(defaultNumbers[0]), "default compact context must NOT be amber --state-awaiting");
  pass(!/var\(--error\)/.test(defaultNumbers[0]), "default compact context must NOT be red --error");
}

// ── Compaction line: quiet neutral, never colored ──────────────────────────
const compLabel = css.match(/\.context-compaction-label\s*\{[^}]*\}/);
pass(!!compLabel, "context-compaction-label rule exists");
const compStat = css.match(/\.context-compaction-stat\s*\{[^}]*\}/);
pass(!!compStat, "context-compaction-stat (expanded math) rule exists");
// Check neutral-color constraints on each compaction rule individually (not as a
// span) so that an unrelated amber/error rule added between them doesn't cause a
// spurious failure here.
const compLine = css.match(/\.context-compaction-line\s*\{[^}]*\}/);
if (compLine) {
  pass(!/var\(--error\)/.test(compLine[0]), "context-compaction-line must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compLine[0]), "context-compaction-line is neutral, not amber");
}
if (compLabel) {
  pass(!/var\(--error\)/.test(compLabel[0]), "context-compaction-label must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compLabel[0]), "context-compaction-label is neutral, not amber (it is a settled DONE event)");
}
if (compStat) {
  pass(!/var\(--error\)/.test(compStat[0]), "context-compaction-stat must NOT use red --error");
  pass(!/var\(--state-awaiting\)/.test(compStat[0]), "context-compaction-stat is neutral, not amber (it is a settled DONE event)");
}

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: compact context-pressure + compaction CSS grammar");
