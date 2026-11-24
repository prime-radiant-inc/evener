// The Watches tab observes the shared session watch collection and admits
// continuation pages at its visible boundary.

import { buildWatchRows } from "@evener/appwire-client";
import { useSessionActivity } from "../../stores/sessionActivity";
import { ActivityCollectionGate } from "./ActivityCollectionGate";
import { useActivityScrollProgress } from "./ActivityViewport";
import { WatchRow } from "./activityRows";
import type { ActivityTabBodyProps } from "./activityTabs";

export function WatchesTab({ scope, now }: ActivityTabBodyProps) {
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "watches");
  const collection = snapshot?.watches;
  const summary = snapshot?.summary?.watches;
  const watchRows = buildWatchRows(collection?.rows ?? []);
  // The receiver-owned watch in this view names the viewed session when that
  // session is a delegate; its own row is not in its own delegates collection.
  const viewing = snapshot?.context ?? undefined;
  useActivityScrollProgress(
    watchRows.map((row) => row.id),
    collection?.complete ?? false,
    collection?.hasMore ?? false,
  );
  return (
    <ActivityCollectionGate noun="watches" collection={collection} summary={summary} loadMore={loadMore}>
      {watchRows.map((row) => (
        <WatchRow key={row.id} row={row} now={now} viewing={viewing} />
      ))}
    </ActivityCollectionGate>
  );
}
