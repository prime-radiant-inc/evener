import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { errorText } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { useIsMobile } from "../../../shell/useIsMobile";
import { activityPanelStore, EMPTY_ACTIVITY_PANEL_ENTRY, useActivityPanelStore } from "../../../stores/activityPanel";
import {
  activitySummaryStore,
  EMPTY_ACTIVITY_SUMMARY_ENTRY,
  useActivitySummaryStore,
} from "../../../stores/activitySummary";
import { threadsStore } from "../../../stores/threads";
import { Button, EmptyState, Sheet, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ActivityInspector } from "./ActivityInspector";
import { ActivityTree, type ActivityTreeHandle, findActivitySelection } from "./ActivityTree";
import { type ActivityCounts, type ActivityTree as ActivityTreeData, parseActivityTree } from "./activityData";
import styles from "./activitypanel.module.css";

export interface ActivityPanelProps {
  sessionRef: string;
  model: ThreadModel;
  now: number;
  hideTrigger?: boolean;
  // SessionChrome's desktop replacement button hides this panel's own
  // trigger, but still needs the trigger-owned background summary refresh.
  // The store's loading/bump gate keeps this second owner duplicate-free.
  refreshWhenHidden?: boolean;
}

export interface ActivityPanelBodyProps {
  sessionRef: string;
  model: ThreadModel;
}

export interface ActivityPanelHandle {
  open: () => void;
}

const CLASS = {
  state: requireClass(styles.state, "activitypanel.module.css", "state"),
  stale: requireClass(styles.stale, "activitypanel.module.css", "stale"),
  staleMessage: requireClass(styles.staleMessage, "activitypanel.module.css", "staleMessage"),
  panel: requireClass(styles.panel, "activitypanel.module.css", "panel"),
  masterDetail: requireClass(styles.masterDetail, "activitypanel.module.css", "masterDetail"),
  mobilePane: requireClass(styles.mobilePane, "activitypanel.module.css", "mobilePane"),
  panelColumn: requireClass(styles.panelColumn, "activitypanel.module.css", "panelColumn"),
  mobileBack: requireClass(styles.mobileBack, "activitypanel.module.css", "mobileBack"),
};

function continuationFailureMessage(err?: unknown): string {
  const detail = err ? errorText(err).trim() : "";
  return detail
    ? `Couldn't load more retained activity for this branch: ${detail}`
    : "Couldn't load more retained activity for this branch.";
}

function retainedTree(load: (typeof EMPTY_ACTIVITY_PANEL_ENTRY)["load"]): ActivityTreeData | undefined {
  if (load.kind === "ready") return load.tree;
  if (load.kind === "ended") return load.tree;
  return undefined;
}

function triggerLabel(counts: ActivityCounts | undefined): string {
  if (!counts?.complete) return "Activity";
  return `Activity · ${counts.active}`;
}

function refreshRoot(
  sessionRef: string,
  bump: number | null,
  onFailure?: (sentence: string) => void,
  force = false,
): number | null {
  return activitySummaryStore
    .getState()
    .refreshRoot(sessionRef, bump, (ref) => threadsStore.getState().listJobs(ref), onFailure, force);
}

