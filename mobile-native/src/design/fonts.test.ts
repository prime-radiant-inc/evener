// mobile-native/src/design/fonts.test.ts
//
// iOS picks a custom face by PostScript name and silently falls back to the
// system font when the name matches nothing, so this reads the names out of
// the font files app.json embeds rather than trusting tokens.ts.
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";
import { fonts } from "./tokens";

const root = fileURLToPath(new URL("../../", import.meta.url));

function postScriptName(path: string): string {
	const d = readFileSync(path);
	const tables = d.readUInt16BE(4);
	for (let i = 0; i < tables; i++) {
		const rec = 12 + 16 * i;
		if (d.toString("latin1", rec, rec + 4) !== "name") continue;
		const off = d.readUInt32BE(rec + 8);
		const count = d.readUInt16BE(off + 2);
		const strings = off + d.readUInt16BE(off + 4);
		for (let j = 0; j < count; j++) {
			const r = off + 6 + 12 * j;
			if (d.readUInt16BE(r) === 3 && d.readUInt16BE(r + 6) === 6) {
				const at = strings + d.readUInt16BE(r + 10);
				return Buffer.from(d.subarray(at, at + d.readUInt16BE(r + 8))).swap16().toString("utf16le");
			}
		}
	}
	throw new Error(`no PostScript name in ${path}`);
}

function embeddedFonts(): string[] {
	const app = JSON.parse(readFileSync(`${root}app.json`, "utf8"));
	const plugin = app.expo.plugins.find((p: unknown) => Array.isArray(p) && p[0] === "expo-font");
	if (!plugin) throw new Error("app.json has no expo-font plugin");
	return plugin[1].fonts;
}

it("app.json embeds exactly the faces tokens.ts names, and they exist", () => {
	const files = embeddedFonts();
	for (const f of files) expect(existsSync(`${root}${f}`), f).toBe(true);
	expect(files.map((f) => postScriptName(`${root}${f}`)).sort()).toEqual(
		[fonts.serif, fonts.serifItalic, fonts.serifSemibold].sort(),
	);
});
