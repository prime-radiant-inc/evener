// Native neutral surfaces, with the same semantic hues as the web:
// amber needs a human, green is working, red failed, and blue is actionable
// or unread. Platform surfaces differ; the meanings do not.

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
			return Math.max(0, Math.min(255, Math.round(c * 255)))
				.toString(16)
				.padStart(2, "0");
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

type Core = Omit<
	Palette,
	| "attentionBg"
	| "attentionEdge"
	| "aliveBg"
	| "aliveEdge"
	| "dangerBg"
	| "dangerEdge"
	| "accentBg"
	| "accentEdge"
	| "bubble"
>;

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
	}),
	dark: withTints({
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
	}),
};

/** The palette for React Native's color scheme; anything but "dark" (null,
 * "unspecified") is light. */
export function paletteFor(scheme: string | null | undefined): Palette {
	return scheme === "dark" ? palettes.dark : palettes.light;
}

/** iOS PostScript names of the embedded Source Serif 4 faces (app.config.js's
 * expo-font plugin) and the app's existing machine face. Markdown's strong
 * and em reach the SemiBold, Italic and SemiBold Italic faces by trait
 * matching inside the "Source Serif 4" family, so every face here is in use
 * even where no code names it. The serif names are iPhone-only: Android
 * resolves an embedded font by its file name (SourceSerif4_400Regular), not
 * by these names, so there the text falls back to the system face. The
 * redesign qualifies iPhone only (see the roadmap); Android keeps building. */
export const fonts = {
	serif: "SourceSerif4-Regular",
	serifItalic: "SourceSerif4-Italic",
	serifSemibold: "SourceSerif4-SemiBold",
	serifSemiboldItalic: "SourceSerif4-SemiBoldItalic",
	mono: "Menlo",
} as const;

/** Reading roles from spec 16.2, in points before Dynamic Type. A React
 * Native Text with font scaling off multiplies fontSize and lineHeight by the
 * iOS font scale, as ui.tsx's Copy does; EnrichedMarkdownText scales on its
 * own, so MarkdownResponse passes a role as it is. */
export const typeRoles = {
	agentProse: { fontFamily: fonts.serif, fontSize: 17, lineHeight: 26 },
	yourMessage: { fontFamily: fonts.serif, fontSize: 17, lineHeight: 25 },
	document: { fontFamily: fonts.serif, fontSize: 18, lineHeight: 28 },
	machine: { fontFamily: fonts.mono, fontSize: 13, lineHeight: 18 },
} as const;

/** Interface roles in points before Dynamic Type; scaledType applies the
 * text scale. System fonts keep interface text distinct from reading text. */
export const uiType = {
	listRow: { fontSize: 17, lineHeight: 22 },
	/** The why line: Subheadline. */
	subheadline: { fontSize: 15, lineHeight: 20 },
	/** Meta and captions: a row's second line, a group's footer. */
	footnote: { fontSize: 13, lineHeight: 18 },
	/** Spec 16.2's Page status line, the Hub's connection line and the Live
	 * summary's counts: 14pt, as the prototype's Hub status line (hub.js:17),
	 * on the 20pt line of its Live summary (styles.css:285, .live-sum). A
	 * sheet's reconnecting line is Footnote. */
	statusLine: { fontSize: 14, lineHeight: 20 },
	/** Sentence-case section headings, legible without tracked capitals. */
	sectionLabel: {
		fontSize: 15,
		lineHeight: 20,
		fontWeight: "600",
	},
} as const;

/** A type role sized for the text scale: a React Native Text with font scaling
 * off multiplies fontSize, lineHeight and letterSpacing by the iOS font scale. */
export function scaledType<Role extends { fontSize: number; lineHeight: number; letterSpacing?: number }>(
	role: Role,
	scale: number,
): Role {
	const scaled = { ...role, fontSize: role.fontSize * scale, lineHeight: role.lineHeight * scale };
	// Tracking is in points but specified in ems (+0.06em), so it grows with the size.
	return role.letterSpacing === undefined ? scaled : { ...scaled, letterSpacing: role.letterSpacing * scale };
}

/** Spacing from spec 16.3 (a 4pt base) and the prototype's grouped list. */
export const space = {
	/** The side margin of a group, a segmented control or a field. */
	margin: 16,
	/** Between two groups (the prototype's `.group + .group`). */
	groupGap: 16,
	/** A section label's and a footer's text: the margin plus a row's inset. */
	labelInset: 32,
	/** A row's padding from the group's edge. */
	rowInset: 16,
	/** A row's vertical padding (spec 16.3): with a 22pt line, a 46pt row. */
	rowPadding: 12,
	/** Between a row's glyph, text, value and chevron. */
	rowGap: 12,
} as const;
