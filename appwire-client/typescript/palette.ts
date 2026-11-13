/*
 * The one palette both clients render, and the one OKLab mix that derives its
 * tints.
 *
 * The web declares `WebPalette` as custom properties in
 * cmd/evener-hub/frontend/src/styles/tokens.css. Its token contract
 * (src/styles/token-contract.test.ts) fails when a block drifts from
 * `webPalettes`, so tokens.css cannot grow a second palette.
 *
 * The phone aliases these roles onto its Palette in
 * mobile-native/src/design/tokens.ts and derives its ANSI run colors from
 * them; it holds no palette hex of its own. Its surface roles keep the
 * phone's vocabulary (page, canvas, surface, inset, pressed) but take the
 * shared values, so the two clients render one palette rather than two
 * transcriptions of the same idea. `NativeExtras` is the three roles only the
 * phone declares: its reading ink, and a filled-action pair that holds one
 * value in both themes where the web's accent/on-accent pair turns with the
 * theme.
 *
 * Role names are the web custom properties without the leading `--`, which
 * keeps the drift check, the aliases and this source on one vocabulary.
 *
 * This module is part of @evener/appwire-client (subpath `./palette`), the
 * package both clients already consume; import it by name. Its values are
 * tuned together: the light hover-2 wash is also the phone's pressed ground,
 * and the light -ink companions clear 4.5:1 on it because the phone renders
 * every ink on a pressed row.
 */

export type Scheme = "light" | "dark";

/** Every color the web declares as a custom property in tokens.css. */
export interface WebPalette {
  "surface-0": string;
  "surface-canvas": string;
  "surface-1": string;
  "surface-2": string;
  "surface-inset": string;
  "hover-1": string;
  "hover-2": string;
  field: string;
  edge: string;
  "edge-strong": string;
  "ink-hi": string;
  "ink-mid": string;
  "ink-low": string;
  attention: string;
  alive: string;
  danger: string;
  accent: string;
  "attention-bg": string;
  "attention-edge": string;
  "alive-bg": string;
  "alive-edge": string;
  "danger-bg": string;
  "danger-edge": string;
  "accent-bg": string;
  "accent-edge": string;
  "selection-bg": string;
  "attention-ink": string;
  "alive-ink": string;
  "danger-ink": string;
  "accent-ink": string;
  /** Text and glyphs on a solid accent fill (the one primary action). It
   * clears 4.5:1 on accent in both themes. */
  "on-accent": string;
  "tooltip-bg": string;
  "tooltip-fg": string;
  "tooltip-muted": string;
  "tooltip-border": string;
  "diff-add-bg": string;
  "diff-del-bg": string;
  "ansi-black": string;
  "ansi-red": string;
  "ansi-green": string;
  "ansi-yellow": string;
  "ansi-blue": string;
  "ansi-magenta": string;
  "ansi-cyan": string;
  "ansi-white": string;
  "ansi-bright-black": string;
  "ansi-bright-red": string;
  "ansi-bright-green": string;
  "ansi-bright-yellow": string;
  "ansi-bright-blue": string;
  "ansi-bright-magenta": string;
  "ansi-bright-cyan": string;
  "ansi-bright-white": string;
  "shadow-color": string;
}

/** The roles only the phone declares; the web has no matching token. */
export interface NativeExtras {
  /** The phone's long-form reading ink, a step below ink-hi in dark mode to
   * cut glare. The web's reading surfaces use ink-hi. */
  "prose-ink": string;
  /** The phone's filled action's fill: the brand indigo at one value in both
   * themes, where the web's accent lightens in dark and takes a dark
   * on-accent. on-fill clears 4.5:1 on it. */
  "accent-fill": string;
  /** The symbol and label that ride on accent-fill. */
  "on-fill": string;
}

export type ThemePalette = WebPalette & NativeExtras;

type TintRole =
  | "attention-bg"
  | "attention-edge"
  | "alive-bg"
  | "alive-edge"
  | "danger-bg"
  | "danger-edge"
  | "accent-bg"
  | "accent-edge"
  | "selection-bg";

type BaseWebPalette = Omit<WebPalette, TintRole>;
type Triple = [number, number, number];

