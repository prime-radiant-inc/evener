import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ToolIcon, type ToolIconKind } from ".";

afterEach(cleanup);

const ALL_KINDS: ToolIconKind[] = [
  "terminal",
  "file",
  "edit",
  "search",
  "folder",
  "globe",
  "ask",
  "tasks",
  "delegate",
  "transcript",
  "job",
  "send",
  "skill",
  "wrench",
  "thought",
];

function svgOf(container: HTMLElement): SVGSVGElement {
  const svg = container.querySelector("svg");
  if (!svg) throw new Error("ToolIcon rendered no svg");
  return svg;
}

// Same load-bearing property as widgets/chevron: a square box whose painted
// size can never surprise a wrapping flex row (the transcript's old text
// glyph's non-square line box is what once escaped a row into a pane-wide
// horizontal scrollbar - see chevron's own header for the full story).
test("the icon box is square at the default size", () => {
  const { container } = render(<ToolIcon kind="terminal" />);
  const svg = svgOf(container);
  expect(svg.getAttribute("width")).toBe(svg.getAttribute("height"));
  expect(svg.getAttribute("viewBox")).toBe("0 0 16 16");
});

test("size sets both dimensions, keeping the box square at any size", () => {
  const { container } = render(<ToolIcon kind="file" size={24} />);
  const svg = svgOf(container);
  expect(svg.getAttribute("width")).toBe("24");
  expect(svg.getAttribute("height")).toBe("24");
});

test("the svg is a block box, so no line-height gap pads it back out of square", () => {
  const { container } = render(<ToolIcon kind="search" />);
  expect(svgOf(container).style.display).toBe("block");
});

// Every kind must actually draw - a typo'd kind rendering nothing would fail
// silently in the transcript (the row would just have a gap).
test("every kind draws a distinct, non-empty path", () => {
  const paths = new Set<string>();
  for (const kind of ALL_KINDS) {
    const { container, unmount } = render(<ToolIcon kind={kind} />);
    const d = container.querySelector("path")?.getAttribute("d");
    expect(d, `kind ${kind} rendered no path`).toBeTruthy();
    paths.add(d as string);
    unmount();
  }
  expect(paths.size).toBe(ALL_KINDS.length);
});

// Decoration beside a row whose text already names the action - exposing the
// glyph would make a screen reader announce the same fact twice.
test("the icon is decorative: aria-hidden, never focusable, no title", () => {
  const { container } = render(<ToolIcon kind="wrench" />);
  const svg = svgOf(container);
  expect(svg.getAttribute("aria-hidden")).toBe("true");
  expect(svg.getAttribute("focusable")).toBe("false");
  expect(svg.querySelector("title")).toBeNull();
});

// Single-colour line art by construction: currentColor stroke, no fill, so a
// consumer's ink token governs the glyph and no colour literal is needed
// outside tokens.css (styles/token-contract).
test("every kind is single-colour line art: currentColor stroke, fill none", () => {
  for (const kind of ALL_KINDS) {
    const { container, unmount } = render(<ToolIcon kind={kind} />);
    const path = container.querySelector("path");
    expect(path?.getAttribute("stroke")).toBe("currentColor");
    expect(path?.getAttribute("fill")).toBe("none");
    unmount();
  }
});
