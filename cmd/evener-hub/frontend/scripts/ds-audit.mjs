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

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = new URL("..", import.meta.url).pathname;
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

if (filesFor) {
	for (const h of hits[filesFor] || []) console.log(h);
	process.exit(0);
}
if (asJson) {
	console.log(JSON.stringify(report, null, 2));
	process.exit(0);
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
const unused = Object.entries(report.tokenUse).filter(([, n]) => n === 0).map(([t]) => t);
console.log(`\nDeclared tokens: ${declared.size}; never referenced: ${unused.length}`);
if (unused.length) console.log(`  ${unused.join(", ")}`);
const undef = Object.keys(report.undefinedTokens);
console.log(`\nvar() references to tokens declared nowhere: ${undef.length}`);
if (undef.length) console.log(`  ${top(report.undefinedTokens, 20)}`);
console.log("\nDetail: --files <group> lists file:line; --json dumps everything.");
