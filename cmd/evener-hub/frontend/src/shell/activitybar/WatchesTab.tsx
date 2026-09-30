import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The Watches tab observes the shared session watch collection and admits
// continuation pages at its visible boundary. Cadence and delivery labels use
// the shared clock, which ticks only while this tab is mounted.

import { useNowTick } from "../../panes/session/liveness";
import { useSessionActivity } from "../../stores/sessionActivity";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ActivityScope } from "../statusbar/statusScope";
import styles from "./activitybar.module.css";
import { WatchRow } from "./activityRows";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
  passiveMore: requireClass(styles.passiveMore, "activitybar.module.css", "passiveMore"),
};

export function WatchesTab({ scope }: { scope: ActivityScope }) {
  const now = useNowTick(30_000);
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "watches");
  const collection = snapshot?.watches;
  if (collection?.permanent && collection.rows.length === 0)
    return <span className={CLASS.emptyNote}>Watches unavailable for this session.</span>;
  if (!collection || (collection.rows.length === 0 && !collection.complete))
    return <span className={CLASS.emptyNote}>Loading watches…</span>;
  const watches = collection.rows;
  if (watches.length === 0 && collection.complete)
    return <span className={CLASS.emptyNote}>No watches at this level.</span>;
  return (
    <div className={CLASS.stack}>
      {watches.map((watch) => (
        <WatchRow key={JSON.stringify([watch.receiverRef, watch.watch.id])} watch={watch} now={now} />
      ))}
      <ActivityPageBoundary
        resource="watches"
        label="watches"
        rows={collection.rows}
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        loadMore={loadMore}
      />
      {collection.error && !collection.permanent ? (
        <span className={CLASS.emptyNote}>Watches are updating…</span>
      ) : null}
    </div>
  );
}
