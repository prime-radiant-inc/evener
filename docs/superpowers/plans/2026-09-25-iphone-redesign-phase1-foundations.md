# iPhone redesign, Phase 1: Foundations — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The native iPhone app adopts the redesign's palette and type foundations: one tokens module feeding every existing screen through `useColors()`, filled buttons that pass contrast in both themes, and Source Serif 4 as the conversation's voice.

**Architecture:** A new `mobile-native/src/design/tokens.ts` holds the spec's palette (light and dark), tints, type roles and font names. `useColors()` in `mobile-native/src/ui.tsx` keeps its existing keys (69 call sites use `background`, `surface`, `text`, `secondary`, `border`, `accent`, `error`, `warning`, `onAccent`) but reads their values from the tokens, and gains a `palette` field for new code. Source Serif 4 is embedded at build time with the `expo-font` config plugin from `@expo-google-fonts/source-serif-4`.

**Tech Stack:** Expo SDK 57, React Native 0.86.3, React 19.2.3, TypeScript 6, vitest 5 with react-test-renderer (`src/renderNative.testkit.tsx`), CocoaPods 1.16.2 via Bundler 2.7.2 on Ruby 3.3.6.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md` (sections 8.2, 16.1, 16.2, 16.3). Roadmap: `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`.

## Global Constraints

- Colors are exactly the spec's table (16.1): page #FAF9F6/#191918, canvas #F1F0EB/#1D1D1B, surface #FCFBF8/#232320, inset #F4F3EE/#20201E, pressed #F0EFE9/#2B2B28, edge #DDDCD4/#34342F, edge-strong #B7B6AC/#51514A, ink-hi #252521/#F2F1EB, ink-mid #5F5F57/#B0AFA6, ink-low #6D6D64/#99998F, prose = ink-hi/#E0DED6, attention #F59E0B/#F68F3C with attention-ink #AD5209/#F68F3C, alive #189A4D/#3DBB72 with alive-ink #12763B/#3DBB72, danger #E3474C/#EE5C61 with danger-ink #C51D23/#F17478, accent #0285FF/#3D9AFF with accent-ink #0064C2/#459EFF, accent-fill #0070E0 in both, diff add #E9F4EE/#19251A, diff delete #F5EAF0/#170B17.
- Tints: each hue's `-bg` is the hue at 15% over surface; `-edge` is the hue at 40% over edge, both mixed in OKLab as the web's `color-mix(in oklab, …)` does. Your message bubble is accent-bg in both themes.
- "No other hues exist in the app." Amber appears only where a human is needed.
- Text on a fill uses accent-fill (#0070E0) with white: 4.8:1 in both themes. Accent-colored text uses accent-ink.
- Type: SF Pro (the system font) for everything you operate; Source Serif 4 for words someone wrote (agent prose 17/26, your messages 17/25, documents 18/28); the app's existing machine face, Menlo, for paths, commands and code. Agent-message headings are SF Pro semibold 20/17/15.
- 44pt minimum touch targets. iPhone only (Android and iPad are deferred; keep them buildable, don't qualify them).
- Never change storage keys, route params that feed them (`hubId`, `ref`), or transcript row identity.

## Review Focus

1. **Dark mode on every changed surface.** A person in dark mode expects every screen to switch together. The markdown code colors today detect dark mode by comparing `colors.background` to the old `#121417`; after this phase that comparison is always false. Pinned by Task 2, Step 1 (`MarkdownResponse` picks code colors by scheme).
2. **Dynamic Type.** Text scales with `fontScale` on iOS through the existing `textScale` multiplication; the new prose style must scale font size and line height the same way. Pinned by Task 4, Step 1.
3. **A wrong font name fails silently.** iOS falls back to the system font without an error when `fontFamily` names no embedded face, so the app would look "done" and not be. Pinned by Task 3, Step 1 (the PostScript names are read from the shipped TTF files).
4. **CocoaPods lock drift.** Making `expo-font` a direct dependency moves its pod path; the TestFlight workflow runs `pod install --deployment`, which refuses a stale lock. Pinned by Task 3, Step 7.
5. **Existing tests that assert copy.** Fifteen suites assert rendered text. This phase changes no copy; the whole native suite must stay green. Checked at the end of each task.

---

## PR 1: tokens behind `useColors()` (Tasks 1-2)

### Task 1: The tokens module

**Files:**
- Create: `mobile-native/src/design/tokens.ts`
- Test: `mobile-native/src/design/tokens.test.ts`