/** Shared activity reader body used by the mobile Sheet and desktop pane. */
export function ActivityPanelBody({ sessionRef, model }: ActivityPanelBodyProps) {
  const toasts = useToasts();
  const isMobile = useIsMobile();
  const treeRef = useRef<ActivityTreeHandle>(null);
  const mountedRef = useRef(false);
  const bodyGenerationRef = useRef(0);
  const focusRestoreIDRef = useRef<string | null>(null);
  const currentSessionRef = useRef(sessionRef);
  const [showMobileTree, setShowMobileTree] = useState(true);
  const entry = useActivityPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
  const summary = useActivitySummaryStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_SUMMARY_ENTRY;
  currentSessionRef.current = sessionRef;

  // biome-ignore lint/correctness/useExhaustiveDependencies: this effect resets transient mobile navigation when the ref changes
  useEffect(() => {
    setShowMobileTree(true);
  }, [sessionRef]);

  useEffect(() => {
    const bodyGeneration = bodyGenerationRef.current + 1;
    bodyGenerationRef.current = bodyGeneration;
    mountedRef.current = true;
    activitySummaryStore.getState().mountBody(sessionRef);
    return () => {
      mountedRef.current = false;
      activitySummaryStore.getState().unmountBody(sessionRef);
    };
  }, [sessionRef]);

  useLayoutEffect(() => {
    if (!showMobileTree) return;
    if (!focusRestoreIDRef.current) return;
    const id = focusRestoreIDRef.current;
    focusRestoreIDRef.current = null;
    treeRef.current?.focusRow(id);
  }, [showMobileTree]);

  const tree = retainedTree(entry.load);
  const selection = useMemo(
    () => findActivitySelection(tree, entry.disclosure.selectedID),
    [tree, entry.disclosure.selectedID],
  );

  const fetchRoot = useCallback(
    (continuation?: { nodeID: string; token: string }, forceRoot = false) => {
      if (!continuation) {
        const bodyGeneration = bodyGenerationRef.current;
        refreshRoot(
          sessionRef,
          model.jobsUpdatedAt,
          (sentence) => {
            if (
              mountedRef.current &&
              currentSessionRef.current === sessionRef &&
              bodyGenerationRef.current === bodyGeneration
            ) {
              toasts.push("error", sentence);
            }
          },
          forceRoot,
        );
        return;
      }
      const requestID = activityPanelStore.getState().beginFetch(sessionRef, { nodeID: continuation.nodeID });
      void threadsStore
        .getState()
        .listJobs(sessionRef, continuation.token)
        .then((data) => {
          const parsed = parseActivityTree(data);
          if (parsed === null) {
            activityPanelStore.getState().publishFetch(sessionRef, requestID, {
              kind: "continuation-failed",
              nodeID: continuation.nodeID,
              message: continuationFailureMessage(),
            });
            return;
          }
          activityPanelStore.getState().publishFetch(sessionRef, requestID, { kind: "ready", tree: parsed });
        })
        .catch((err) => {
          activityPanelStore.getState().publishFetch(sessionRef, requestID, {
            kind: "continuation-failed",
            nodeID: continuation.nodeID,
            message: continuationFailureMessage(err),
          });
        });
    },
    [model.jobsUpdatedAt, sessionRef, toasts],
  );

  // The mount fetch preserves the old visible-fetch contract exactly: a
  // changed jobs bump is fresh, while idle/failed/unsupported/ended retained
  // states are retried even when the bump has not changed. Store completions
  // stay live after this body unmounts, so this effect intentionally does not
  // depend on completion state and cannot loop after a failed request.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the gate is sampled on mount and jobsUpdatedAt pushes; store completion changes must not turn a retained failure into a retry loop
  useEffect(() => {
    const bumpMismatch = summary.lastFetchedBump !== model.jobsUpdatedAt;
    const retainedNonReady =
      entry.load.kind === "idle" ||
      entry.load.kind === "failed" ||
      entry.load.kind === "unsupported" ||
      entry.load.kind === "ended";
    if (bumpMismatch || retainedNonReady) fetchRoot(undefined, retainedNonReady);
  }, [fetchRoot, model.jobsUpdatedAt, sessionRef]);

  useEffect(() => {
    if (!isMobile) setShowMobileTree(true);
  }, [isMobile]);

  useEffect(() => {
    if (isMobile && entry.disclosure.selectedID) setShowMobileTree(false);
  }, [entry.disclosure.selectedID, isMobile]);

  function handleContinue(nodeID: string, token: string) {
    fetchRoot({ nodeID, token });
  }

  function handleBackToActivity() {
    focusRestoreIDRef.current = entry.disclosure.selectedID ?? null;
    setShowMobileTree(true);
  }

  function renderBody() {
    if (entry.load.kind === "unsupported") {
      return (
        <EmptyState title="Activity isn't available" hint="This session's source doesn't support retained activity." />
      );
    }
    if (entry.load.kind === "failed") {
      return (
        <EmptyState
          title={entry.load.error.headline}
          hint={entry.load.error.detail}
          action={
            <Button variant="quiet" size="sm" onClick={() => fetchRoot(undefined, true)}>
              Try again
            </Button>
          }
        />
      );
    }
    if (entry.load.kind === "idle" || entry.load.kind === "loading") {
      return <p className={CLASS.state}>Loading activity…</p>;
    }
    const currentTree = retainedTree(entry.load);
    const staleError = entry.load.kind === "ready" ? entry.load.staleError : undefined;
    const ended = entry.load.kind === "ended";
    return (
      <div className={CLASS.panel}>
        {ended && !currentTree && (
          <EmptyState
            title="This session has ended"
            hint="Its daemon has exited, and there's no retained activity to fall back on."
          />
        )}
        {ended && currentTree && (
          <div className={CLASS.stale}>
            <p role="alert" className={CLASS.staleMessage}>
              This session has ended
            </p>
            <p className={CLASS.staleMessage}>Showing the last retained activity.</p>
          </div>
        )}
        {staleError && (
          <div className={CLASS.stale}>
            <p role="alert" className={CLASS.staleMessage}>
              {staleError.sentence}
            </p>
            <p className={CLASS.staleMessage}>Showing the last activity that loaded.</p>
            <Button variant="quiet" size="sm" onClick={() => fetchRoot(undefined, true)}>
              Try again
            </Button>
          </div>
        )}
        {currentTree && currentTree.root.entries.length === 0 ? (
          <EmptyState
            title="No retained activity yet"
            hint="No shell or delegate activity has been retained for this session."
          />
        ) : currentTree ? (
          isMobile && !showMobileTree ? (
            <div className={CLASS.mobilePane}>
              <div className={CLASS.mobileBack}>
                <Button variant="quiet" size="sm" onClick={handleBackToActivity}>
                  Back to activity
                </Button>
              </div>
              <div className={CLASS.panelColumn}>
                <ActivityInspector
                  selection={selection}
                  removedSelectionNotice={entry.disclosure.selectionPruned}
                  sessionRef={sessionRef}
                />
              </div>
            </div>
          ) : (
            <div className={CLASS.masterDetail}>
              <div className={CLASS.panelColumn}>
                <ActivityTree
                  ref={treeRef}
                  tree={currentTree}
                  expandedIDs={entry.disclosure.expandedIDs}
                  selectedID={entry.disclosure.selectedID}
                  continuationFailures={entry.continuationFailures}
                  onExpandedChange={(expandedIDs) => activityPanelStore.getState().setExpanded(sessionRef, expandedIDs)}
                  onSelect={(selectedID) => activityPanelStore.getState().setSelected(sessionRef, selectedID)}
                  onContinue={handleContinue}
                  loadingContinuationID={entry.continuationLoadingID}
                />
              </div>
              {!isMobile && (
                <div className={CLASS.panelColumn}>
                  <ActivityInspector
                    selection={selection}
                    removedSelectionNotice={entry.disclosure.selectionPruned}
                    sessionRef={sessionRef}
                  />
                </div>
              )}
            </div>
          )
        ) : null}
      </div>
    );
  }

  return renderBody();
}

