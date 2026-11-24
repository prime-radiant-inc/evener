// The shared loading, empty, and error sequence for the activity tab
// collections; each tab supplies only its rows.

import type { SessionActivityCollection, SessionActivityCollectionState } from "@evener/appwire-client";
import type { ReactNode } from "react";
import { EmptyState, InlineNotice, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { ActivityPageBoundary } from "./ActivityPageBoundary";
import styles from "./activitybar.module.css";
import { activityPlaceholderLines } from "./activityPlaceholders";

const STACK = requireClass(styles.stack, "activitybar.module.css", "stack");
const RESOURCE_BY_NOUN = { subagents: "delegates", jobs: "jobs", watches: "watches" } as const;
const capitalized = (noun: string): string => noun.charAt(0).toUpperCase() + noun.slice(1);

export function ActivityCollectionGate({
  noun,
  collection,
  summary,
  loadMore,
  children,
}: {
  noun: keyof typeof RESOURCE_BY_NOUN;
  collection: SessionActivityCollectionState<unknown> | undefined;
  /** The collection's count slice; it sizes the first-page skeleton. */
  summary: { known: boolean; total: number } | undefined;
  loadMore(resource: SessionActivityCollection): Promise<void>;
  children: ReactNode;
}) {
  const title = capitalized(noun);
  if (collection?.permanent && collection.rows.length === 0)
    return <EmptyState size="compact" title={`${title} unavailable for this session.`} />;
  if (!collection || (collection.rows.length === 0 && !collection.complete))
    return <Skeleton lines={summary?.known ? activityPlaceholderLines(summary.total) : undefined} />;
  const empty = collection.rows.length === 0 && collection.complete;
  const updating = Boolean(collection.error) && !collection.permanent;
  return (
    <div className={STACK}>
      {empty ? <EmptyState size="compact" title={`No ${noun} at this level.`} /> : null}
      {children}
      <ActivityPageBoundary
        resource={RESOURCE_BY_NOUN[noun]}
        rows={collection.rows}
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        loadMore={loadMore}
      />
      {updating ? <InlineNotice live="off">{`${title} are updating…`}</InlineNotice> : null}
    </div>
  );
}
