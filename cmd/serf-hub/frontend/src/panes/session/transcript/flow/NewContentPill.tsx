// NewContentPill: the "↓ N new" affordance shown when the reader has
// scrolled up while items keep arriving. Badge carries the plain count
// (the widget named in the wave's own task scope); the needs-you upgrade
// swaps to a Chip reading "needs you" instead - Badge is strictly numeric
// (its one content prop is `count: number`), so a non-numeric replacement
// text structurally can't live inside it, and per the pinned contract
// (docs/web-ui/parity/contracts-transcript-scroll-liveness.md, §5) the
// needs-you state REPLACES the count entirely rather than showing both.
// Chip carries the exact same tone vocabulary and is equally allowlisted
// for --attention (src/styles/token-contract.test.ts) - reusing it here
// keeps the attention tone confined to this one pill (both its count and
// needs-you forms), never spilling into this file's own CSS.
import { Badge, Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./newcontentpill.module.css";

export interface NewContentPillProps {
  /** Items rendered since the reader scrolled away from the bottom. */
  count: number;
  /** Upgrades the pill to the needs-you tone/label (see useTranscriptScroll.ts). */
  needsYou: boolean;
  /** Scrolls to bottom and clears the pill (also fires on a manual return to bottom, independently). */
  onClick: () => void;
}

const CLASS = {
  pill: requireClass(styles.pill, "newcontentpill.module.css", "pill"),
  arrow: requireClass(styles.arrow, "newcontentpill.module.css", "arrow"),
};

export function NewContentPill({ count, needsYou, onClick }: NewContentPillProps) {
  if (count <= 0) return null;

  return (
    <button type="button" data-testid="new-content-pill" className={CLASS.pill} onClick={onClick}>
      <span className={CLASS.arrow} aria-hidden="true">
        ↓
      </span>
      {needsYou ? (
        <Chip tone="attention">needs you</Chip>
      ) : (
        <>
          <Badge count={count} tone="neutral" />
          <span>new</span>
        </>
      )}
    </button>
  );
}
