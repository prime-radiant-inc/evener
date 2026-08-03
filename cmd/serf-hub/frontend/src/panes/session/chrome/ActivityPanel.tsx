import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react";
import { errorText, sessionActionError, sessionActionHeadline } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { useIsMobile } from "../../../shell/useIsMobile";
import { threadsStore } from "../../../stores/threads";
import { Button, EmptyState, Sheet, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ActivityInspector } from "./ActivityInspector";
import { ActivityTree, type ActivityTreeHandle, findActivitySelection } from "./ActivityTree";
import {
  type ActivityDelegate,
  type ActivityDisclosureState,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityTree as ActivityTreeData,
  activityNodeID,
  defaultExpandedIDs,
  parseActivityTree,
  reconcileActivityState,
} from "./activityData";
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

type ActivityLoadState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; tree: ActivityTreeData; staleError?: LoadFailure }
  | { kind: "unsupported" }
  | { kind: "failed"; error: LoadFailure }
  | { kind: "ended"; tree?: ActivityTreeData };

interface PanelState {
  load: ActivityLoadState;
  disclosure: ActivityDisclosureState;
  established: boolean;
  continuationLoadingID?: string;
  continuationFailures: Record<string, string | undefined>;
}

type PanelAction =
  | { type: "reset" }
  | { type: "fetch/start"; keepTree: boolean }
  | { type: "fetch/ready"; tree: ActivityTreeData; disclosure: ActivityDisclosureState }
  | { type: "fetch/unsupported" }
  | { type: "fetch/failed"; error: LoadFailure }
  | { type: "fetch/ended" }
  | { type: "continuation/start"; nodeID: string }
  | { type: "continuation/ready"; tree: ActivityTreeData; disclosure: ActivityDisclosureState }
  | { type: "continuation/failed"; nodeID: string; message: string }
  | { type: "disclosure/expanded"; expandedIDs: string[] }
  | { type: "disclosure/selected"; selectedID?: string };

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

