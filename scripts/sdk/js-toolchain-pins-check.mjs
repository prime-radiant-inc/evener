#!/usr/bin/env node
// js-toolchain-pins-check.mjs fails when the JavaScript toolchain packages the
// npm trees share are not pinned to one exact version.
//
// The frontend, mobile-native and appwire-client/typescript trees compile,
// format and test the same shared client sources, so a caret range or a
// different minor in one tree means the same file is checked by two toolchains.
// For each package in SHARED_TOOLCHAIN, every tree that declares it must use an
// exact version (no ^, ~ or other range) and all trees must agree. React and
// react-dom are exact in every tree but may differ between trees: the web app
// and the native app each run their own renderer (react-native ships one built
// for a single React version), and the shared client is framework-free. A biome.jsonc
// $schema URL must name the same Biome version as its tree's pin. Packages that
// only one tree uses (vite, expo, mermaid, ...) are outside this policy.
//
// --root points the check at a fixture tree instead of this checkout; only the
// gate's own Go test passes it.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const TREES = ["cmd/evener-hub/frontend", "mobile-native", "appwire-client/typescript", "docs-site"];
const SHARED_TOOLCHAIN = [
	"@biomejs/biome",
	"@types/react",
	"@types/react-dom",
	"@vitest/coverage-v8",
	"typescript",
	"vitest",
];
const EXACT_PER_TREE = ["react", "react-dom"];
const EXACT_VERSION = /^\d+\.\d+\.\d+$/;

const args = process.argv.slice(2);
if (args.includes("--help") || args.includes("-h")) {
	console.log("usage: js-toolchain-pins-check.mjs [--root DIR]\nFails if a shared JS toolchain package is not pinned to one exact version across the npm trees.");
	process.exit(0);
}
const rootFlag = args.indexOf("--root");
const root = rootFlag >= 0 ? resolve(args[rootFlag + 1]) : resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

const failures = [];
const declared = new Map(); // package -> [{tree, version}]
for (const tree of TREES) {
	const manifestPath = join(root, tree, "package.json");
	if (!existsSync(manifestPath)) {
		continue;
	}
	const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
	const deps = { ...manifest.dependencies, ...manifest.devDependencies };
	for (const name of [...SHARED_TOOLCHAIN, ...EXACT_PER_TREE]) {
		if (!(name in deps)) {
			continue;
		}
		if (!EXACT_VERSION.test(deps[name])) {
			failures.push(`${tree}/package.json: ${name} is "${deps[name]}", want an exact version`);
		}
		declared.set(name, [...(declared.get(name) ?? []), { tree, version: deps[name] }]);
	}
	const biomeConfig = join(root, tree, "biome.jsonc");
	if (existsSync(biomeConfig) && deps["@biomejs/biome"]) {
		const schema = /schemas\/([^/]+)\/schema\.json/.exec(readFileSync(biomeConfig, "utf8"));
		if (schema && schema[1] !== deps["@biomejs/biome"]) {
			failures.push(`${tree}/biome.jsonc: $schema names Biome ${schema[1]}, package.json pins ${deps["@biomejs/biome"]}`);
		}
	}
}
for (const [name, uses] of declared) {
	if (EXACT_PER_TREE.includes(name)) {
		continue;
	}
	if (new Set(uses.map((use) => use.version)).size > 1) {
		failures.push(`${name} differs across trees: ${uses.map((use) => `${use.tree} ${use.version}`).join(", ")}`);
	}
}

if (failures.length > 0) {
	console.error(`JS toolchain pins disagree (shared packages: exact versions, one version across trees):\n  ${failures.join("\n  ")}`);
	process.exit(1);
}
