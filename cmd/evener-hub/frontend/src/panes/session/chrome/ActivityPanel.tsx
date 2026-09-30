import {
  buildEntityView,
  projectSessionActivity,
  type SessionActivityCollection,
  type ThreadModel,
} from "@evener/appwire-client";
import { forwardRef, memo, useEffect, useImperativeHandle, useMemo, useState } from "react";
import { ActivityPageBoundary } from "../../../shell/activitybar/ActivityPageBoundary";
import { activityPanelStore, EMPTY_ACTIVITY_PANEL_ENTRY, useActivityPanelStore } from "../../../stores/activityPanel";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { EntityViewsProvider } from "../../../transcriptDisplay/entityViews";
import { Button, EmptyState, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ActivityTree } from "./ActivityTree";
import styles from "./activitypanel.module.css";

export interface ActivityPanelProps {
  sessionRef: string;
  model: ThreadModel;
  hideTrigger?: boolean;
  refreshWhenHidden?: boolean;
  discoverWhenHidden?: boolean;
}
export interface ActivityPanelBodyProps {
  sessionRef: string;
  model: ThreadModel;
}
export interface ActivityPanelHandle {
  open(): void;
}
const COLLECTIONS: readonly SessionActivityCollection[] = ["delegates", "jobs", "watches"];
const CLASS = {
  panel: requireClass(styles.panel, "activitypanel.module.css", "panel"),
  panelColumn: requireClass(styles.panelColumn, "activitypanel.module.css", "panelColumn"),
  stale: requireClass(styles.stale, "activitypanel.module.css", "stale"),
};

/** The recursive tree owns explicit subtree demand only while its body is visible. */
export const ActivityPanelBody = memo(function ActivityPanelBody({ sessionRef, model }: ActivityPanelBodyProps) {
  const { snapshot, loadMore } = useSessionActivity(sessionRef, "subtree", COLLECTIONS);
  const presentation = useMemo(() => (snapshot ? projectSessionActivity(snapshot) : null), [snapshot]);
  const entry = useActivityPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
  const entities = useMemo(
    () =>
      buildEntityView({
        sessionRef,
        tree: presentation?.tree ?? undefined,
        delegates: model.delegates,
        turns: model.turns,
        stale: Boolean(snapshot?.delegates.error || snapshot?.jobs.error),
        ended: snapshot?.context?.availability === "retained",
      }),
    [sessionRef, presentation, model.delegates, model.turns, snapshot],
  );
  const tree = presentation?.tree;
  const empty = tree?.root.entries.length === 0 && presentation?.watches.length === 0;
  const complete = Boolean(presentation?.complete && snapshot?.watches.complete);
  const unavailable = COLLECTIONS.some((resource) => snapshot?.[resource].permanent);
  const updating = COLLECTIONS.some(
    (resource) =>
      !snapshot?.[resource].permanent &&
      (snapshot?.[resource].loading || snapshot?.[resource].error || snapshot?.[resource].unavailable),
  );
  return (
    <EntityViewsProvider entities={entities} ownerRef={sessionRef}>
      <div className={CLASS.panel}>
        {updating ? <p className={CLASS.stale}>Activity is updating…</p> : null}
        {!tree || (empty && !complete) ? (
          <EmptyState title={unavailable ? "Activity unavailable for this session" : "Loading activity…"} />
        ) : null}
        {empty && complete ? (
          <EmptyState
            title="No retained activity yet"
            hint="No shell, delegate or watch activity has been retained for this session."
          />
        ) : null}
        {tree && !empty ? (
          <div className={CLASS.panelColumn}>
            <ActivityTree
              tree={tree}
              watches={presentation.watches}
              watchCounts={snapshot?.summary?.watches}
              expandedFoldIDs={entry.expandedFoldIDs}
              onToggleFold={(foldID) => activityPanelStore.getState().toggleFold(sessionRef, foldID)}
            />
          </div>
        ) : null}
        {COLLECTIONS.map((resource) =>
          snapshot ? (
            <ActivityPageBoundary
              key={resource}
              resource={resource}
              label={resource}
              hasMore={snapshot[resource].hasMore}
              loading={snapshot[resource].loading}
              error={snapshot[resource].error}
              permanent={snapshot[resource].permanent}
              loadMore={loadMore}
            />
          ) : null,
        )}
      </div>
    </EntityViewsProvider>
  );
});

export const ActivityPanel = forwardRef<ActivityPanelHandle, ActivityPanelProps>(function ActivityPanel(
  { sessionRef, model, hideTrigger = false, refreshWhenHidden = false, discoverWhenHidden = refreshWhenHidden },
  ref,
) {
  const [open, setOpen] = useState(false);
  const { snapshot } = useSessionActivity(
    !hideTrigger || refreshWhenHidden || discoverWhenHidden || open ? sessionRef : null,
  );
  const summary = snapshot?.summary;
  const active = summary?.delegates.known && summary.jobs.known ? summary.delegates.active + summary.jobs.active : null;
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: each selected session has its own Sheet lifetime
  useEffect(() => {
    setOpen(false);
  }, [sessionRef]);
  return (
    <>
      {!hideTrigger ? (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          {active === null ? "Activity" : `Activity · ${active}`}
        </Button>
      ) : null}
      <Sheet open={open} onClose={() => setOpen(false)} title="Activity" size="wide">
        {open ? <ActivityPanelBody sessionRef={sessionRef} model={model} /> : null}
      </Sheet>
    </>
  );
});
