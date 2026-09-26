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

function channels(hex: string): [number, number, number] {
	return [1, 3, 5].map((i) => Number.parseInt(hex.slice(i, i + 2), 16)) as [number, number, number];
}

/** `hue` laid over `base` at `amount` (0 to 1), blended per sRGB channel and
 * rounded: the spec's tints ("the hue at 15% over surface"). The web's
 * color-mix blends the same formula in OKLab; the two land within about one
 * just-noticeable difference of each other. */
export function mix(hue: string, base: string, amount: number): string {
	const h = channels(hue);
	const b = channels(base);
	return `#${b
		.map((c, i) => Math.round(c + (h[i] - c) * amount).toString(16).padStart(2, "0"))
		.join("")
		.toUpperCase()}`;
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
