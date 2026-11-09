#!/usr/bin/env node
// Writes a generated Xcode project's complete parsed content as canonical,
// sorted-key JSON. The qualification runner hashes this form when comparing
// repeated prebuild output, because Expo's writer may reformat an unchanged
// empty PBX dictionary while the project's objects, keys, UUIDs and references
// stay identical. The parser is the real @bacons/xcode parser that generated
// the project, resolved from the installed native dependency graph.
import { createRequire } from "node:module";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const [nativeDir, projectPath, outputPath] = process.argv.slice(2);
if (!nativeDir || !projectPath || !outputPath) {
	console.error("usage: project-canonicalize.mjs NATIVE_DIR PROJECT_FILE OUTPUT_FILE");
	process.exit(2);
}
const require = createRequire(path.resolve(nativeDir, "package.json"));
const { parse } = require("@bacons/xcode/json");

// Sorting object keys gives one project one canonical form, independent of the
// writer's layout or key ordering.
const canonicalize = (value) => {
	if (Array.isArray(value)) {
		return value.map(canonicalize);
	}
	if (value !== null && typeof value === "object") {
		return Object.fromEntries(
			Object.keys(value)
				.sort()
				.map((key) => [key, canonicalize(value[key])]),
		);
	}
	return value;
};

const project = canonicalize(parse(await readFile(projectPath, "utf8")));
await writeFile(outputPath, `${JSON.stringify(project, null, "\t")}\n`);
