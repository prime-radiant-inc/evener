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
//
// The error variant (same §5, lines 113-114) is a THIRD tone on the same
// Chip, danger this time - precedence (error > needs-you > plain count) is
// resolved right here, as a plain prop-driven if/else-if: this stays ONE
// component with three renderings, not a second component (useTranscriptScroll.ts
// exposes pillError/pillNeedsYou/pillCount as independent values for exactly
// this reason). Legacy rendered a dedicated SVG icon for this pill's tones
// (parity §16); this codebase's OWN needs-you branch above already dropped
// that in favor of tone-only + copy (no icon anywhere in this file), so the
// error variant matches that established sibling precedent rather than
// reintroducing an icon convention nothing else here uses. Copy modernized
// from legacy's bare "error" to sentence-case "Failed turn" (more legible
// as a phrase, consistent with WarningItem's own sentence-case fallback
// "Warning") - the count itself is never shown once upgraded, same as the
// needs-you branch.
import { Badge, Chevron, Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./newcontentpill.module.css";

export interface NewContentPillProps {
  /** Items rendered since the reader scrolled away from the bottom. */
  count: number;
  /** Upgrades the pill to the needs-you tone/label (see useTranscriptScroll.ts). */
  needsYou: boolean;
  /** Upgrades the pill to the danger tone/label - a failed turn the reader
   * hasn't seen yet (see useTranscriptScroll.ts's error anchor). Outranks
   * needsYou when both are true. Defaults to false so an existing caller
   * that only knows about count/needsYou is unaffected. */
  error?: boolean;
  /** Direction for the chevron arrow. Defaults to "down" to point toward new
   * content below the viewport. Set to "up" when the anchor (failed turn)
   * being jumped to is above the current scroll position. */
  pillArrowDirection?: "up" | "down";
  /** Scrolls to bottom and clears the pill (also fires on a manual return to bottom, independently). */
  onClick: () => void;
}

const CLASS = {
  pill: requireClass(styles.pill, "newcontentpill.module.css", "pill"),
  arrow: requireClass(styles.arrow, "newcontentpill.module.css", "arrow"),
};

export function NewContentPill({
  count,
  needsYou,
  error = false,
  pillArrowDirection = "down",
  onClick,
}: NewContentPillProps) {
  // count alone is not the render gate: a turn's failed settle stamp on
  // the real wire carries no items (turn/completed's EventError path -
  // itemsView:"", no items array), so a genuinely unseen failure can
  // arrive with count still 0 - the failure is itself the news, not the
  // (absent) item count. needsYou has no equivalent gap: the hook only
  // ever sets it alongside a nonzero pillCount (see pillNeedsYou's own
  // definition), so needsYou-without-count never reaches here for real.
  if (count <= 0 && !error) return null;

  return (
    <button type="button" data-testid="new-content-pill" className={CLASS.pill} onClick={onClick}>
      <span className={CLASS.arrow} aria-hidden="true">
        <Chevron direction={pillArrowDirection} />
      </span>
      {error ? (
        <Chip tone="danger">Failed turn</Chip>
      ) : needsYou ? (
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
