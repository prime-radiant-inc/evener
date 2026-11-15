import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { mix, palettes as sharedPalettes, type ThemePalette } from "@evener/appwire-client/palette";
import { contrast } from "./contrast.testkit";
import { fonts, paletteFor, palettes, type Palette, scaledType, typeRoles, uiType } from "./tokens";

// The phone's role -> the shared role it must render. Restated here rather
// than read from design/tokens.ts's alias table: this is the expected
// contract, so a one-sided edit to that table fails.
const NATIVE_ALIASES: Record<string, keyof ThemePalette> = {
	page: "surface-0",
	canvas: "surface-canvas",
	surface: "surface-1",
	inset: "surface-inset",
	pressed: "hover-2",
	edge: "edge",
	edgeStrong: "edge-strong",
	inkHi: "ink-hi",
	inkMid: "ink-mid",
	inkLow: "ink-low",
	prose: "prose-ink",
	attention: "attention",
	attentionInk: "attention-ink",
	attentionBg: "attention-bg",
	attentionEdge: "attention-edge",
	alive: "alive",
	aliveInk: "alive-ink",
	aliveBg: "alive-bg",
	aliveEdge: "alive-edge",
	danger: "danger",
	dangerInk: "danger-ink",
	dangerBg: "danger-bg",
	dangerEdge: "danger-edge",
	accent: "accent",
	accentInk: "accent-ink",
	accentBg: "accent-bg",
	accentEdge: "accent-edge",
	accentFill: "accent-fill",
	onFill: "on-fill",
	bubble: "accent-bg",
	diffAdd: "diff-add-bg",
	diffDel: "diff-del-bg",
};

describe("the native palette aliases the shared palette", () => {
	it.each(["light", "dark"] as const)("%s", (scheme) => {
		for (const [role, sharedRole] of Object.entries(NATIVE_ALIASES)) {
			expect(palettes[scheme][role as keyof typeof palettes.light], `${role} -> ${sharedRole}`).toBe(
				sharedPalettes[scheme][sharedRole],
			);
		}
	});

	it("covers every Palette role", () => {
		expect(
			Object.keys(palettes.light)
				.filter((role) => role !== "scheme")
				.sort(),
		).toEqual(Object.keys(NATIVE_ALIASES).sort());
	});
});

const PALETTE_CONSUMERS = [
	["design/tokens.ts", readFileSync(new URL("./tokens.ts", import.meta.url), "utf8")],
	["ansiOutputStyles.ts", readFileSync(new URL("../ansiOutputStyles.ts", import.meta.url), "utf8")],
] as const;

describe("the phone keeps no palette hex of its own", () => {
	it.each(PALETTE_CONSUMERS)("%s", (_path, text) => {
		const withoutComments = text.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/\/\/[^\n]*/g, " ");
		expect(withoutComments.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []).toEqual([]);
	});
});

// The frontmatter is the phone's design record; keep it tied to the palette
// it documents instead of trusting a hand edit.
const DESIGN_FRONTMATTER: Record<string, Exclude<keyof Palette, "scheme">> = {
	background: "page",
	surface: "inset",
	text: "inkHi",
	secondary: "inkMid",
	border: "edge",
	accent: "accentInk",
	"accent-fill": "accentFill",
	error: "dangerInk",
	raised: "surface",
	canvas: "canvas",
	"attention-ink": "attentionInk",
	"quiet-ink": "inkLow",
	prose: "prose",
	pressed: "pressed",
	"strong-border": "edgeStrong",
	"on-fill": "onFill",
	attention: "attention",
	"attention-bg": "attentionBg",
	"attention-edge": "attentionEdge",
	alive: "alive",
	"alive-ink": "aliveInk",
	danger: "danger",
	"accent-mark": "accent",
	"accent-bg": "accentBg",
	"accent-edge": "accentEdge",
};

describe("DESIGN.md records the shared palette", () => {
	const design = readFileSync(new URL("../../DESIGN.md", import.meta.url), "utf8");
	const colors = /^colors:\n([\s\S]*?)^typography:/m.exec(design)?.[1] ?? "";

	it.each(["light", "dark"] as const)("%s frontmatter", (scheme) => {
		for (const [key, role] of Object.entries(DESIGN_FRONTMATTER)) {
			const value = new RegExp(`^  ${scheme}-${key}: "(#[0-9A-Fa-f]{6})"$`, "m").exec(colors)?.[1];
			expect(value?.toUpperCase(), `${scheme}-${key}`).toBe(palettes[scheme][role].toUpperCase());
		}
	});
});

