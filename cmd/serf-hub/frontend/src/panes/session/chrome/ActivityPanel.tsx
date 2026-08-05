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
import { errorText, sessionActionError, sessionActionHeadline } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { useIsMobile } from "../../../shell/useIsMobile";
import { activityPanelStore, EMPTY_ACTIVITY_PANEL_ENTRY, useActivityPanelStore } from "../../../stores/activityPanel";
import { threadsStore } from "../../../stores/threads";
import { Button, EmptyState, Sheet, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ActivityInspector } from "./ActivityInspector";
import { ActivityTree, type ActivityTreeHandle, findActivitySelection } from "./ActivityTree";
import { type ActivityTree as ActivityTreeData, parseActivityTree } from "./activityData";
import styles from "./activitypanel.module.css";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";

export interface ActivityPanelProps {
  sessionRef: string;
  model: ThreadModel;
  now: number;
  hideTrigger?: boolean;
}

export interface ActivityPanelHandle {
  open: () => void;
}

interface LoadFailure {
  headline: string;
  detail?: string;
  sentence: string;
}

const LOAD_FAILURE = "Couldn't load activity";

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

function loadFailure(err: unknown): LoadFailure {
  const headline = sessionActionHeadline(LOAD_FAILURE, err);
  const sentence = sessionActionError(LOAD_FAILURE, err);
  const detail = errorText(err).trim();
  return detail ? { headline, detail, sentence } : { headline, sentence };
}

function retainedTree(load: (typeof EMPTY_ACTIVITY_PANEL_ENTRY)["load"]): ActivityTreeData | undefined {
  if (load.kind === "ready") return load.tree;
  if (load.kind === "ended") return load.tree;
  return undefined;
}

function triggerLabel(tree: ActivityTreeData | undefined): string {
  if (!tree?.root.counts.complete) return "Activity";
  return `Activity · ${tree.root.counts.active}`;
}

export const ActivityPanel = forwardRef<ActivityPanelHandle, ActivityPanelProps>(function ActivityPanel(
  { sessionRef, model, now: _now, hideTrigger = false },
  ref,
) {
  const toasts = useToasts();
  const isMobile = useIsMobile();
  const treeRef = useRef<ActivityTreeHandle>(null);
  const requestIDRef = useRef(0);
  const fetchedBumpRef = useRef<number | null | undefined>(undefined);
  const mountedRef = useRef(true);
  const openRef = useRef(false);
  const focusRestoreIDRef = useRef<string | null>(null);
  const openedRef = useRef(false);
  const [open, setOpen] = useState(false);
  const [showMobileTree, setShowMobileTree] = useState(true);
  const entry = useActivityPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_PANEL_ENTRY;

  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  useEffect(() => {
    openRef.current = open;
    if (!open) openedRef.current = false;
  }, [open]);

  useEffect(
    () => () => {
      mountedRef.current = false;
      requestIDRef.current += 1;
    },
    [],
  );

  useEffect(() => {
    if (sessionRef === "") return;
    requestIDRef.current += 1;
    fetchedBumpRef.current = undefined;
    setOpen(false);
    setShowMobileTree(true);
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
    (continuation?: { nodeID: string; token: string }) => {
      const requestID = activityPanelStore
        .getState()
        .beginFetch(sessionRef, continuation ? { nodeID: continuation.nodeID } : undefined);
      if (!continuation) fetchedBumpRef.current = model.jobsUpdatedAt;
      void threadsStore
        .getState()
        .listJobs(sessionRef, continuation?.token)
        .then((data) => {
          const parsed = parseActivityTree(data);
          if (parsed === null) {
            activityPanelStore.getState().publishFetch(
              sessionRef,
              requestID,
              continuation
                ? {
                    kind: "continuation-failed",
                    nodeID: continuation.nodeID,
                    message: continuationFailureMessage(),
                  }
                : { kind: "unsupported" },
            );
            return;
          }
          activityPanelStore.getState().publishFetch(sessionRef, requestID, { kind: "ready", tree: parsed });
        })
        .catch((err) => {
          if (continuation) {
            activityPanelStore.getState().publishFetch(sessionRef, requestID, {
              kind: "continuation-failed",
              nodeID: continuation.nodeID,
              message: continuationFailureMessage(err),
            });
            return;
          }
          if (isActionUnavailable(err)) {
            activityPanelStore.getState().publishFetch(sessionRef, requestID, { kind: "unsupported" });
            return;
          }
          if (isThreadNotFound(err)) {
            activityPanelStore.getState().publishFetch(sessionRef, requestID, { kind: "ended" });
            return;
          }
          const failure = loadFailure(err);
          activityPanelStore.getState().publishFetch(sessionRef, requestID, { kind: "failed", error: failure });
          if (mountedRef.current && openRef.current) toasts.push("error", failure.sentence);
        });
    },
    [model.jobsUpdatedAt, sessionRef, toasts],
  );

  useEffect(() => {
    if (open) {
      const firstRenderWhileOpen = !openedRef.current;
      openedRef.current = true;
      const needsVisibleFetch = firstRenderWhileOpen || fetchedBumpRef.current !== model.jobsUpdatedAt;
      if (needsVisibleFetch) fetchRoot();
      return;
    }
    if (hideTrigger) return;
    if (fetchedBumpRef.current === undefined) return;
    if (fetchedBumpRef.current === model.jobsUpdatedAt) return;
    fetchRoot();
  }, [fetchRoot, hideTrigger, model.jobsUpdatedAt, open]);

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
            <Button variant="quiet" size="sm" onClick={() => fetchRoot()}>
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
            <Button variant="quiet" size="sm" onClick={() => fetchRoot()}>
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

  return (
    <>
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          {triggerLabel(tree)}
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Activity" size="wide">
        {renderBody()}
      </Sheet>
    </>
  );
});
