#!/usr/bin/env node
// ds-audit: measure how far the app's CSS drifts from the design tokens in
// src/styles/tokens.css. Colors are already enforced by
// src/styles/token-contract.test.ts; this covers what that test does not:
// type sizes, spacing, radii, weights, motion durations, and token usage.
//
// Usage: node scripts/ds-audit.mjs [--json] [--files <property>]
//   --json             print the full report as JSON instead of a summary
//   --files <property> list every file:line with a literal for one property
//                      group (font-size|spacing|radius|weight|duration|line-height)
// Run before and after a cleanup pass to see the drift move.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const CONTROLS = ["button", "input", "select", "textarea"];
// Class-name fragments that name a concept src/widgets already owns.
const WIDGET_CONCEPTS =
  /^(?:[a-zA-Z]*?)(chip|badge|pill|btn|button|card|empty|menu|tooltip|switch|toggle|skeleton|kbd|meter)/i;

// One pass, so whichever token starts first wins: a "//" comment that
// mentions a glob like widgets/** must not open a block comment, and a string
// literal ("https://...", "image/*") keeps its comment-like characters.
const stripComments = (text) =>
  text.replace(
    /("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|`(?:\\.|[^`\\])*`)|\/\*[\s\S]*?\*\/|\/\/[^\n]*/g,
    (_match, literal) => literal ?? "",
  );

// Native controls written directly in TSX instead of through a widget.
export function rawControlsIn(tsx) {
  const text = stripComments(tsx);
  const counts = {};
  for (const tag of CONTROLS) counts[tag] = [...text.matchAll(new RegExp(`<${tag}(?=[\\s/>])`, "g"))].length;
  return counts;
}

// CSS-module class names, at the head of a selector, that re-implement a
// widget concept (chip, card, empty state, ...). Descendant parts of a
// compound selector are not counted separately.
export function widgetConceptClassesIn(css) {
  const found = [];
  for (const m of stripComments(css).matchAll(/(?:^|[},])\s*\.([a-zA-Z][a-zA-Z0-9_-]*)/g)) {
    const name = m[1];
    if (WIDGET_CONCEPTS.test(name) && !found.includes(name)) found.push(name);
  }
  return found;
}

// fileURLToPath, not URL.pathname: pathname keeps percent-encoding, so a
// checkout path with spaces would name a directory that does not exist.
export function frontendRootFrom(moduleUrl) {
  return fileURLToPath(new URL("..", moduleUrl));
}

const ROOT = frontendRootFrom(import.meta.url);
const SRC = join(ROOT, "src");
const TOKENS = join(SRC, "styles/tokens.css");

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(css|tsx?)$/.test(name) && !/\.test\.tsx?$/.test(name)) out.push(p);
  }
  return out;
}