**Interfaces:**
- Produces: `type Scheme = "light" | "dark"`, `interface Palette` (fields below), `palettes: Record<Scheme, Palette>`, `paletteFor(scheme: string | null | undefined): Palette`, `mix(hue: string, base: string, amount: number): string`, `fonts: { serif: string; serifItalic: string; serifSemibold: string; mono: string }`, `type` roles (`typeRoles`) used by Task 4.

- [ ] **Step 1: Write the failing test**

```ts
// mobile-native/src/design/tokens.test.ts
import { describe, expect, it } from "vitest";
import { fonts, mix, paletteFor, palettes, typeRoles } from "./tokens";

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

describe("type", () => {
	it("names the embedded Source Serif 4 faces and the app's machine face", () => {
		expect(fonts).toEqual({
			serif: "SourceSerif4-Regular",
			serifItalic: "SourceSerif4-Italic",
			serifSemibold: "SourceSerif4-SemiBold",
			mono: "Menlo",
		});
	});
	it("sets the spec's reading sizes", () => {
		expect(typeRoles.agentProse).toEqual({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 26 });
		expect(typeRoles.yourMessage).toEqual({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 25 });
		expect(typeRoles.document).toEqual({ fontFamily: "SourceSerif4-Regular", fontSize: 18, lineHeight: 28 });
		expect(typeRoles.machine).toEqual({ fontFamily: "Menlo", fontSize: 13, lineHeight: 18 });
	});
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd mobile-native && npx vitest run src/design/tokens.test.ts`
Expected: FAIL, "Failed to resolve import "./tokens"".

- [ ] **Step 3: Write the implementation**

```ts
// mobile-native/src/design/tokens.ts
//
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd mobile-native && npx vitest run src/design/tokens.test.ts`
Expected: PASS (all cases).

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/design/tokens.ts mobile-native/src/design/tokens.test.ts
git commit -m "feat(native): design tokens from the redesign spec"
```

### Task 2: Every screen reads the tokens through `useColors()`

**Files:**
- Modify: `mobile-native/src/ui.tsx` (`useColors`, `Action`'s primary fill)
- Modify: `mobile-native/src/MarkdownResponse.tsx:131-132` (code colors by scheme)
- Test: `mobile-native/src/ui.test.tsx` (create), `mobile-native/src/MarkdownResponse.test.tsx` (create)

**Interfaces:**
- Consumes: `paletteFor`, `Palette` from Task 1.
- Produces: `useColors(): LegacyColors & { palette: Palette }` where `LegacyColors` keeps the existing keys. Mapping: `background = page`, `surface = inset`, `text = inkHi`, `secondary = inkMid`, `border = edge`, `accent = accentInk`, `error = dangerInk`, `warning = attentionInk`, `onAccent = onFill`. Filled buttons use `palette.accentFill`.

- [ ] **Step 1: Write the failing tests**

```tsx
// mobile-native/src/ui.test.tsx
import { expect, it, vi } from "vitest";
import { Action, useColors } from "./ui";
import { render, renderHook } from "./renderNative.testkit";

// vi.mock is hoisted above everything else, so the scheme it reads lives in
// vi.hoisted state rather than a plain top-level variable.
const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	useColorScheme: () => mode.scheme,
}));

it("maps the existing color keys onto the spec's light palette", () => {
	mode.scheme = "light";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors).toMatchObject({
		background: "#FAF9F6", surface: "#F4F3EE", text: "#252521", secondary: "#5F5F57",
		border: "#DDDCD4", accent: "#0064C2", error: "#C51D23", warning: "#AD5209", onAccent: "#FFFFFF",
	});
	expect(colors.palette.scheme).toBe("light");
});

it("maps the existing color keys onto the spec's dark palette", () => {
	mode.scheme = "dark";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors).toMatchObject({
		background: "#191918", surface: "#20201E", text: "#F2F1EB", secondary: "#B0AFA6",
		border: "#34342F", accent: "#459EFF", error: "#F17478", warning: "#F68F3C", onAccent: "#FFFFFF",
	});
});

it("fills a primary action with accent-fill so white text passes contrast in dark mode", () => {
	mode.scheme = "dark";
	const tree = render(<Action tone="primary" onPress={() => {}}>Send</Action>);
	const pressable = tree.root.findByProps({ accessibilityLabel: "Send" });
	const style = pressable.props.style({ pressed: false });
	expect(style).toEqual(expect.arrayContaining([expect.objectContaining({ backgroundColor: "#0070E0" })]));
});
```

`renderHook` and `render` come from `mobile-native/src/renderNative.testkit.tsx`: `renderHook(hook)` returns `{ result: { current }, rerender, unmount }` and `render(element)` returns the react-test-renderer tree.

```tsx
// mobile-native/src/MarkdownResponse.test.tsx
import { expect, it, vi } from "vitest";
import { MarkdownResponse } from "./MarkdownResponse";
import { render } from "./renderNative.testkit";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: () => {} },
	Linking: { openURL: async () => {} },
	useColorScheme: () => mode.scheme,
}));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));

