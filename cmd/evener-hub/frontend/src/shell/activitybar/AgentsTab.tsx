import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The selected session's typed delegates: current rows stay visible, while
// inactive history opens through disclosure. Visible page boundaries add demand
// to the shared owner without acquiring another reader or retry loop.

import { activityNodeID, type SessionDelegate } from "@evener/appwire-client";
import { openTranscript } from "../../panes/session/transcript/openTranscript";
import { enterAgentCascade } from "../../panes/zoom/actions";
import { useSessionActivity } from "../../stores/sessionActivity";
import { Disclosure, EmptyState, InlineNotice, Skeleton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { isMobileViewport } from "../useIsMobile";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import { activityPlaceholderLines } from "./activityPlaceholders";
import { AgentRow } from "./activityRows";
import type { ActivityTabBodyProps } from "./activityTabs";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
};

function drill(sub: SessionDelegate): void {
  if (isMobileViewport()) openTranscript(sub.childRef, sub.ownerRef);
  else enterAgentCascade(sub);
}

export function AgentsTab({ scope, now, onDrill = drill }: ActivityTabBodyProps) {
  const foldID = `${scope.leaf.ref}\0inactive-delegates`;
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "delegates");
  const collection = snapshot?.delegates;
  const summary = snapshot?.summary?.delegates;
  const inactiveTotal = summary?.known ? summary.total - summary.active : null;
  const rows = collection?.rows ?? [];
  const current = rows.filter((row) => !row.terminal);
  const inactive = rows.filter((row) => row.terminal);
  useActivityScrollProgress(
    collection?.rows.map((sub) => activityNodeID({ ...sub, kind: "delegate" })) ?? [],
    collection?.complete ?? false,
    collection?.hasMore ?? false,
  );
  if (collection?.permanent && collection.rows.length === 0)
    return <EmptyState size="compact" title="Subagents unavailable for this session." />;
  if (!collection || (collection.rows.length === 0 && !collection.complete)) {
    return <Skeleton lines={summary?.known ? activityPlaceholderLines(summary.total) : undefined} />;
  }
  return (
    <div className={CLASS.stack}>
      {rows.length === 0 && collection.complete ? (
        <EmptyState size="compact" title="No subagents at this level." />
      ) : null}
      {current.map((sub) => (
        <AgentRow key={activityNodeID({ ...sub, kind: "delegate" })} sub={sub} now={now} onDrill={() => onDrill(sub)} />
      ))}
      {inactive.length > 0 ? (
        <Disclosure
          id={foldID}
          summary={
            <span>
              {inactiveTotal === null
                ? "Inactive subagents"
                : `${inactiveTotal} inactive ${inactiveTotal === 1 ? "subagent" : "subagents"}`}
            </span>
          }
        >
          <div className={CLASS.stack}>
            {inactive.map((sub) => (
              <AgentRow
                key={activityNodeID({ ...sub, kind: "delegate" })}
                sub={sub}
                now={now}
                onDrill={() => onDrill(sub)}
              />
            ))}
          </div>
        </Disclosure>
      ) : null}
      <ActivityPageBoundary
        resource="delegates"
        rows={collection.rows}
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        loadMore={loadMore}
      />
      {collection.error && !collection.permanent ? (
        <InlineNotice live="off">Subagents are updating…</InlineNotice>
      ) : null}
    </div>
  );
}