function main() {
  const args = process.argv.slice(2);
  const asJson = args.includes("--json");
  const filesFor = args.includes("--files") ? args[args.indexOf("--files") + 1] : null;

  const all = walk(SRC);
  const isDev = (p) => p.includes("/src/dev/");
  const css = all.filter((p) => p.endsWith(".css") && p !== TOKENS && !isDev(p));
  const code = all.filter((p) => !isDev(p) && p !== TOKENS);

  // Literal patterns per property group. A value counts as drift when it is a
  // non-zero length/number written inline instead of through var(--*).
  const GROUPS = {
    "font-size": /font-size:\s*([0-9.]+(?:px|rem|em))/g,
    spacing: /(?:padding|margin|gap|row-gap|column-gap)(?:-[a-z]+)*:\s*([^;}]*?\b[1-9][0-9]*px[^;}]*)/g,
    radius: /border-radius:\s*([0-9][^;}]*)/g,
    weight: /font-weight:\s*([0-9]+)/g,
    duration: /(?:transition|animation)[a-z-]*:\s*[^;}]*?\b([0-9]+m?s)\b/g,
    "line-height": /line-height:\s*([0-9.]+(?:px)?)\s*[;}]/g,
  };

  const report = { files: css.length, groups: {}, tokenUse: {}, undefinedTokens: {} };
  const hits = {};
  for (const g of Object.keys(GROUPS)) {
    report.groups[g] = { count: 0, values: {} };
    hits[g] = [];
  }

  for (const file of css) {
    const text = readFileSync(file, "utf8");
    const lines = text.split("\n");
    for (const [g, re] of Object.entries(GROUPS)) {
      lines.forEach((line, i) => {
        for (const m of line.matchAll(re)) {
          const v = m[1].trim();
          const r = report.groups[g];
          r.count++;
          r.values[v] = (r.values[v] || 0) + 1;
          hits[g].push(`${relative(ROOT, file)}:${i + 1}  ${line.trim()}`);
        }
      });
    }
  }

  // Token inventory: declared vs referenced (CSS + TS/TSX inline styles).
  const tokenText = readFileSync(TOKENS, "utf8");
  const declared = new Set([...tokenText.matchAll(/(--[a-z0-9-]+)\s*:/g)].map((m) => m[1]));
  const declaredElsewhere = new Set();
  const used = {};
  for (const file of code) {
    const text = readFileSync(file, "utf8");
    for (const m of text.matchAll(/(--[a-z][a-z0-9-]*)\s*:/g)) declaredElsewhere.add(m[1]);
    for (const m of text.matchAll(/var\(\s*(--[a-z0-9-]+)/g)) used[m[1]] = (used[m[1]] || 0) + 1;
  }
  for (const m of tokenText.matchAll(/var\(\s*(--[a-z0-9-]+)/g)) used[m[1]] = (used[m[1]] || 0) + 1;
  for (const t of declared) report.tokenUse[t] = used[t] || 0;
  for (const [t, n] of Object.entries(used)) {
    if (!declared.has(t) && !declaredElsewhere.has(t) && !t.startsWith("--dv-")) report.undefinedTokens[t] = n;
  }

  // Widget bypasses: everything outside src/widgets that hand-builds what a
  // widget provides. Driving these to zero is the widget-adoption goal.
  const isWidget = (p) => p.includes("/src/widgets/");
  report.bypass = { controls: {}, conceptClasses: 0 };
  hits.bypass = [];
  for (const tag of CONTROLS) report.bypass.controls[tag] = 0;
  for (const file of code.filter((p) => p.endsWith(".tsx") && !isWidget(p))) {
    const counts = rawControlsIn(readFileSync(file, "utf8"));
    const n = Object.values(counts).reduce((a, b) => a + b, 0);
    for (const tag of CONTROLS) report.bypass.controls[tag] += counts[tag];
    if (n) hits.bypass.push(`${relative(ROOT, file)}  ${n} raw control(s)`);
  }
  for (const file of css.filter((p) => !isWidget(p))) {
    const names = widgetConceptClassesIn(readFileSync(file, "utf8"));
    report.bypass.conceptClasses += names.length;
    if (names.length) hits.bypass.push(`${relative(ROOT, file)}  .${names.join(" .")}`);
  }

  if (filesFor) {
    for (const h of hits[filesFor] || []) console.log(h);
    return;
  }
  if (asJson) {
    console.log(JSON.stringify(report, null, 2));
    return;
  }

  const top = (obj, n) =>
    Object.entries(obj)
      .sort((a, b) => b[1] - a[1])
      .slice(0, n)
      .map(([k, v]) => `${v}× ${k}`)
      .join(", ");
  console.log(`Scanned ${report.files} component CSS files (src/dev excluded).\n`);
  console.log("Literal values that bypass tokens:");
  for (const [g, r] of Object.entries(report.groups)) {
    console.log(`  ${g.padEnd(12)} ${String(r.count).padStart(4)}  top: ${top(r.values, 6)}`);
  }
  const unused = Object.entries(report.tokenUse)
    .filter(([, n]) => n === 0)
    .map(([t]) => t);
  console.log(`\nDeclared tokens: ${declared.size}; never referenced: ${unused.length}`);
  if (unused.length) console.log(`  ${unused.join(", ")}`);
  const undef = Object.keys(report.undefinedTokens);
  console.log(`\nvar() references to tokens declared nowhere: ${undef.length}`);
  if (undef.length) console.log(`  ${top(report.undefinedTokens, 20)}`);
  const b = report.bypass;
  console.log(`\nWidget bypasses (outside src/widgets and src/dev):`);
  console.log(`  raw controls   ${top(b.controls, 4)}`);
  console.log(`  module classes re-implementing a widget concept: ${b.conceptClasses}`);
  console.log("\nDetail: --files <group|bypass> lists sites; --json dumps everything.");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
