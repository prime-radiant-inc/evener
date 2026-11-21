import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The Watches tab observes the shared session watch collection and admits
// continuation pages at its visible boundary.

import { buildWatchRows } from "@evener/appwire-client";
import { useSessionActivity } from "../../stores/sessionActivity";
import { EmptyState, InlineNotice, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import { activityPlaceholderLines } from "./activityPlaceholders";
import { WatchRow } from "./activityRows";
import type { ActivityTabBodyProps } from "./activityTabs";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
};

export function WatchesTab({ scope, now }: ActivityTabBodyProps) {
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "watches");
  const collection = snapshot?.watches;
  const total = snapshot?.summary?.watches;
  const watchRows = buildWatchRows(collection?.rows ?? []);
  // The receiver-owned watch in this view names the viewed session when that
  // session is a delegate; its own row is not in its own delegates collection.
  const viewing = snapshot?.context ?? undefined;
  useActivityScrollProgress(
    watchRows.map((row) => row.id),
    collection?.complete ?? false,
    collection?.hasMore ?? false,
  );
  if (collection?.permanent && collection.rows.length === 0)
    return <EmptyState size="compact" title="Watches unavailable for this session." />;
  if (!collection || (collection.rows.length === 0 && !collection.complete))
    return <Skeleton lines={total?.known ? activityPlaceholderLines(total.total) : undefined} />;
  const watches = collection.rows;
  if (watches.length === 0 && collection.complete)
    return <EmptyState size="compact" title="No watches at this level." />;
  return (
    <div className={CLASS.stack}>
      {watchRows.map((row) => (
        <WatchRow key={row.id} row={row} now={now} viewing={viewing} />
      ))}
      <ActivityPageBoundary
        resource="watches"
        rows={collection.rows}
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        loadMore={loadMore}
      />
      {collection.error && !collection.permanent ? <InlineNotice live="off">Watches are updating…</InlineNotice> : null}
    </div>
  );
}
