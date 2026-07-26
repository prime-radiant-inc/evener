import { requireClass } from "../internal/requireClass";
import styles from "./steeringglyph.module.css";

const CLASS = {
  glyph: requireClass(styles.glyph, "steeringglyph.module.css", "glyph"),
};

/** The ◇ that marks a system steering message: a hollow diamond, sized to
 * sit on a line of text.
 *
 * SVG rather than the U+25C7 character because global.css subsets IBM Plex Sans
 * to a unicode-range that declares no U+25xx block at all - a literal ◇ would
 * be the one glyph in the app rendering from a system fallback font. A diamond
 * rather than a reference mark because steering rows and failure rows can appear
 * in the same gutter; FailureGlyph is ✗ and a reference mark ※ would become
 * indistinguishable from it in monochrome.
 *
 * Decorative and unnamed, unlike FailureGlyph: the row's own text says
 * "System steered: <kind>", so naming the glyph would repeat it. Inherits
 * currentColor, so it needs no token-contract allowlist entry. */
export function SteeringGlyph() {
  return (
    <span aria-hidden="true" className={CLASS.glyph} data-testid="steering-glyph">
      <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
        <path
          d="M5 1.1 L8.9 5 L5 8.9 L1.1 5 Z"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.3"
          strokeLinejoin="round"
        />
      </svg>
    </span>
  );
}
