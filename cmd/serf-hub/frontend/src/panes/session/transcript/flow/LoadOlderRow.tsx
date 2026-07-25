// LoadOlderRow: an always-available, quiet manual affordance for paging in
// older turns - the accessible complement to useTranscriptScroll's own
// scroll-near-top auto-trigger (matching the legacy renderer's isNearTop
// threshold exactly; see that hook's own doc comment). A purely scroll-
// triggered mechanism has no way to be reached without scrolling pixel-
// precisely near the very top, which is a real gap for keyboard/screen-
// reader use - this row calls the exact same loadOlder(), so an overlapping
// scroll-trigger and a click collapse to one fetch via loadOlder's own
// re-entrancy guard (useTranscript.ts), not any de-dupe of this row's own.
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./loadolderrow.module.css";

export interface LoadOlderRowProps {
  onClick: () => void;
  loading: boolean;
}

const CLASS = {
  row: requireClass(styles.row, "loadolderrow.module.css", "row"),
};

export function LoadOlderRow({ onClick, loading }: LoadOlderRowProps) {
  return (
    <button type="button" data-testid="load-older-row" className={CLASS.row} onClick={onClick} disabled={loading}>
      {loading ? "Loading older turns…" : "Load older"}
    </button>
  );
}
