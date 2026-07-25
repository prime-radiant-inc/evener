import { requireClass } from "../internal/requireClass";
import styles from "./failureglyph.module.css";

const CLASS = {
  glyph: requireClass(styles.glyph, "failureglyph.module.css", "glyph"),
};

/** The ✗ that marks something as failed: a --danger cross, sized to sit on a
 * line of text. It exists as a widget (rather than a stylesheet rule wherever a
 * failure is marked) because --danger may only be referenced from an
 * allowlisted widget stylesheet - see src/styles/token-contract.test.ts. Carries
 * its own accessible name, since it is often the only failure signal on a row
 * and nothing else labels it. Passive: no interaction, no focus ring. */
export function FailureGlyph() {
  return (
    <span role="img" aria-label="Failed" className={CLASS.glyph} data-testid="failure-glyph">
      <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
        <path d="M1.5 1.5 L8.5 8.5 M8.5 1.5 L1.5 8.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
      </svg>
    </span>
  );
}
