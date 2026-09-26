// The redesign's design tokens: the web's palette
// (cmd/evener-hub/frontend/src/styles/tokens.css) as the redesign spec lists
// it (docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md,
// section 16), so the phone and the web speak one color language. Four hues
// carry meaning (amber: a human is needed; green: working; red: failed;
// blue: tappable, selected or unread); everything else is ink on paper.

export type Scheme = "light" | "dark";

export interface Palette {
	scheme: Scheme;
	page: string;
	canvas: string;
	surface: string;
	inset: string;
	pressed: string;
	edge: string;
	edgeStrong: string;
	inkHi: string;
	inkMid: string;
	inkLow: string;
	/** Long-form reading text: a step below ink-hi in dark mode to cut glare. */
	prose: string;
	attention: string;
	attentionInk: string;
	attentionBg: string;
	attentionEdge: string;
	alive: string;
	aliveInk: string;
	aliveBg: string;
	aliveEdge: string;
	danger: string;
	dangerInk: string;
	dangerBg: string;
	dangerEdge: string;
	accent: string;
	accentInk: string;
	accentBg: string;
	accentEdge: string;
	/** Filled buttons: white on it is 4.8:1 in both themes. */
	accentFill: string;
	onFill: string;
	/** Your message bubble: the accent tint, as on the web. */
	bubble: string;
	diffAdd: string;
	diffDel: string;
}

type Triple = [number, number, number];

// sRGB <-> OKLab with Björn Ottosson's reference matrices: the same math the
// web's token contract test uses to reproduce CSS color-mix(in oklab)
// (cmd/evener-hub/frontend/src/styles/token-contract.test.ts).
function toOklab(hex: string): Triple {
	const [r, g, b] = [1, 3, 5].map((i) => {
		const c = Number.parseInt(hex.slice(i, i + 2), 16) / 255;
		return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
	});
	const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
	const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
	const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
	return [
		0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
		1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
		0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
	];
}

function fromOklab([L, a, b]: Triple): string {
	const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
	const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
	const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
	const linear = [
		4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
		-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
		-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
	];
	return `#${linear
		.map((v) => {
			const c = v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055;
			return Math.max(0, Math.min(255, Math.round(c * 255))).toString(16).padStart(2, "0");
		})
		.join("")
		.toUpperCase()}`;
}

/** `hue` laid over `base` at `amount` (0 to 1), blended in OKLab as the web's
 * `color-mix(in oklab, …)` does: the spec's tints ("the hue at 15% over
 * surface"). Two in-gamut colors mix in gamut, so a clamp is enough. */
export function mix(hue: string, base: string, amount: number): string {
	const h = toOklab(hue);
	const b = toOklab(base);
	return fromOklab([0, 1, 2].map((i) => h[i] * amount + b[i] * (1 - amount)) as Triple);
}

type Core = Omit<Palette, "attentionBg" | "attentionEdge" | "aliveBg" | "aliveEdge" | "dangerBg" | "dangerEdge" | "accentBg" | "accentEdge" | "bubble">;

function withTints(core: Core): Palette {
	const bg = (hue: string) => mix(hue, core.surface, 0.15);
	const edge = (hue: string) => mix(hue, core.edge, 0.4);
	return {
		...core,
		attentionBg: bg(core.attention),
		attentionEdge: edge(core.attention),
		aliveBg: bg(core.alive),
		aliveEdge: edge(core.alive),
		dangerBg: bg(core.danger),
		dangerEdge: edge(core.danger),
		accentBg: bg(core.accent),
		accentEdge: edge(core.accent),
		bubble: bg(core.accent),
	};
}

export const palettes: Record<Scheme, Palette> = {
	light: withTints({
		scheme: "light",
		page: "#FAF9F6",
		canvas: "#F1F0EB",
		surface: "#FCFBF8",
		inset: "#F4F3EE",
		pressed: "#F0EFE9",
		edge: "#DDDCD4",
		edgeStrong: "#B7B6AC",
		inkHi: "#252521",
		inkMid: "#5F5F57",
		inkLow: "#6D6D64",
		prose: "#252521",
		attention: "#F59E0B",
		attentionInk: "#AD5209",
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
	}),
	dark: withTints({
		scheme: "dark",
		page: "#191918",
		canvas: "#1D1D1B",
		surface: "#232320",
		inset: "#20201E",
		pressed: "#2B2B28",
		edge: "#34342F",
		edgeStrong: "#51514A",
		inkHi: "#F2F1EB",
		inkMid: "#B0AFA6",
		inkLow: "#99998F",
		prose: "#E0DED6",
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
	}),
};

/** The palette for React Native's color scheme; anything but "dark" (null,
 * "unspecified") is light. */
export function paletteFor(scheme: string | null | undefined): Palette {
	return scheme === "dark" ? palettes.dark : palettes.light;
}

/** iOS PostScript names of the embedded Source Serif 4 faces (see app.json's
 * expo-font plugin), and the app's existing machine face. A custom face is
 * chosen by name, never by fontWeight. */
export const fonts = {
	serif: "SourceSerif4-Regular",
	serifItalic: "SourceSerif4-Italic",
	serifSemibold: "SourceSerif4-SemiBold",
	mono: "Menlo",
} as const;

/** Reading roles from spec 16.2. Sizes are points before Dynamic Type; callers
 * multiply fontSize and lineHeight by the iOS font scale, as ui.tsx does. */
export const typeRoles = {
	agentProse: { fontFamily: fonts.serif, fontSize: 17, lineHeight: 26 },
	yourMessage: { fontFamily: fonts.serif, fontSize: 17, lineHeight: 25 },
	document: { fontFamily: fonts.serif, fontSize: 18, lineHeight: 28 },
	machine: { fontFamily: fonts.mono, fontSize: 13, lineHeight: 18 },
} as const;
