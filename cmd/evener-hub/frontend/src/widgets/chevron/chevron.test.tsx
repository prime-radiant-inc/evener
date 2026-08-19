import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { Chevron } from ".";

afterEach(cleanup);

function svgOf(container: HTMLElement): SVGSVGElement {
  const svg = container.querySelector("svg");
  if (!svg) throw new Error("Chevron rendered no svg");
  return svg;
}

// The load-bearing property, and the reason this widget exists at all.
//
// Every disclosure in the app turns its chevron with `transform:
// rotate(90deg)`. A transform does not change layout, but it DOES change the
// painted box - and a chevron whose box is taller than it is wide gets WIDER
// when turned. The app's old text glyph (`▸`, 12px font in an 18px line box)
// measured 6x18: turned, it painted 18px wide and escaped its row by 6px on
// each side. In the transcript, whose scroll containers declare `overflow-y:
// auto` (which computes overflow-x to `auto`, not `visible`), that escape
// became a horizontal scrollbar across the whole pane, clipping the left edge
// of every line above it.
//
// A square box is the fix that holds no matter what any consumer rotates it
// by, so it is asserted here rather than left to each caller.
test("the icon box is square, so rotating it cannot change its painted width", () => {
  const { container } = render(<Chevron />);
  const svg = svgOf(container);
  expect(svg.getAttribute("width")).toBe(svg.getAttribute("height"));
  expect(svg.getAttribute("viewBox")).toBe("0 0 16 16");
});

test("size sets both dimensions, keeping the box square at any size", () => {
  const { container } = render(<Chevron size={24} />);
  const svg = svgOf(container);
  expect(svg.getAttribute("width")).toBe("24");
  expect(svg.getAttribute("height")).toBe("24");
});

// display:block, not the inline default: an inline SVG sits on the text
// baseline inside a line box taller than itself, which reintroduces exactly
// the non-square painted box the square viewBox was chosen to avoid.
test("the svg is a block box, so no line-height gap pads it back out of square", () => {
  const { container } = render(<Chevron />);
  expect(svgOf(container).style.display).toBe("block");
});

test("direction draws a different path rather than transforming the box", () => {
  const right = render(<Chevron direction="right" />)
    .container.querySelector("path")
    ?.getAttribute("d");
  cleanup();
  const down = render(<Chevron direction="down" />)
    .container.querySelector("path")
    ?.getAttribute("d");
  cleanup();
  const left = render(<Chevron direction="left" />)
    .container.querySelector("path")
    ?.getAttribute("d");
  cleanup();
  const up = render(<Chevron direction="up" />)
    .container.querySelector("path")
    ?.getAttribute("d");
  expect(new Set([right, down, left, up]).size).toBe(4);
  for (const d of [right, down, left, up]) expect(d).toBeTruthy();
});

// A chevron is decoration beside a control that already carries its own
// accessible name (a <summary>, a button with an aria-expanded). Exposing it
// would make a screen reader announce the same state twice.
test("the icon is decorative: aria-hidden, never focusable, no title", () => {
  const { container } = render(<Chevron />);
  const svg = svgOf(container);
  expect(svg.getAttribute("aria-hidden")).toBe("true");
  expect(svg.getAttribute("focusable")).toBe("false");
  expect(svg.querySelector("title")).toBeNull();
});

// currentColor everywhere, so a consumer's own ink token governs the glyph
// and no colour literal is needed outside tokens.css (styles/token-contract).
test("the stroke inherits the consumer's ink rather than naming a colour", () => {
  const { container } = render(<Chevron />);
  const path = container.querySelector("path");
  expect(path?.getAttribute("stroke")).toBe("currentColor");
  expect(path?.getAttribute("fill")).toBe("none");
});
