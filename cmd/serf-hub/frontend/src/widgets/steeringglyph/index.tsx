import { requireClass } from "../internal/requireClass";
import styles from "./steeringglyph.module.css";

const CLASS = {
  glyph: requireClass(styles.glyph, "steeringglyph.module.css", "glyph"),
};

/** The ※ that marks a system steering message: the reference mark, drawn as a
 * four-spark asterisk over a slash, sized to sit on a line of text.
 *
 * SVG rather than the U+203B character because global.css subsets IBM Plex Sans
 * to a unicode-range that stops at U+203A and resumes at U+2044 - a literal ※
 * would be the one glyph in the app rendering from a system fallback font.
 *
 * Decorative and unnamed, unlike FailureGlyph: the row's own text says
 * "System steered: <kind>", so naming the glyph would repeat it. Inherits
 * currentColor, so it needs no token-contract allowlist entry. */
export function SteeringGlyph() {
  return (
    <span aria-hidden="true" className={CLASS.glyph} data-testid="steering-glyph">
      <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
        <path
          d="M5 1.2 V4.4 M2.3 2.9 L7.7 6.1 M7.7 2.9 L2.3 6.1 M1.8 8.4 H8.2"
          stroke="currentColor"
          strokeWidth="1.1"
          strokeLinecap="round"
        />
      </svg>
    </span>
  );
}