const INITIAL_STATE: PanelState = {
  load: { kind: "idle" },
  disclosure: { expandedIDs: [], selectedID: undefined, selectionPruned: false },
  established: false,
  continuationFailures: {},
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

function retainedTree(load: ActivityLoadState): ActivityTreeData | undefined {
  if (load.kind === "ready") return load.tree;
  if (load.kind === "ended") return load.tree;
  return undefined;
}

function triggerLabel(tree: ActivityTreeData | undefined): string {
  if (!tree?.root.counts.complete) return "Activity";
  return `Activity · ${tree.root.counts.active}`;
}

function initialDisclosure(tree: ActivityTreeData): ActivityDisclosureState {
  return {
    expandedIDs: defaultExpandedIDs(tree),
    selectedID: undefined,
    selectionPruned: false,
    tree,
  };
}

function cloneEntry(entry: ActivityEntry): ActivityEntry {
  return entry.kind === "shell"
    ? { kind: "shell", job: { ...entry.job } }
    : { kind: "delegate", delegate: cloneDelegate(entry.delegate) };
}

function cloneSession(session: ActivitySessionNode): ActivitySessionNode {
  return {
    ...session,
    counts: { ...session.counts },
    branch: { ...session.branch },
    entries: session.entries.map(cloneEntry),
  };
}

function cloneDelegate(delegate: ActivityDelegate): ActivityDelegate {
  return {
    ...delegate,
    turns: delegate.turns.map((turn) => ({ ...turn })),
    branch: { ...delegate.branch },
    child: delegate.child ? cloneSession(delegate.child) : undefined,
  };
}

function mergeDelegate(current: ActivityDelegate, patch: ActivityDelegate, targetID: string): ActivityDelegate {
  const delegateID = activityNodeID({ kind: "delegate", delegateId: current.delegateId });
  if (delegateID === targetID) return cloneDelegate(patch);
  return {
    ...current,
    childSessionId: patch.childSessionId,
    childRef: patch.childRef,
    mandate: patch.mandate,
    turns: patch.turns.map((turn) => ({ ...turn })),
    branch: { ...patch.branch },
    child:
      current.child && patch.child && current.child.sessionId === patch.child.sessionId
        ? mergeSession(current.child, patch.child, targetID)
        : patch.child
          ? cloneSession(patch.child)
          : current.child
            ? cloneSession(current.child)
            : undefined,
  };
}

function mergeSession(current: ActivitySessionNode, patch: ActivitySessionNode, targetID: string): ActivitySessionNode {
  if (activityNodeID(current) === targetID) return cloneSession(patch);
  const patchByID = new Map<string, ActivityEntry>();
  for (const entry of patch.entries) {
    patchByID.set(entry.kind === "shell" ? activityNodeID(entry) : activityNodeID(entry), entry);
  }
  const mergedEntries = current.entries.map((entry) => {
    const id = entry.kind === "shell" ? activityNodeID(entry) : activityNodeID(entry);
    const patchEntry = patchByID.get(id);
    if (!patchEntry) return cloneEntry(entry);
    if (entry.kind === "delegate" && patchEntry.kind === "delegate") {
      return { kind: "delegate", delegate: mergeDelegate(entry.delegate, patchEntry.delegate, targetID) };
    }
    return cloneEntry(patchEntry);
  }) as ActivityEntry[];
  for (const patchEntry of patch.entries) {
    const id = patchEntry.kind === "shell" ? activityNodeID(patchEntry) : activityNodeID(patchEntry);
    if (
      !current.entries.some((entry) => (entry.kind === "shell" ? activityNodeID(entry) : activityNodeID(entry)) === id)
    ) {
      mergedEntries.push(cloneEntry(patchEntry));
    }
  }
  return {
    ...current,
    ref: patch.ref,
    label: patch.label,
    aggregate: patch.aggregate,
    counts: { ...patch.counts },
    branch: { ...patch.branch },
    entries: mergedEntries,
  };
}

function graftContinuationTree(current: ActivityTreeData, targetID: string, patch: ActivityTreeData): ActivityTreeData {
  return {
    revision: Math.max(current.revision, patch.revision),
    root: mergeSession(current.root, patch.root, targetID),
  };
}

function reducer(state: PanelState, action: PanelAction): PanelState {
  switch (action.type) {
    case "reset":
      return INITIAL_STATE;
    case "fetch/start": {
      const tree = retainedTree(state.load);
      if (action.keepTree && tree) {
        return {
          ...state,
          load: { kind: "ready", tree },
          continuationLoadingID: undefined,
          continuationFailures: state.continuationFailures,
        };
      }
      return {
        ...state,
        load: { kind: "loading" },
        continuationLoadingID: undefined,
        continuationFailures: state.continuationFailures,
      };
    }
    case "fetch/ready":
      return {
        load: { kind: "ready", tree: action.tree },
        disclosure: { ...action.disclosure, tree: action.tree },
        established: true,
        continuationLoadingID: undefined,
        continuationFailures: state.continuationFailures,
      };
    case "fetch/unsupported":
      return { ...state, load: { kind: "unsupported" }, continuationLoadingID: undefined, continuationFailures: {} };
    case "fetch/failed": {
      const tree = retainedTree(state.load);
      if (tree) {
        return {
          ...state,
          load: { kind: "ready", tree, staleError: action.error },
          continuationLoadingID: undefined,
          continuationFailures: state.continuationFailures,
        };
      }
      return {
        ...state,
        load: { kind: "failed", error: action.error },
        continuationLoadingID: undefined,
        continuationFailures: {},
      };
    }
    case "fetch/ended": {
      const tree = retainedTree(state.load);
      return {
        ...state,
        load: { kind: "ended", tree },
        continuationLoadingID: undefined,
        continuationFailures: state.continuationFailures,
      };
    }
    case "continuation/start": {
      const continuationFailures = { ...state.continuationFailures };
      delete continuationFailures[action.nodeID];
      return { ...state, continuationLoadingID: action.nodeID, continuationFailures };
    }
    case "continuation/ready": {
      const continuationFailures = { ...state.continuationFailures };
      delete continuationFailures[state.continuationLoadingID ?? ""];
      return {
        ...state,
        load: { kind: "ready", tree: action.tree },
        disclosure: { ...action.disclosure, tree: action.tree },
        continuationLoadingID: undefined,
        continuationFailures,
      };
    }
    case "continuation/failed": {
      const continuationFailures = { ...state.continuationFailures, [action.nodeID]: action.message };
      return { ...state, continuationLoadingID: undefined, continuationFailures };
    }
    case "disclosure/expanded":
      return {
        ...state,
        disclosure: { ...state.disclosure, expandedIDs: action.expandedIDs, selectionPruned: false },
      };
    case "disclosure/selected":
      return {
        ...state,
        disclosure: { ...state.disclosure, selectedID: action.selectedID, selectionPruned: false },
      };
    default:
      return state;
  }
}

export const ActivityPanel = forwardRef<ActivityPanelHandle, ActivityPanelProps>(function ActivityPanel(
  { sessionRef, model, now: _now, hideTrigger = false },
  ref,
) {
  const toasts = useToasts();
  const isMobile = useIsMobile();
  const treeRef = useRef<ActivityTreeHandle>(null);
  const stateRef = useRef<PanelState>(INITIAL_STATE);
  const requestIDRef = useRef(0);
  const fetchedBumpRef = useRef<number | null | undefined>(undefined);
  const mountedRef = useRef(true);
  const openRef = useRef(false);
  const focusRestoreIDRef = useRef<string | null>(null);
  const [open, setOpen] = useState(false);
  const [showMobileTree, setShowMobileTree] = useState(true);
  const [state, dispatch] = useReducer(reducer, INITIAL_STATE);

  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  useEffect(() => {
    openRef.current = open;
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
    dispatch({ type: "reset" });
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

  const tree = retainedTree(state.load);
  const selection = useMemo(
    () => findActivitySelection(tree, state.disclosure.selectedID),
    [tree, state.disclosure.selectedID],
  );

  const fetchRoot = useCallback(
    (continuation?: { nodeID: string; token: string }) => {
      const requestID = requestIDRef.current + 1;
      requestIDRef.current = requestID;
      const currentState = stateRef.current;
      const previousTree = retainedTree(currentState.load);
      if (!continuation) {
        fetchedBumpRef.current = model.jobsUpdatedAt;
        dispatch({ type: "fetch/start", keepTree: previousTree !== undefined });
      } else {
        dispatch({ type: "continuation/start", nodeID: continuation.nodeID });
      }
      void threadsStore
        .getState()
        .listJobs(sessionRef, continuation?.token)
        .then((data) => {
          if (!mountedRef.current || requestID !== requestIDRef.current) return;
          const parsed = parseActivityTree(data);
          if (parsed === null) {
            if (continuation) {
              dispatch({
                type: "continuation/failed",
                nodeID: continuation.nodeID,
                message: continuationFailureMessage(),
              });
              return;
            }
            dispatch({ type: "fetch/unsupported" });
            return;
          }
          if (continuation && previousTree) {
            const grafted = graftContinuationTree(previousTree, continuation.nodeID, parsed);
            const disclosure = reconcileActivityState({ ...currentState.disclosure, tree: previousTree }, grafted);
            dispatch({ type: "continuation/ready", tree: grafted, disclosure });
            return;
          }
          const disclosure = previousTree
            ? reconcileActivityState({ ...currentState.disclosure, tree: previousTree }, parsed)
            : initialDisclosure(parsed);
          dispatch({ type: "fetch/ready", tree: parsed, disclosure });
        })
        .catch((err) => {
          if (!mountedRef.current || requestID !== requestIDRef.current) return;
          if (continuation) {
            dispatch({
              type: "continuation/failed",
              nodeID: continuation.nodeID,
              message: continuationFailureMessage(err),
            });
            return;
          }
          if (isActionUnavailable(err)) {
            dispatch({ type: "fetch/unsupported" });
            return;
          }
          if (isThreadNotFound(err)) {
            dispatch({ type: "fetch/ended" });
            return;
          }
          const failure = loadFailure(err);
          dispatch({ type: "fetch/failed", error: failure });
          if (openRef.current) toasts.push("error", failure.sentence);
        });
    },
    [model.jobsUpdatedAt, sessionRef, toasts],
  );

  useEffect(() => {
    if (open) {
      const needsVisibleFetch =
        state.load.kind === "idle" ||
        state.load.kind === "unsupported" ||
        state.load.kind === "failed" ||
        state.load.kind === "ended" ||
        fetchedBumpRef.current !== model.jobsUpdatedAt;
      if (needsVisibleFetch) fetchRoot();
      return;
    }
    if (hideTrigger) return;
    if (fetchedBumpRef.current === undefined) return;
    if (fetchedBumpRef.current === model.jobsUpdatedAt) return;
    fetchRoot();
  }, [fetchRoot, hideTrigger, model.jobsUpdatedAt, open, state.load.kind]);

  useEffect(() => {
    if (!isMobile) setShowMobileTree(true);
  }, [isMobile]);

  useEffect(() => {
    if (isMobile && state.disclosure.selectedID) setShowMobileTree(false);
  }, [isMobile, state.disclosure.selectedID]);

  function handleContinue(nodeID: string, token: string) {
    fetchRoot({ nodeID, token });
  }

  function handleBackToActivity() {
    focusRestoreIDRef.current = state.disclosure.selectedID ?? null;
    setShowMobileTree(true);
  }

  function renderBody() {
    if (state.load.kind === "unsupported") {
      return (
        <EmptyState title="Activity isn't available" hint="This session's source doesn't support retained activity." />
      );
    }
    if (state.load.kind === "failed") {
      return (
        <EmptyState
          title={state.load.error.headline}
          hint={state.load.error.detail}
          action={
            <Button variant="quiet" size="sm" onClick={() => fetchRoot()}>
              Try again
            </Button>
          }
        />
      );
    }
    if (state.load.kind === "idle" || state.load.kind === "loading") {
      return <p className={CLASS.state}>Loading activity…</p>;
    }
    const currentTree = retainedTree(state.load);
    const staleError = state.load.kind === "ready" ? state.load.staleError : undefined;
    const ended = state.load.kind === "ended";
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
                  removedSelectionNotice={state.disclosure.selectionPruned}
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
                  expandedIDs={state.disclosure.expandedIDs}
                  selectedID={state.disclosure.selectedID}
                  continuationFailures={state.continuationFailures}
                  onExpandedChange={(expandedIDs) => dispatch({ type: "disclosure/expanded", expandedIDs })}
                  onSelect={(selectedID) => dispatch({ type: "disclosure/selected", selectedID })}
                  onContinue={handleContinue}
                  loadingContinuationID={state.continuationLoadingID}
                />
              </div>
              {!isMobile && (
                <div className={CLASS.panelColumn}>
                  <ActivityInspector
                    selection={selection}
                    removedSelectionNotice={state.disclosure.selectionPruned}
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