// sRGB <-> OKLab with Björn Ottosson's reference matrices, the same math CSS
// color-mix(in oklab, ...) applies. Both clients derive from this function;
// the web's token contract also uses it to reproduce the CSS mix when it
// compares tokens.css with the values below.
function srgbChannelToLinear(channel: number): number {
  const c = channel / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function toOklab(hex: string): Triple {
  const r = srgbChannelToLinear(Number.parseInt(hex.slice(1, 3), 16));
  const g = srgbChannelToLinear(Number.parseInt(hex.slice(3, 5), 16));
  const b = srgbChannelToLinear(Number.parseInt(hex.slice(5, 7), 16));
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
 * `color-mix(in oklab, ...)` does: the spec's tints ("the hue at 15% over
 * surface"). Two in-gamut colors mix in gamut, so a clamp is enough. */
export function mix(hue: string, base: string, amount: number): string {
  const h = toOklab(hue);
  const b = toOklab(base);
  return fromOklab([
    h[0] * amount + b[0] * (1 - amount),
    h[1] * amount + b[1] * (1 - amount),
    h[2] * amount + b[2] * (1 - amount),
  ]);
}

function withTints(base: BaseWebPalette): WebPalette {
  const bg = (hue: string) => mix(hue, base["surface-1"], 0.15);
  const edge = (hue: string) => mix(hue, base.edge, 0.4);
  return {
    ...base,
    "attention-bg": bg(base.attention),
    "attention-edge": edge(base.attention),
    "alive-bg": bg(base.alive),
    "alive-edge": edge(base.alive),
    "danger-bg": bg(base.danger),
    "danger-edge": edge(base.danger),
    "accent-bg": bg(base.accent),
    "accent-edge": edge(base.accent),
    "selection-bg": mix(base.accent, base["surface-1"], 0.4),
  };
}

/** The web's palette: the roles tokens.css must declare, theme by theme. */
export const webPalettes: Record<Scheme, WebPalette> = {
  light: withTints({
    "surface-0": "#F6F7F9",
    "surface-canvas": "#EEF0F3",
    "surface-1": "#FFFFFF",
    "surface-2": "#FFFFFF",
    "surface-inset": "#F2F4F7",
    "hover-1": "#E7EAEF",
    // Shared pressed/selected wash. It must stay visibly darker than the
    // page and light enough for every -ink companion to clear 4.5:1 on it
    // (the phone's pressed-state test enforces both).
    "hover-2": "#DCDEE3",
    field: "#F1F3F6",
    edge: "#D5D9E0",
    "edge-strong": "#A7AEBA",
    "ink-hi": "#1A1D23",
    "ink-mid": "#555B66",
    "ink-low": "#5C626D",
    attention: "#F59E0B",
    alive: "#189A4D",
    danger: "#E3474C",
    accent: "#4F5BF5",
    "attention-ink": "#9C4908",
    "alive-ink": "#0F6E36",
    "danger-ink": "#BD1B21",
    "accent-ink": "#3A44D6",
    "on-accent": "#FFFFFF",
    "tooltip-bg": "#25272B",
    "tooltip-fg": "#F6F7F8",
    "tooltip-muted": "#A5A8AD",
    "tooltip-border": "#3A3C40",
    "diff-add-bg": "#DCEFE4",
    "diff-del-bg": "#F3DFE8",
    "ansi-black": "#1F2124",
    "ansi-red": "#A33226",
    "ansi-green": "#2E6E4E",
    "ansi-yellow": "#7A5208",
    "ansi-blue": "#2450B8",
    "ansi-magenta": "#7A3E9D",
    "ansi-cyan": "#0E6B6E",
    "ansi-white": "#62656B",
    "ansi-bright-black": "#62656B",
    "ansi-bright-red": "#B3382C",
    "ansi-bright-green": "#35835F",
    "ansi-bright-yellow": "#8A5A0B",
    "ansi-bright-blue": "#2E5FD0",
    "ansi-bright-magenta": "#8E4EC6",
    "ansi-bright-cyan": "#0E7490",
    "ansi-bright-white": "#17181A",
    "shadow-color": "#00000011",
  }),
  dark: withTints({
    "surface-0": "#111214",
    "surface-canvas": "#16181C",
    "surface-1": "#1D2025",
    "surface-2": "#1D2025",
    "surface-inset": "#191C21",
    "hover-1": "#252931",
    "hover-2": "#303034",
    field: "#23272E",
    edge: "#333842",
    "edge-strong": "#4C535F",
    "ink-hi": "#F1F3F6",
    "ink-mid": "#B2B7C0",
    "ink-low": "#9AA1AC",
    attention: "#F68F3C",
    alive: "#3DBB72",
    danger: "#EE5C61",
    accent: "#8A96FF",
    "attention-ink": "#F68F3C",
    "alive-ink": "#3DBB72",
    "danger-ink": "#F17478",
    "accent-ink": "#96A1FF",
    "on-accent": "#111214",
    "tooltip-bg": "#111214",
    "tooltip-fg": "#F2F3F4",
    "tooltip-muted": "#A5A8AD",
    "tooltip-border": "#2E3033",
    "diff-add-bg": "#16261B",
    "diff-del-bg": "#261722",
    "ansi-black": "#5E6E80",
    "ansi-red": "#E06C75",
    "ansi-green": "#4FBF9A",
    "ansi-yellow": "#E8B04B",
    "ansi-blue": "#81B4E8",
    "ansi-magenta": "#A88FD8",
    "ansi-cyan": "#6BC4D4",
    "ansi-white": "#E5ECF4",
    "ansi-bright-black": "#71839A",
    "ansi-bright-red": "#EC858D",
    "ansi-bright-green": "#6CD3B1",
    "ansi-bright-yellow": "#F0C374",
    "ansi-bright-blue": "#9CC6EF",
    "ansi-bright-magenta": "#BFA9E6",
    "ansi-bright-cyan": "#8AD4E1",
    "ansi-bright-white": "#FFFFFF",
    "shadow-color": "#00000057",
  }),
};

const NATIVE_EXTRAS: Record<Scheme, NativeExtras> = {
  light: {
    "prose-ink": "#1C1C1E",
    "accent-fill": "#4F5BF5",
    "on-fill": "#FFFFFF",
  },
  dark: {
    "prose-ink": "#E3E3E8",
    "accent-fill": "#4F5BF5",
    "on-fill": "#FFFFFF",
  },
};

/** The full palette the phone aliases: the web's roles plus NativeExtras. */
export const palettes: Record<Scheme, ThemePalette> = {
  light: { ...webPalettes.light, ...NATIVE_EXTRAS.light },
  dark: { ...webPalettes.dark, ...NATIVE_EXTRAS.dark },
};
