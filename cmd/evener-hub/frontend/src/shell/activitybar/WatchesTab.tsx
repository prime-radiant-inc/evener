// The Watches tab: the scope's watches with the shared cadence/meta wording,
// plus the honest "+N more · M armed" line when the hub omitted rows (the
// rail's watchCountLabel grammar, so the two surfaces cannot disagree).

import { requireClass } from "../../widgets/internal/requireClass";
import { activeWatchCount, watchCountLabel } from "../rail/railNodes";
import type { ActivityScope } from "../statusbar/statusScope";
import styles from "./activitybar.module.css";
import { WatchRow } from "./activityRows";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
  passiveMore: requireClass(styles.passiveMore, "activitybar.module.css", "passiveMore"),
};

export function WatchesTab({ scope, now }: { scope: ActivityScope; now: number }) {
  const watches = scope.leaf.watches ?? [];
  const omitted = scope.leaf.omitted_watches ?? 0;
  if (watches.length === 0 && omitted === 0) {
    return <span className={CLASS.emptyNote}>No watches at this level.</span>;
  }
  return (
    <div className={CLASS.stack}>
      {watches.map((watch) => (
        <WatchRow key={watch.id} watch={watch} now={now} />
      ))}
      {omitted > 0 ? (
        // The rail's own grammar with the TRUE totals: the armed figure counts
        // retained armed rows AND the omitted armed subset, never just one side.
        <span className={CLASS.passiveMore}>
          {watchCountLabel(activeWatchCount(scope.leaf), watches.length, omitted)}
        </span>
      ) : null}
    </div>
  );
}