function markdownStyle(markdown: string) {
	const tree = render(<MarkdownResponse markdown={markdown} />);
	return tree.root.findByType("EnrichedMarkdownText" as never).props.markdownStyle;
}

it("picks light code colors in light mode", () => {
	mode.scheme = "light";
	expect(markdownStyle("`x`").codeBlock.syntaxColors).toMatchObject({ string: "#2e6443", number: "#785119" });
});

it("picks dark code colors in dark mode", () => {
	mode.scheme = "dark";
	expect(markdownStyle("`x`").codeBlock.syntaxColors).toMatchObject({ string: "#b8d8a3", number: "#ecc48d" });
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd mobile-native && npx vitest run src/ui.test.tsx src/MarkdownResponse.test.tsx`
Expected: FAIL: the light test sees `#fafaf8` for `background`; the dark code-color test gets the light colors, because `colors.background` is no longer `#121417`.

- [ ] **Step 3: Implement**

In `mobile-native/src/ui.tsx`, replace `useColors` with:

```tsx
import { paletteFor } from "./design/tokens";

/** The app's colors: the redesign palette (src/design/tokens.ts) under the
 * keys every existing screen already reads, plus the full palette for new
 * code. */
export function useColors() {
	const palette = paletteFor(useColorScheme());
	return {
		background: palette.page,
		surface: palette.inset,
		text: palette.inkHi,
		secondary: palette.inkMid,
		border: palette.edge,
		accent: palette.accentInk,
		error: palette.dangerInk,
		warning: palette.attentionInk,
		onAccent: palette.onFill,
		palette,
	};
}
```

In `Action`, change the primary fill from `backgroundColor: colors.accent` to `backgroundColor: colors.palette.accentFill` (the label keeps `colors.onAccent`).

In `mobile-native/src/MarkdownResponse.tsx`, read the scheme from the palette instead of comparing a hex:

```tsx
          string: colors.palette.scheme === "dark" ? "#b8d8a3" : "#2e6443",
          number: colors.palette.scheme === "dark" ? "#ecc48d" : "#785119",
```

and add `colors.palette.scheme` to the `useMemo` dependency list.

- [ ] **Step 4: Run the tests, the type check and the whole native suite**

Run: `cd mobile-native && npx vitest run src/ui.test.tsx src/MarkdownResponse.test.tsx && npm run check && npm test`
Expected: all PASS. No existing suite asserts a color, so nothing else changes; if one does, it asserted an old hex and is updated to the token in this task.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/ui.tsx mobile-native/src/ui.test.tsx mobile-native/src/MarkdownResponse.tsx mobile-native/src/MarkdownResponse.test.tsx
git commit -m "feat(native): every screen reads the redesign palette through useColors"
```

- [ ] **Step 6: Open PR 1**

Push the branch, open a regular PR titled "feat(native): redesign palette behind useColors (phase 1, PR 1)" whose body lists the key mapping above and links the spec and roadmap. Attach before/after simulator screenshots of the Sessions and Conversation screens in both themes. Land per the roadmap's rules (RoboRev, /simplify, admin squash merge).

---

## PR 2: Source Serif 4 for the conversation (Tasks 3-4)

### Task 3: Embed Source Serif 4

**Files:**
- Modify: `mobile-native/package.json`, `mobile-native/package-lock.json` (dependencies)
- Modify: `mobile-native/app.json` (expo-font plugin)
- Modify: `mobile-native/Podfile.lock` (regenerated, never hand-edited)
- Test: `mobile-native/src/design/fonts.test.ts` (create)

**Interfaces:**
- Consumes: `fonts` from Task 1.
- Produces: the three faces available to iOS by PostScript name.

- [ ] **Step 1: Write the failing test**

```ts
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd mobile-native && npx vitest run src/design/fonts.test.ts`
Expected: FAIL, "app.json has no expo-font plugin".

- [ ] **Step 3: Add the dependencies**

Read https://docs.expo.dev/versions/v57.0.0/sdk/font/ first (mobile-native/AGENTS.md requires the versioned docs). Then, from `mobile-native` (only if `node_modules` is a real directory, never through a symlink):

```bash
npx expo install expo-font
npm install --save-exact @expo-google-fonts/source-serif-4@0.4.1
```

Expected: `expo-font` appears in `dependencies` at a `~57.0.x` range chosen by `expo install`, and `@expo-google-fonts/source-serif-4` at exactly `0.4.1` (its fonts are OFL-1.1, see `node_modules/@expo-google-fonts/source-serif-4/LICENSE_FONT`).

- [ ] **Step 4: Embed the three faces**

Add to `app.json`'s `expo.plugins` array, after `"expo-sqlite"`:

```json
[
  "expo-font",
  {
    "fonts": [
      "./node_modules/@expo-google-fonts/source-serif-4/400Regular/SourceSerif4_400Regular.ttf",
      "./node_modules/@expo-google-fonts/source-serif-4/400Regular_Italic/SourceSerif4_400Regular_Italic.ttf",
      "./node_modules/@expo-google-fonts/source-serif-4/600SemiBold/SourceSerif4_600SemiBold.ttf"
    ]
  }
]
```

The test's `existsSync(`${root}${f}`)` resolves `./node_modules/...` against `mobile-native/`.

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd mobile-native && npx vitest run src/design/fonts.test.ts`
Expected: PASS: the names read from the files are `SourceSerif4-Regular`, `SourceSerif4-Italic`, `SourceSerif4-SemiBold`.

- [ ] **Step 6: Prove Metro still bundles**

Run: `make test-native-bundle`
Expected: PASS ("export writes an iOS bundle").

- [ ] **Step 7: Regenerate the CocoaPods lock the documented way**

The Gemfile pins Ruby 3.3.6 and Bundler 2.7.2. If `rbenv versions` lacks 3.3.6, run `rbenv install 3.3.6`, then from `mobile-native`:

```bash
rbenv shell 3.3.6
gem install bundler:2.7.2
bundle _2.7.2_ install
npx expo prebuild --platform ios --no-install
cp Podfile.lock ios/Podfile.lock
bundle _2.7.2_ exec pod install --project-directory=ios
cp ios/Podfile.lock Podfile.lock
git diff --stat Podfile.lock
bundle _2.7.2_ exec pod install --deployment --project-directory=ios
```

Expected: the diff moves `ExpoFont` from `../node_modules/expo/node_modules/expo-font/ios` to `../node_modules/expo-font/ios` and changes nothing unrelated; the final `--deployment` install succeeds. If anything else in the lock changes, stop and diagnose before committing (docs/design/mobile/ios-build-distribution.md: "a failed deployment install requires diagnosis rather than silently regenerating it"). `ios/` stays untracked.

- [ ] **Step 8: Build and look**

Build the Evener scheme in Release for an iOS simulator (normal simulator signing) and confirm the app launches. The serif is not used until Task 4.

- [ ] **Step 9: Commit**

```bash
git add mobile-native/package.json mobile-native/package-lock.json mobile-native/app.json mobile-native/Podfile.lock mobile-native/src/design/fonts.test.ts
git commit -m "feat(native): embed Source Serif 4 for the conversation's voice"
```

### Task 4: Agent prose and your messages in the serif

**Files:**
- Modify: `mobile-native/src/MarkdownResponse.tsx` (body, lists, blockquotes in the serif; headings SF Pro semibold 20/17/15; code in Menlo)
- Modify: `mobile-native/src/ui.tsx` (`Copy` gains `variant?: "ui" | "yourMessage"`)
- Modify: `mobile-native/src/TimelineItem.tsx` (your message: `variant="yourMessage"`, bubble fill `colors.palette.bubble`)
- Test: `mobile-native/src/MarkdownResponse.test.tsx` (extend), `mobile-native/src/TimelineItem.test.tsx` (extend)

**Interfaces:**
- Consumes: `typeRoles`, `fonts` (Task 1), `useColors().palette` (Task 2).

- [ ] **Step 1: Write the failing tests**

Add to `mobile-native/src/MarkdownResponse.test.tsx`, reusing its `markdownStyle` helper:

```tsx
it("sets agent prose in Source Serif 4 at 17/26 and headings in the system font", () => {
	mode.scheme = "light";
	const s = markdownStyle("Hello");
	expect(s.paragraph).toMatchObject({ fontFamily: "SourceSerif4-Regular", fontSize: 17, lineHeight: 26, color: "#252521" });
	expect(s.list).toMatchObject({ fontFamily: "SourceSerif4-Regular" });
	expect(s.h1).toMatchObject({ fontSize: 20, fontWeight: "600" });
	expect(s.h1.fontFamily).toBeUndefined();
	expect(s.h2).toMatchObject({ fontSize: 17, fontWeight: "600" });
	expect(s.h3).toMatchObject({ fontSize: 15, fontWeight: "600" });
	expect(s.codeBlock).toMatchObject({ fontFamily: "Menlo" });
});

it("uses the dimmer prose ink in dark mode", () => {
	mode.scheme = "dark";
	expect(markdownStyle("Hello").paragraph).toMatchObject({ color: "#E0DED6" });
});
```

Add to `mobile-native/src/TimelineItem.test.tsx` a case that renders a `user` row (build it the way the file's existing user-row cases do) and asserts that the message text node's style has `fontFamily: "SourceSerif4-Regular"`, `fontSize: 17`, `lineHeight: 25`, and that the bubble view's `backgroundColor` is `#DDEBFC` in light mode. The text itself and its accessibility label ("You: …") must not change.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd mobile-native && npx vitest run src/MarkdownResponse.test.tsx src/TimelineItem.test.tsx`
Expected: FAIL: no `fontFamily` on paragraphs, headings at 25/22/19, the bubble on `#F4F3EE`.

- [ ] **Step 3: Implement**

In `MarkdownResponse.tsx` build `body` from `typeRoles.agentProse` (keep the existing iOS-only size logic out: the spec's sizes are iOS sizes, and the component already relies on `allowFontScaling`), set `color: colors.palette.prose`, spread `fontFamily: fonts.serif` into `list` and `blockquote`, set headings to `{ fontWeight: "600", color: colors.text }` with `h1` 20/26, `h2` 17/24, `h3` 15/21 and `h4`-`h6` 15/21, and add `fontFamily: fonts.mono` to `code` and `codeBlock`. Add `colors.palette.prose` to the memo's dependencies.

In `ui.tsx`, give `Copy` a `variant` prop: `"ui"` (default, today's behavior) or `"yourMessage"`, which uses `typeRoles.yourMessage.fontFamily`, `fontSize: 17 * textScale`, `lineHeight: 25 * textScale` and `color: colors.palette.prose`. Muted text is unaffected.

In `TimelineItem.tsx`, render the user text with `<Copy variant="yourMessage" label={`You: ${item.text}`}>` and set the user bubble's `backgroundColor` to `colors.palette.bubble` (radius, padding and margin unchanged).

- [ ] **Step 4: Run the tests, the type check and the native suite**

Run: `cd mobile-native && npx vitest run src/MarkdownResponse.test.tsx src/TimelineItem.test.tsx && npm run check && npm test`
Expected: all PASS.

- [ ] **Step 5: Look at it**

Rebuild the Release simulator app. Open a conversation with headings, lists, code and your own messages, in light and dark. Agent prose and your messages are in Source Serif 4; headings are SF Pro semibold; code is Menlo. Take screenshots for the PR.

- [ ] **Step 6: Commit and open PR 2**

```bash
git add mobile-native/src/MarkdownResponse.tsx mobile-native/src/MarkdownResponse.test.tsx mobile-native/src/ui.tsx mobile-native/src/TimelineItem.tsx mobile-native/src/TimelineItem.test.tsx
git commit -m "feat(native): the conversation in Source Serif 4"
```

Open PR 2 ("feat(native): Source Serif 4 for the conversation (phase 1, PR 2)") with the screenshots and the Podfile.lock diff explained. Land per the roadmap's rules.

---

## Self-review

- Spec coverage: 16.1 palette and tints (Tasks 1-2), 16.2 type roles for prose, your messages and machine text (Tasks 1, 3, 4), 8.2 agent message and your message rendering (Task 4), the accent-fill contrast rule (Task 2). The Board, marks, pulse meter and SF Symbols are Phase 2 (they land with their first screen, so nothing ships unused).
- Placeholders: none; each code step has its code, and the test-kit calls match `renderNative.testkit.tsx` as it is on main.
- Names: `paletteFor`, `palettes`, `mix`, `fonts`, `typeRoles`, `useColors().palette`, `Copy variant="yourMessage"` are used consistently.
- Review Focus: dark mode (Task 2 Step 1, Task 4 Step 1), Dynamic Type (Task 4's `Copy` variant multiplies by `textScale`; its test should also render with `fontScale: 1.5` if the kit's `useWindowDimensions` mock can be overridden, else note it in the PR), font names (Task 3 Step 1), pod lock (Task 3 Step 7), existing copy tests (each task's full-suite run).
