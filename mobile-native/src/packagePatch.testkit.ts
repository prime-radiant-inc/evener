import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// mobile-native fixes some third-party native source with patch-package
// (patches/). The native gate has no iOS toolchain to compile or run that
// Objective-C, so a patch's test reads the committed diff instead.

/** The added (`+`) lines of the committed patch for `pkg`@`version`, for one
 * package-relative path. */
export function patchAddedLines(pkg: string, version: string, path: string): string {
	const patchPath = fileURLToPath(new URL(`../patches/${pkg}+${version}.patch`, import.meta.url));
	const lines = readFileSync(patchPath, "utf8").split("\n");
	const start = lines.findIndex((line) => line.startsWith(`diff --git a/node_modules/${pkg}/${path} `));
	if (start === -1) throw new Error(`the patch has no diff for ${path}`);
	const rest = lines.slice(start + 1);
	const end = rest.findIndex((line) => line.startsWith("diff --git "));
	const body = end === -1 ? rest : rest.slice(0, end);
	return body
		.filter((line) => line.startsWith("+") && !line.startsWith("+++"))
		.map((line) => line.slice(1))
		.join("\n");
}