describe.each(Object.entries(palettes))("%s text contrast", (_scheme, palette) => {
	it("keeps interface text and placeholders readable on every neutral surface", () => {
		for (const surface of [palette.page, palette.canvas, palette.surface, palette.inset, palette.pressed]) {
			for (const ink of [
				palette.inkHi,
				palette.inkMid,
				palette.inkLow,
				palette.prose,
				palette.accentInk,
				palette.attentionInk,
				palette.aliveInk,
				palette.dangerInk,
			]) {
				expect(contrast(ink, surface), `${ink} on ${surface}`).toBeGreaterThanOrEqual(4.5);
			}
		}
	});
	it("keeps semantic labels readable on their own tints and filled actions", () => {
		for (const [ink, surface] of [
			[palette.attentionInk, palette.attentionBg],
			[palette.aliveInk, palette.aliveBg],
			[palette.dangerInk, palette.dangerBg],
			[palette.accentInk, palette.accentBg],
			[palette.onFill, palette.accentFill],
		])
			expect(contrast(ink, surface), `${ink} on ${surface}`).toBeGreaterThanOrEqual(4.5);
	});

	it("keeps the pressed wash visibly distinct from every neutral surface", () => {
		for (const surface of [palette.page, palette.canvas, palette.surface, palette.inset]) {
			expect(contrast(palette.pressed, surface), `${palette.pressed} on ${surface}`).toBeGreaterThanOrEqual(1.1);
		}
	});
});

describe("tints", () => {
	it("mix blends in OKLab, as CSS color-mix does, and rounds each channel", () => {
		expect(mix("#000000", "#FFFFFF", 0.5)).toBe("#636363");
		expect(mix("#F59E0B", "#FCFBF8", 0.15)).toBe("#FCEEDC");
	});
});

describe("paletteFor", () => {
	it("follows the system scheme and treats an unknown scheme as light", () => {
		expect(paletteFor("dark").scheme).toBe("dark");
		expect(paletteFor("light").scheme).toBe("light");
		expect(paletteFor(null).scheme).toBe("light");
		expect(paletteFor(undefined).scheme).toBe("light");
		expect(paletteFor("unspecified").scheme).toBe("light");
	});
});

describe("type", () => {
	it("names the embedded Source Serif 4 faces and the app's machine face", () => {
		expect(fonts).toEqual({
			serif: "SourceSerif4-Regular",
			serifItalic: "SourceSerif4-Italic",
			serifSemibold: "SourceSerif4-SemiBold",
			serifSemiboldItalic: "SourceSerif4-SemiBoldItalic",
			mono: "Menlo",
		});
	});
	it("sets the spec's reading sizes", () => {
		expect(typeRoles.agentProse).toEqual({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 26 });
		expect(typeRoles.yourMessage).toEqual({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 25 });
		expect(typeRoles.document).toEqual({ fontFamily: "SourceSerif4-Regular", fontSize: 18, lineHeight: 28 });
		expect(typeRoles.machine).toEqual({ fontFamily: "Menlo", fontSize: 13, lineHeight: 18 });
	});
	it("sets the spec's interface sizes for the grouped lists", () => {
		expect(uiType.listRow).toEqual({ fontSize: 17, lineHeight: 22 });
		expect(uiType.subheadline).toEqual({ fontSize: 15, lineHeight: 20 });
		expect(uiType.footnote).toEqual({ fontSize: 13, lineHeight: 18 });
		expect(uiType.sectionLabel).toEqual({
			fontSize: 15,
			lineHeight: 20,
			fontWeight: "600",
		});
	});
	it("scales a role's size, line height and tracking by the text scale, and keeps the rest", () => {
		const label = scaledType({ fontSize: 12, lineHeight: 16, letterSpacing: 0.72, fontWeight: "600" }, 1.5);
		expect(label).toMatchObject({ fontSize: 18, lineHeight: 24, fontWeight: "600" });
		// Tracking belongs to the role, not to the section-label convention.
		expect(label.letterSpacing).toBeCloseTo(1.08);
		expect(scaledType(typeRoles.machine, 2)).toEqual({ fontFamily: "Menlo", fontSize: 26, lineHeight: 36 });
	});
});
