// mobile-native/src/allowFontScaling.test.ts
//
// On iOS a text's size comes from useTextScale, so the platform must not scale
// it a second time. Every Text that sizes itself under Dynamic Type therefore
// passes ui.tsx's exported `allowFontScaling` (Platform.OS !== "ios") rather
// than writing that Platform check out again by hand. This scans the source
// tree for the inline spelling so the convention cannot drift back in.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const srcRoot = path.dirname(fileURLToPath(import.meta.url));

function sourceFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) out.push(...sourceFiles(full));
		else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(full);
	}
	return out;
}

it("no source writes a Platform check inline as the allowFontScaling prop", () => {
	const offenders: string[] = [];
	for (const file of sourceFiles(srcRoot)) {
		const text = readFileSync(file, "utf8");
		for (const match of text.matchAll(/allowFontScaling=\{([^}]*)\}/g)) {
			if (match[1].includes("Platform")) offenders.push(`${path.relative(srcRoot, file)}: ${match[0]}`);
		}
	}
	expect(offenders).toEqual([]);
});
