// The app's one chevron. Every disclosure triangle, tree twisty and dropdown
// caret draws from here.
//
// It replaces the text glyphs (`▸` / `▾`) these sites used to render, for two
// reasons - one visual, one structural.
//
// Visual: a typographic triangle is tiny, and how tiny is a property of
// whatever font happened to load. At the app's caption size `▸` painted about
// 6px across - a reviewer auditing a session for correctness had already
// flagged it as easy to miss when deciding whether a collapsed row had more to
// inspect (see toolcallitem.module.css's own note). This draws a stroked
// chevron on the app's 16x16 icon grid (the same grammar as CloseIcon,
// BackIcon and openbutton's OpenIcon), at a size that is a design decision rather
// than a font's.
//
// Structural: a text glyph's box is its line box - 6x18 at caption size, far
// taller than it is wide. The disclosure sites turn their chevron with
// `transform: rotate(90deg)`, and turning a 6x18 box paints it 18px wide,
// 6px past its own layout box on EACH side. Where that overhang met the right
// edge of the transcript's reading measure it escaped the row, and because
// the transcript's scroll containers declare `overflow-y: auto` (which
// computes overflow-x to `auto`, never `visible`) the escape became a
// horizontal scrollbar across the whole pane - clipping the first character of
// every line above it. A square box cannot do that at any rotation, so
// squareness is asserted in this widget's own test rather than trusted to
// each caller.
//
// Consumers that ANIMATE the turn (a disclosure opening) pass `open`; this
// widget sets `data-open` on the svg and owns the 90-degree rotation and its
// reduced-motion-gated transition in chevron.module.css. Callers no longer
// reach into `> svg` to turn it, and every rotation targets the same square
// element. Consumers with a fixed direction (a dropdown caret, a tree row's
// current state) pass `direction` and get a different PATH - no transform to
// collide with.

import { requireClass } from "../internal/requireClass";
import styles from "./chevron.module.css";

const CLASS = {
  chevron: requireClass(styles.chevron, "chevron.module.css", "chevron"),
};

export type ChevronDirection = "right" | "down" | "left" | "up";

// Drawn on a 16x16 grid, apex on the centre line, arms of equal length, so
// each direction is the same glyph seen from a different side and the four
// read as one family rather than four drawings.
const PATHS: Record<ChevronDirection, string> = {
  right: "M6 3.5 L10.5 8 L6 12.5",
  down: "M3.5 6 L8 10.5 L12.5 6",
  left: "M10 3.5 L5.5 8 L10 12.5",
  up: "M3.5 10 L8 5.5 L12.5 10",
};

export interface ChevronProps {
  direction?: ChevronDirection;
  /** Box edge in px. Square by construction - see this widget's own test. */
  size?: number;
  /** Whether the thing this chevron marks is open. Sets `data-open` on the
   * svg and turns it 90 degrees, so a disclosure's leading glyph learns the
   * state from the disclosure rather than from a caller descendant selector. */
  open?: boolean;
}

// 14px: the same edge CloseIcon and BackIcon use, so every icon in the app
// occupies one box size unless it has a reason not to.
const DEFAULT_SIZE = 14;

export function Chevron({ direction = "right", size = DEFAULT_SIZE, open }: ChevronProps = {}) {
  return (
    <svg
      className={CLASS.chevron}
      viewBox="0 0 16 16"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
      data-open={open === undefined ? undefined : open ? "true" : "false"}
      // Inline rather than a class: `display` here is correctness (an inline
      // SVG would sit in a line box taller than itself, undoing the square box
      // above), not styling a consumer should be able to override. The class
      // carries only the open rotation in chevron.module.css.
      style={{ display: "block" }}
    >
      <path
        d={PATHS[direction]}
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}