export const ActivityPanel = forwardRef<ActivityPanelHandle, ActivityPanelProps>(function ActivityPanel(
  { sessionRef, model, now: _now, hideTrigger = false, refreshWhenHidden = false },
  ref,
) {
  const [open, setOpen] = useState(false);
  const summary = useActivitySummaryStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_SUMMARY_ENTRY;

  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: this effect resets the Sheet's transient open state when the ref changes
  useEffect(() => {
    setOpen(false);
  }, [sessionRef]);

  // A closed Sheet has no mounted body, so its trigger owns the established
  // background refresh. The root result also reconciles the panel store.
  useEffect(() => {
    if (open || (hideTrigger && !refreshWhenHidden) || summary.mountedBodies > 0) return;
    if (!refreshWhenHidden && !summary.established) return;
    if (summary.lastFetchedBump === model.jobsUpdatedAt) return;
    refreshRoot(sessionRef, model.jobsUpdatedAt);
  }, [
    hideTrigger,
    model.jobsUpdatedAt,
    open,
    refreshWhenHidden,
    sessionRef,
    summary.established,
    summary.lastFetchedBump,
    summary.mountedBodies,
  ]);

  return (
    <>
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          {triggerLabel(summary.counts)}
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Activity" size="wide">
        {open ? <ActivityPanelBody sessionRef={sessionRef} model={model} /> : null}
      </Sheet>
    </>
  );
});
