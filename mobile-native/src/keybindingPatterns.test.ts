import { parseKeybinding } from "tinykeys";
import { describe, expect, it } from "vitest";
import { parseKeybinding as parseWeb } from "../../cmd/evener-hub/frontend/node_modules/tinykeys/dist/tinykeys.mjs";
import { checkedKeybindingChange, keybindingPreview } from "./keybindingRules";

const samples = [
	"A",
	"a",
	"F",
	"f",
	"6",
	"K",
	"k",
	"K",
	"ſ",
	"Ꭰ",
	"ꭰ",
	"𐐀",
	"𐐨",
	"F6",
	"f6",
	"F7",
	"F8",
	"é",
	"ß",
	"😀",
	"α",
	"Enter",
];
const patterns = [
	"F6|F7",
	String.raw`[\p{ASCII}&&\p{Letter}]`,
	String.raw`[[A-Z]--[F]]`,
	String.raw`[^\p{Lowercase_Letter}]`,
	String.raw`\P{Lowercase_Letter}`,
	String.raw`[\P{Lowercase_Letter}&&\p{Letter}]`,
	String.raw`\p{Script=Greek}`,
	String.raw`\p{Basic_Emoji}`,
	String.raw`[[\q{F6|F7}]--[\q{F7}]]`,
];

describe("native shortcut pattern compilation", () => {
	it.each(patterns)(
		"compiles %s to UnicodeSets membership without the v flag",
		(pattern) => {
			const chord = `Control+(${pattern})`;
			const native = parseKeybinding(chord)[0]?.[2];
			const web = parseWeb(chord)[0]?.[2];
			expect(native).toBeInstanceOf(RegExp);
			expect(web).toBeInstanceOf(RegExp);
			if (!(native instanceof RegExp) || !(web instanceof RegExp))
				throw new Error("Expected pattern matchers");
			expect(native.flags).not.toContain("v");
			for (const sample of samples) {
				// V8's ASCII-property fast path omits simple case folding for these
				// two members. Use the ECMAScript membership oracle for that case;
				// see docs/design/mobile/keybinding-patterns.md for the derivation.
				const expected =
					pattern === String.raw`[\p{ASCII}&&\p{Letter}]` &&
					(sample === "K" || sample === "ſ")
						? true
						: web.test(sample);
				expect(native.test(sample), `${pattern}: ${sample}`).toBe(expected);
				expect(native.test(sample)).toBe(expected);
				expect(new RegExp(native.source, native.flags).test(sample)).toBe(
					expected,
				);
			}
		},
	);
	it("preserves the authored pattern through the real native preview and change validator", () => {
		const chord = String.raw`([\p{ASCII}&&\p{Letter}])`;
		const changed = checkedKeybindingChange([], "palette.open", chord);
		expect(changed).toEqual([{ action: "palette.open", chord }]);
		expect(keybindingPreview(changed).warnings).toEqual([]);
		expect(
			keybindingPreview(changed).rows.find(
				(row) => row.actionId === "palette.open",
			)?.shortcuts,
		).toEqual([chord]);
	});
	it("rejects the same invalid Unicode sets as the web parser", () => {
		for (const chord of ["([a&&])", "([a--])", "(\\p{NotAProperty})"]) {
			expect(() => parseWeb(chord)).toThrow();
			expect(() => parseKeybinding(chord)).toThrow();
		}
	});
});
