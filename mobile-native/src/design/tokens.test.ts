import { describe, expect, it } from "vitest";
import { contrast } from "./contrast.testkit";
import { fonts, mix, paletteFor, palettes, scaledType, typeRoles, uiType } from "./tokens";

describe("the native neutral palette", () => {
	it("light", () => {
		expect(palettes.light).toMatchObject({
			scheme: "light",
			page: "#FFFFFF",
			canvas: "#F2F2F7",
			surface: "#FFFFFF",
			inset: "#EDEDF2",
			pressed: "#E5E5EA",
			edge: "#D8D8DF",
			edgeStrong: "#B8B8C1",
			inkHi: "#1C1C1E",
			inkMid: "#55555E",
			inkLow: "#62626D",
			prose: "#1C1C1E",
			attention: "#F59E0B",
			attentionInk: "#9C4908",
			alive: "#189A4D",
			aliveInk: "#12763B",
			danger: "#E3474C",
			dangerInk: "#C51D23",
			accent: "#0285FF",
			accentInk: "#0064C2",
			accentFill: "#0070E0",
			onFill: "#FFFFFF",
			diffAdd: "#E9F4EE",
			diffDel: "#F5EAF0",
		});
	});
	it("dark", () => {
		expect(palettes.dark).toMatchObject({
			scheme: "dark",
			page: "#111113",
			canvas: "#08080A",
			surface: "#1C1C1E",
			inset: "#252528",
			pressed: "#303034",
			edge: "#38383D",
			edgeStrong: "#56565F",
			inkHi: "#F5F5F7",
			inkMid: "#B8B8C1",
			inkLow: "#A5A5B0",
			prose: "#E3E3E8",
			attention: "#F68F3C",
			attentionInk: "#F68F3C",
			alive: "#3DBB72",
			aliveInk: "#3DBB72",
			danger: "#EE5C61",
			dangerInk: "#F17478",
			accent: "#3D9AFF",
			accentInk: "#459EFF",
			accentFill: "#0070E0",
			onFill: "#FFFFFF",
			diffAdd: "#19251A",
			diffDel: "#170B17",
		});
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
});

describe("tints: the hue at 15% over surface, edges at 40% over edge", () => {
	it("mix blends in OKLab, as CSS color-mix does, and rounds each channel", () => {
		expect(mix("#000000", "#FFFFFF", 0.5)).toBe("#636363");
		expect(mix("#F59E0B", "#FCFBF8", 0.15)).toBe("#FCEEDC");
	});
	it("light tints", () => {
		expect(palettes.light).toMatchObject({
			attentionBg: "#FFF1E2",
			attentionEdge: "#E6C3A0",
			aliveBg: "#E1F0E3",
			aliveEdge: "#98C0A5",
			dangerBg: "#FFE5E3",
			dangerEdge: "#E4A4A4",
			accentBg: "#DFEEFF",
			accentEdge: "#94BAEF",
			bubble: "#DFEEFF",
		});
	});
	it("dark tints", () => {
		expect(palettes.dark).toMatchObject({
			attentionBg: "#382C26",
			attentionEdge: "#805B45",
			aliveBg: "#24312A",
			aliveEdge: "#436A53",
			dangerBg: "#382728",
			dangerEdge: "#7F4A4C",
			accentBg: "#232D3B",
			accentEdge: "#405E86",
			bubble: "#232D3B",
		});
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
