import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render } from "@testing-library/react";
import { expect, test } from "vitest";
import { DEFAULT_SIZE, SpeakerAvatar } from ".";

function tileOf(container: HTMLElement): HTMLElement {
  const tile = container.querySelector("[data-testid='speaker-avatar']");
  if (!tile) throw new Error("SpeakerAvatar rendered no tile");
  return tile as HTMLElement;
}

// jsdom resolves no stylesheet, so the token contract is asserted at the
// declaration level, straight off disk. Comments are stripped FIRST: a
// stylesheet grep that matches its own comment prose asserts nothing (this
// repo has that precedent - see toolRowGrammar.test.tsx's rowCss()).
function tileCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "speakeravatar.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

// Same declaration-level read for the transcript's geometry: the speaker
// custom properties live in tokens.css (single-sourced there because the
// transcript surfaces are sibling render trees). Comments stripped first,
// same as tileCss().
function turnCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "../../styles/tokens.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

function geometryVar(css: string, name: string): number {
  const match = new RegExp(`${name}:\\s*(\\d+)px`).exec(css);
  if (!match) throw new Error(`tokens.css declares no ${name}`);
  return Number(match[1]!);
}

// The load-bearing property: each speaker draws a different glyph, so the
// tile actually answers "whose turn is this". Distinctness here also guards
// against the two speakers silently falling back to one drawing.
test("user and agent render distinct glyph paths", () => {
  const user = render(<SpeakerAvatar speaker="user" />)
    .container.querySelector("path")
    ?.getAttribute("d");
  cleanup();
  const agent = render(<SpeakerAvatar speaker="agent" />)
    .container.querySelector("path")
    ?.getAttribute("d");
  expect(user).toBeTruthy();
  expect(agent).toBeTruthy();
  expect(user).not.toBe(agent);
});

// Equal sides by construction, the same guarantee widgets/chevron makes: the
// tile sits in a flex header row, and a non-square painted box is what once
// let a transcript glyph escape its row (see chevron's own header).
test("the tile is square at the default size", () => {
  const { container } = render(<SpeakerAvatar speaker="user" />);
  const tile = tileOf(container);
  expect(tile.style.width).toBe("24px");
  expect(tile.style.height).toBe("24px");
});

test("size sets both dimensions, keeping the tile square at any size", () => {
  const { container } = render(<SpeakerAvatar speaker="agent" size={32} />);
  const tile = tileOf(container);
  expect(tile.style.width).toBe("32px");
  expect(tile.style.height).toBe("32px");
});

// The glyph scales with the tile so a resized avatar stays proportioned:
// 24px -> 14px (the app's standard icon box), any other size keeps the same
// rounded 14/24 ratio.
test("the glyph scales with the tile: 24px tile draws a 14px glyph", () => {
  const { container } = render(<SpeakerAvatar speaker="user" />);
  const svg = tileOf(container).querySelector("svg");
  expect(svg?.getAttribute("width")).toBe("14");
  expect(svg?.getAttribute("height")).toBe("14");
});

test("the glyph keeps the tile's ratio at a custom size", () => {
  const { container } = render(<SpeakerAvatar speaker="user" size={32} />);
  const svg = tileOf(container).querySelector("svg");
  const expected = String(Math.round(32 * 0.583));
  expect(svg?.getAttribute("width")).toBe(expected);
  expect(svg?.getAttribute("height")).toBe(expected);
});

// The header row beside the tile already names the speaker in words, so the
// tile is decoration - exposing it would announce the same fact twice.
test("the tile is decorative: aria-hidden and no title", () => {
  const { container } = render(<SpeakerAvatar speaker="user" />);
  const tile = tileOf(container);
  expect(tile.getAttribute("aria-hidden")).toBe("true");
  expect(tile.querySelector("title")).toBeNull();
});

// Declaration-level token contract: the tile's edge, fill, glyph ink and
// corner radius are the design tokens, never literals - in the light theme
// the --edge border is what separates the tile from the page (see the
// widget's header), so a literal background would silently drop that. The
// pill radius on equal sides is what makes the tile a circle.
test("the tile uses the edge/surface/ink/radius tokens, not literals", () => {
  const rule = /\.tile\s*\{([^}]*)\}/.exec(tileCss());
  expect(rule).not.toBeNull();
  const declarations = rule![1]!;
  expect(declarations).toMatch(/border:\s*1px solid var\(--edge\)/);
  expect(declarations).toMatch(/background:\s*var\(--surface-2\)/);
  expect(declarations).toMatch(/color:\s*var\(--ink-mid\)/);
  expect(declarations).toMatch(/border-radius:\s*var\(--radius-pill\)/);
});

// The agent's mark is the inverse of the page: its bars are drawn in the page
// ground on an ink tile, so the pair keeps the page's own text contrast in
// both themes.
test("the agent tile inverts the page: ink fill, page-ground glyph", () => {
  const rule = /\.agent\s*\{([^}]*)\}/.exec(tileCss());
  expect(rule).not.toBeNull();
  const declarations = rule![1]!;
  expect(declarations).toMatch(/background:\s*var\(--ink-hi\)/);
  expect(declarations).toMatch(/color:\s*var\(--surface-0\)/);
});

test("only the agent's tile carries the inverted treatment", () => {
  const user = tileOf(render(<SpeakerAvatar speaker="user" />).container).className;
  cleanup();
  const agent = tileOf(render(<SpeakerAvatar speaker="agent" />).container).className;
  expect(agent).not.toBe(user);
  expect(agent.startsWith(user)).toBe(true);
});

// The drift pin. The transcript's gutter geometry is single-sourced on .turn
// in tokens.css: --speaker-avatar-size (the tile edge) +
// --speaker-gap (the header row's gap) = --speaker-gutter (the content
// column indent). This widget's DEFAULT_SIZE is the avatar half of that
// arithmetic, so it is pinned equal to --speaker-avatar-size here: if anyone
// changes the transcript's gutter geometry, the avatar tile default must
// change with it (or the change must be deliberate enough to update this
// pin).
test("the transcript's gutter geometry matches the widget default", () => {
  const css = turnCss();
  const avatarSize = geometryVar(css, "--speaker-avatar-size");
  const gap = geometryVar(css, "--speaker-gap");
  const gutter = geometryVar(css, "--speaker-gutter");
  expect(avatarSize).toBe(24);
  expect(gap).toBe(10);
  expect(gutter).toBe(34);
  expect(avatarSize).toBe(DEFAULT_SIZE);
  expect(avatarSize + gap).toBe(gutter);
});
