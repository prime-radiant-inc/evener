import type { NavigationWatchSummary, ThreadModel } from "@evener/appwire-client";
import {
  type ActivityCounts,
  type ActivityTree as ActivityTreeData,
  activityNodeID,
  errorText,
  parseActivityTree,
} from "@evener/appwire-client";
import { forwardRef, memo, useCallback, useEffect, useImperativeHandle, useRef, useState } from "react";
import {
  activityPanelStore,
  EMPTY_ACTIVITY_PANEL_ENTRY,
  retainedActivityTree,
  useActivityPanelStore,
} from "../../../stores/activityPanel";
import {
  activitySummaryStore,
  EMPTY_ACTIVITY_SUMMARY_ENTRY,
  useActivitySummaryStore,
} from "../../../stores/activitySummary";
import { threadsStore } from "../../../stores/threads";
import { EntityViewsProvider } from "../../../transcriptDisplay/entityViews";
import { Button, EmptyState, Sheet, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { useEntityView } from "../transcript/useEntityView";
import { ActivityTree, type ActivityTreeHandle } from "./ActivityTree";
import styles from "./activitypanel.module.css";
import { refreshActivityRoot, useActivityRefresh } from "./useActivityRefresh";

export interface ActivityPanelProps {
  sessionRef: string;
  model: ThreadModel;
  // The session's live watches, absent-able: an old daemon omits the list.
  watches?: NavigationWatchSummary[];
  // Rows the hub omitted from `watches`; the Watches header reports "+N more".
  omittedWatches?: number;
  // The armed subset of those omitted rows; folded into the header's armed total.
  omittedArmedWatches?: number;
  hideTrigger?: boolean;
  // SessionChrome's desktop replacement button hides this panel's own
  // trigger, but still needs the trigger-owned background summary refresh.
  // The store's loading/bump gate keeps this second owner duplicate-free.
  refreshWhenHidden?: boolean;
  // Defaults to refreshWhenHidden for direct hidden owners. SessionChrome
  // opts in only at live-session mount sites so isolated/read-only chrome
  // consumers keep the body's established first-attempt ownership contract.
  discoverWhenHidden?: boolean;
}

export interface ActivityPanelBodyProps {
  sessionRef: string;
  model: ThreadModel;
  watches?: NavigationWatchSummary[];
  omittedWatches?: number;
  omittedArmedWatches?: number;
}

export interface ActivityPanelHandle {
  open: () => void;
}

const CLASS = {
  state: requireClass(styles.state, "activitypanel.module.css", "state"),
  stale: requireClass(styles.stale, "activitypanel.module.css", "stale"),
  staleMessage: requireClass(styles.staleMessage, "activitypanel.module.css", "staleMessage"),
  panel: requireClass(styles.panel, "activitypanel.module.css", "panel"),
  panelColumn: requireClass(styles.panelColumn, "activitypanel.module.css", "panelColumn"),
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

// emptyPageIsPartial reports a page with no rows that is nonetheless not the
// end of the story: an explanation, a token for what follows, or both.
function emptyPageIsPartial(tree: ActivityTreeData): boolean {
  return Boolean(tree.root.branch.continuation || tree.root.branch.error);
}

// emptyWatchTree is the page the watch group renders through when there is no
// retained tree at all -- a load that failed, or a source that does not support
// retained activity. Watches are session data, so they are independent of that
// page, and the group itself lives in the tree's row list: giving the tree an
// empty page renders exactly the watch rows, through the same machinery and the
// same vocabulary as a loaded session, instead of a second renderer that could
// drift from it.
function emptyWatchTree(ref: string): ActivityTreeData {
  return {
    revision: 0,
    root: {
      kind: "session",
      sessionId: "",
      ref,
      label: "",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function triggerLabel(counts: ActivityCounts | undefined): string {
  if (!counts?.complete) return "Activity";
  return `Activity · ${counts.active}`;
}

/** Shared activity reader body used by the mobile Sheet and desktop pane. */
export const ActivityPanelBody = memo(function ActivityPanelBody({
  sessionRef,
  model,
  watches,
  omittedWatches,
  omittedArmedWatches,
}: ActivityPanelBodyProps) {
  const toasts = useToasts();
  const treeRef = useRef<ActivityTreeHandle>(null);
  const mountedRef = useRef(false);
  const bodyGenerationRef = useRef(0);
  const currentSessionRef = useRef(sessionRef);
  const entry = useActivityPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
  // The same builder the transcript uses, over the same session: the detail
  // strips open in this panel (the delegate line names its delegate id), and
  // the panel is the owner that has both the session ref and the model.
  const entities = useEntityView(sessionRef, model);
  currentSessionRef.current = sessionRef;

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

  const handleRefreshFailure = useCallback((sentence: string) => toasts.push("error", sentence), [toasts]);
  useActivityRefresh(sessionRef, model, { kind: "body", onFailure: handleRefreshFailure });

  const fetchRoot = useCallback(
    (continuation?: { nodeID: string; token: string }, forceRoot = false) => {
      // Both refresh paths report a failure through the same mount/session/
      // generation guard, so a fetch that fails after the Sheet closed or the
      // panel switched sessions cannot toast for a panel no longer on screen.
      const bodyGeneration = bodyGenerationRef.current;
      const onRefreshFailure = (sentence: string) => {
        if (
          mountedRef.current &&
          currentSessionRef.current === sessionRef &&
          bodyGenerationRef.current === bodyGeneration
        ) {
          toasts.push("error", sentence);
        }
      };
      if (!continuation) {
        refreshActivityRoot(sessionRef, model.jobsUpdatedAt, onRefreshFailure, forceRoot);
        return;
      }
      const requestID = activityPanelStore.getState().beginContinuationFetch(sessionRef, continuation.nodeID);
      // Refused while anything else is already out: a root refresh is about to
      // replace this tree and the token this click carried, and another
      // branch's page holds the one request this panel can have in flight.
      if (requestID === null) return;
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
          const retained = retainedActivityTree(activityPanelStore.getState().entries.get(sessionRef));
          if (retained && parsed.revision !== retained.revision) {
            // The page was minted against a different revision than the tree on
            // screen, so its cursor names positions that no longer line up.
            // Discard it rather than grafting mismatched entries. Ask for the
            // fresh root BEFORE settling the discard: refreshRoot defers behind
            // the pending continuation and publishFetch then issues exactly one
            // root fetch, rather than racing a second, redundant one after it.
            refreshActivityRoot(sessionRef, model.jobsUpdatedAt, onRefreshFailure, true);
            activityPanelStore.getState().publishFetch(sessionRef, requestID, {
              kind: "continuation-discarded",
              nodeID: continuation.nodeID,
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

  function handleContinue(nodeID: string, token: string) {
    fetchRoot({ nodeID, token });
  }

  function renderBody() {
    // A session whose watch rows were all omitted by the hub's cap still holds
    // watch content: ActivityTree renders the watch group and its "+N more".
    // Only a session with neither retained nor omitted watches is empty.
    const hasWatchContent = (watches?.length ?? 0) > 0 || (omittedWatches ?? 0) > 0;
    // The watch group is part of the tree's row list, so a session whose
    // retained activity never loaded still renders it through the same
    // machinery. An armed watch is often the only pending work a session has --
    // the rail counts it on the row -- so hiding it behind the load state's
    // message would contradict the rest of the chrome.
    function renderWatchTree(tree: ActivityTreeData) {
      return (
        <div className={CLASS.panelColumn}>
          <ActivityTree
            ref={treeRef}
            tree={tree}
            watches={watches}
            omittedWatches={omittedWatches}
            omittedArmedWatches={omittedArmedWatches}
            expandedFoldIDs={entry.expandedFoldIDs}
            onToggleFold={(foldID) => activityPanelStore.getState().toggleFold(sessionRef, foldID)}
            continuationFailures={entry.continuationFailures}
            onContinue={handleContinue}
            loadingContinuationID={entry.continuationLoadingID}
            rootRefreshing={entry.pending?.kind === "root"}
          />
        </div>
      );
    }
    const watchFallback = hasWatchContent ? renderWatchTree(emptyWatchTree(sessionRef)) : null;
    if (entry.load.kind === "unsupported") {
      return (
        <>
          {watchFallback}
          <EmptyState
            title="Activity isn't available"
            hint="This session's source doesn't support retained activity."
          />
        </>
      );
    }
    if (entry.load.kind === "failed") {
      return (
        <>
          {watchFallback}
          <EmptyState
            title={entry.load.error.headline}
            hint={entry.load.error.detail}
            action={
              <Button variant="quiet" size="sm" onClick={() => fetchRoot(undefined, true)}>
                Try again
              </Button>
            }
          />
        </>
      );
    }
    if (entry.load.kind === "idle" || entry.load.kind === "loading") {
      // A request that has not answered yet is not a session without watches:
      // the group renders above the loading line for the same reason it renders
      // above the failure state.
      return (
        <>
          {watchFallback}
          <p className={CLASS.state}>Loading activity…</p>
        </>
      );
    }
    const currentTree = retainedTree(entry.load);
    const staleError = entry.load.kind === "ready" ? entry.load.staleError : undefined;
    const ended = entry.load.kind === "ended";
    return (
      <div className={CLASS.panel}>
        {ended && !currentTree && (
          <>
            {watchFallback}
            <EmptyState
              title="This session has ended"
              hint="Its daemon has exited, and there's no retained activity to fall back on."
            />
          </>
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
        {currentTree && !currentTree.root.counts.complete && (
          <p className={CLASS.state}>Activity coverage is incomplete.</p>
        )}
        {currentTree?.root.diagnostics?.map((diagnostic) => (
          <p className={CLASS.state} key={diagnostic}>
            {diagnostic}
          </p>
        ))}
        {currentTree && currentTree.root.entries.length === 0 && !hasWatchContent ? (
          emptyPageIsPartial(currentTree) ? (
            // A page can come back with no rows and still have more behind it:
            // the agent drops an entry it cannot encode, says so on the root
            // branch, and hands back a token for what follows. Without this
            // strip the reader is told there is no activity, and what the
            // skipped entry was hiding stays unreachable.
            <EmptyState
              title="Nothing on this page"
              hint={
                entry.continuationFailures[activityNodeID(currentTree.root)] ??
                currentTree.root.branch.error ??
                "This page of retained activity came back empty."
              }
              action={
                currentTree.root.branch.continuation ? (
                  <Button
                    variant="quiet"
                    size="sm"
                    disabled={entry.pending !== undefined}
                    onClick={() =>
                      handleContinue(activityNodeID(currentTree.root), currentTree.root.branch.continuation ?? "")
                    }
                  >
                    {entry.continuationLoadingID === activityNodeID(currentTree.root) ? "Loading…" : "Load more"}
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <EmptyState
              title="No retained activity yet"
              hint="No shell or delegate activity has been retained for this session."
            />
          )
        ) : currentTree ? (
          renderWatchTree(currentTree)
        ) : null}
      </div>
    );
  }

  // The one owner of the session's entity map in the chrome republishes it to
  // every EntityRef below (the detail strips' delegate lines) through the
  // dedicated entity-views context, instead of threading it down the tree.
  return <EntityViewsProvider entities={entities}>{renderBody()}</EntityViewsProvider>;
});

export const ActivityPanel = forwardRef<ActivityPanelHandle, ActivityPanelProps>(function ActivityPanel(
  {
    sessionRef,
    model,
    watches,
    omittedWatches,
    omittedArmedWatches,
    hideTrigger = false,
    refreshWhenHidden = false,
    discoverWhenHidden = refreshWhenHidden,
  },
  ref,
) {
  const [open, setOpen] = useState(false);
  const summary = useActivitySummaryStore((state) => state.entries.get(sessionRef)) ?? EMPTY_ACTIVITY_SUMMARY_ENTRY;

  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: this effect resets the Sheet's transient open state when the ref changes
  useEffect(() => {
    setOpen(false);
  }, [sessionRef]);

  useActivityRefresh(sessionRef, model, {
    kind: "background",
    bodyOwnsFreshness: open || summary.mountedBodies > 0,
    suppressed: hideTrigger && !refreshWhenHidden,
    // SessionChrome's hidden owner is the only closed trigger that establishes
    // a fresh summary; ordinary visible triggers retain their fetch-on-open contract.
    discoverUnestablished: hideTrigger && discoverWhenHidden,
  });

  return (
    <>
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          {triggerLabel(summary.counts)}
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Activity" size="wide">
        {open ? (
          <ActivityPanelBody
            sessionRef={sessionRef}
            model={model}
            watches={watches}
            omittedWatches={omittedWatches}
            omittedArmedWatches={omittedArmedWatches}
          />
        ) : null}
      </Sheet>
    </>
  );
});
