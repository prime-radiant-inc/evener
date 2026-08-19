// SeenDivider: the "you left off here" marker useSeenDivider.ts positions
// in the transcript. A hairline rule can't carry this on its own - --edge
// measures 1.26:1 against --surface-1 in dark (1.35:1 light), both far
// under anything that could read as meaningful on its own - so this reuses
// NewContentPill's already-established vocabulary instead: a labelled
// pill/chip, not a line. Deliberately passive (no button, no onClick): it
// names a fixed point in history rather than offering an action, unlike
// NewContentPill's own jump-to-bottom affordance.
import { Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./seendivider.module.css";

const CLASS = {
  row: requireClass(styles.row, "seendivider.module.css", "row"),
};

export function SeenDivider() {
  return (
    <div className={CLASS.row} data-testid="seen-divider">
      <Chip tone="neutral">New since your last visit</Chip>
    </div>
  );
}
