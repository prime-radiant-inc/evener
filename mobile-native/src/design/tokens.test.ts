import { describe, expect, it } from "vitest";
import { mix, paletteFor, palettes } from "./tokens";

describe("the palette is the spec's (section 16.1)", () => {
	it("light", () => {
		expect(palettes.light).toMatchObject({
			scheme: "light",
			page: "#FAF9F6", canvas: "#F1F0EB", surface: "#FCFBF8", inset: "#F4F3EE", pressed: "#F0EFE9",
			edge: "#DDDCD4", edgeStrong: "#B7B6AC",
			inkHi: "#252521", inkMid: "#5F5F57", inkLow: "#6D6D64", prose: "#252521",
			attention: "#F59E0B", attentionInk: "#AD5209",
			alive: "#189A4D", aliveInk: "#12763B",
			danger: "#E3474C", dangerInk: "#C51D23",
			accent: "#0285FF", accentInk: "#0064C2", accentFill: "#0070E0", onFill: "#FFFFFF",
			diffAdd: "#E9F4EE", diffDel: "#F5EAF0",
		});
	});
	it("dark", () => {
		expect(palettes.dark).toMatchObject({
			scheme: "dark",
			page: "#191918", canvas: "#1D1D1B", surface: "#232320", inset: "#20201E", pressed: "#2B2B28",
			edge: "#34342F", edgeStrong: "#51514A",
			inkHi: "#F2F1EB", inkMid: "#B0AFA6", inkLow: "#99998F", prose: "#E0DED6",
			attention: "#F68F3C", attentionInk: "#F68F3C",
			alive: "#3DBB72", aliveInk: "#3DBB72",
			danger: "#EE5C61", dangerInk: "#F17478",
			accent: "#3D9AFF", accentInk: "#459EFF", accentFill: "#0070E0", onFill: "#FFFFFF",
			diffAdd: "#19251A", diffDel: "#170B17",
		});
	});
});

describe("tints: the hue at 15% over surface, edges at 40% over edge", () => {
	it("mix blends in OKLab, as CSS color-mix does, and rounds each channel", () => {
		expect(mix("#000000", "#FFFFFF", 0.5)).toBe("#636363");
		expect(mix("#F59E0B", "#FCFBF8", 0.15)).toBe("#FCEEDC");
	});
	it("light tints", () => {
		expect(palettes.light).toMatchObject({
			attentionBg: "#FCEEDC", attentionEdge: "#E9C598", aliveBg: "#DEEDDE", aliveEdge: "#9BC29E",
			dangerBg: "#FDE2DD", dangerEdge: "#E7A69D", accentBg: "#DDEBFC", accentEdge: "#97BDEA",
			bubble: "#DDEBFC",
		});
	});
	it("dark tints", () => {
		expect(palettes.dark).toMatchObject({
			attentionBg: "#3F3227", attentionEdge: "#7D583A", aliveBg: "#2B372C", aliveEdge: "#416749",
			dangerBg: "#3F2D29", dangerEdge: "#7C4743", accentBg: "#2A343D", accentEdge: "#3D5C7D",
			bubble: "#2A343D",
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
