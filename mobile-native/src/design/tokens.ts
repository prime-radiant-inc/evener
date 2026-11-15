// The native palette aliases the one shared palette in
// @evener/appwire-client/palette, so the phone and the web render the same
// colors from the same source. Role names keep the phone's surface
// vocabulary; every value comes from the shared role named in NATIVE_ALIASES
// below, and that shared module owns the one OKLab mix the tints are derived
// with.

import { palettes as sharedPalettes, type ThemePalette } from "@evener/appwire-client/palette";

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

type NativeRole = Exclude<keyof Palette, "scheme">;

// The phone's role -> the shared role it renders. The surface names keep
// their platform meaning (page, grouped canvas, raised surface, inset field,
// pressed wash) while their values are the web's. `pressed` is the shared
// pressed/selected wash (--hover-2); its light value and the light -ink
// companions are tuned together so every ink clears 4.5:1 on it while the
// wash stays visibly distinct from the page. prose/accentFill/onFill are the
// module's NativeExtras: roles only the phone declares.
const NATIVE_ALIASES = {
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
} as const satisfies Record<NativeRole, keyof ThemePalette>;

function nativePalette(scheme: Scheme): Palette {
	const shared = sharedPalettes[scheme];
	const aliased = Object.fromEntries(
		Object.entries(NATIVE_ALIASES).map(([role, sharedRole]) => [role, shared[sharedRole]]),
	) as Omit<Palette, "scheme">;
	return { scheme, ...aliased };
}

export const palettes: Record<Scheme, Palette> = {
	light: nativePalette("light"),
	dark: nativePalette("dark"),
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
