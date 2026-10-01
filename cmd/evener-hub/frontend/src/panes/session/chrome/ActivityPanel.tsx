import {
  activityNodeID,
  buildEntityView,
  projectSessionActivity,
  type SessionActivityCollection,
  type ThreadModel,
  watchRowID,
} from "@evener/appwire-client";
import { forwardRef, memo, useCallback, useImperativeHandle, useLayoutEffect, useMemo } from "react";
import { ActivityPageBoundary } from "../../../shell/activitybar/ActivityPageBoundary";
import { ScopeCrumbs } from "../../../shell/statusbar/ScopeCrumbs";
import { deriveScope } from "../../../shell/statusbar/statusScope";
import { useIsMobile } from "../../../shell/useIsMobile";
import { activityPanelStore, EMPTY_ACTIVITY_PANEL_ENTRY, useActivityPanelStore } from "../../../stores/activityPanel";
import { navigationStore, useNavigationStore } from "../../../stores/navigation/store";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { EntityViewsProvider } from "../../../transcriptDisplay/entityViews";
import { Button, EmptyState, Sheet } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ActivityTree } from "./ActivityTree";
import { activityActionLabel } from "./activityFormat";
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
  const isMobile = useIsMobile();
  const { snapshot, loadMore } = useSessionActivity(sessionRef, "subtree", COLLECTIONS);
  const resources = useNavigationStore((state) => state.resources);
  // biome-ignore lint/correctness/useExhaustiveDependencies: resources invalidates the navigation title read.
  const scope = useMemo(
    () => deriveScope(navigationStore.getState(), sessionRef, snapshot),
    [sessionRef, snapshot, resources],
  );
  const presentation = useMemo(() => (snapshot ? projectSessionActivity(snapshot) : null), [snapshot]);
  const entry = useActivityPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
  const progress = useMemo(
    () => ({
      delegates: {
        sessionId: snapshot?.delegates.context?.sessionId,
        ids: snapshot?.delegates.rows.map((delegate) => activityNodeID({ ...delegate, kind: "delegate" })) ?? [],
        complete: snapshot?.delegates.complete ?? false,
      },
      jobs: {
        sessionId: snapshot?.jobs.context?.sessionId,
        ids: snapshot?.jobs.rows.map((job) => activityNodeID({ ...job, kind: "shell" })) ?? [],
        complete: snapshot?.jobs.complete ?? false,
      },
      watches: {
        sessionId: snapshot?.watches.context?.sessionId,
        ids: snapshot?.watches.rows.map((watch) => watchRowID(watch.receiverRef, watch.watch.id)) ?? [],
        complete: snapshot?.watches.complete ?? false,
      },
    }),
    [snapshot?.delegates, snapshot?.jobs, snapshot?.watches],
  );
  const resolvedSessionId = snapshot?.context?.sessionId;
  useLayoutEffect(() => {
    if (resolvedSessionId) activityPanelStore.getState().recordLoadedExtent(sessionRef, resolvedSessionId, progress);
  }, [sessionRef, resolvedSessionId, progress]);
  const detailDisclosure = useMemo(
    () => ({
      overrides: entry.detailOverrides,
      onOpenChange: (rowID: string, open: boolean) =>
        activityPanelStore.getState().setDetailOpen(sessionRef, rowID, open),
    }),
    [sessionRef, entry.detailOverrides],
  );
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
        <ScopeCrumbs path={scope.path} hierarchy />
        {!scope.ancestryKnown ? <p className={CLASS.stale}>Finding session context…</p> : null}
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
              compactDetails={isMobile}
              detailDisclosure={detailDisclosure}
              onToggleFold={(foldID) => activityPanelStore.getState().toggleFold(sessionRef, foldID)}
            />
          </div>
        ) : null}
        {COLLECTIONS.map((resource) =>
          snapshot ? (
            <ActivityPageBoundary
              key={`${resource}:${resolvedSessionId}`}
              resource={resource}
              label={resource}
              rows={snapshot[resource].rows}
              hasMore={snapshot[resource].hasMore}
              loading={snapshot[resource].loading}
              error={snapshot[resource].error}
              permanent={snapshot[resource].permanent}
              restore={
                entry.resolvedSessionId === resolvedSessionId &&
                progress[resource].sessionId === resolvedSessionId &&
                !progress[resource].complete &&
                !!entry.loadedExtent[resource] &&
                !progress[resource].ids.includes(entry.loadedExtent[resource])
              }
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
  const open = useActivityPanelStore((state) => state.entries.get(sessionRef)?.sheetOpen ?? false);
  const setOpen = useCallback(
    (open: boolean) => activityPanelStore.getState().setSheetOpen(sessionRef, open),
    [sessionRef],
  );
  const { snapshot } = useSessionActivity(
    !hideTrigger || refreshWhenHidden || discoverWhenHidden || open ? sessionRef : null,
  );
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), [setOpen]);
  return (
    <>
      {!hideTrigger ? (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          {activityActionLabel(snapshot?.summary)}
        </Button>
      ) : null}
      <Sheet open={open} onClose={() => setOpen(false)} title="Activity" size="wide">
        {open ? <ActivityPanelBody sessionRef={sessionRef} model={model} /> : null}
      </Sheet>
    </>
  );
});
