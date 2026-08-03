import {
  forwardRef,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
  useCallback,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Button, Chevron, StatusDot } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import {
  type ActivityDelegate,
  type ActivityJob,
  type ActivitySessionNode,
  type ActivityTree as ActivityTreeData,
  activityNodeID,
} from "./activityData";
import styles from "./activitypanel.module.css";

export type ActivitySelectionNode =
  | { kind: "session"; id: string; parentID?: string; level: number; session: ActivitySessionNode }
  | { kind: "delegate"; id: string; parentID?: string; level: number; delegate: ActivityDelegate }
  | {
      kind: "job";
      id: string;
      parentID?: string;
      level: number;
      job: ActivityJob;
      source: "session" | "delegate-turn";
    };

interface RenderNode {
  id: string;
  parentID?: string;
  level: number;
  expanded: boolean;
  hasChildren: boolean;
  selection: ActivitySelectionNode;
  label: string;
  detail?: string;
  statusText: string;
  branchError?: string;
  continuation?: { token: string; targetID: string };
  children: RenderNode[];
}

export interface ActivityTreeProps {
  tree: ActivityTreeData;
  expandedIDs: string[];
  selectedID?: string;
  continuationFailures?: Record<string, string | undefined>;
  onExpandedChange: (ids: string[]) => void;
  onSelect: (id: string) => void;
  onContinue?: (targetID: string, continuation: string) => void;
  loadingContinuationID?: string;
}

export interface ActivityTreeHandle {
  focusRow: (id: string) => void;
}

const CLASS = {
  tree: requireClass(styles.tree, "activitypanel.module.css", "tree"),
  row: requireClass(styles.row, "activitypanel.module.css", "row"),
  rowSelected: requireClass(styles.rowSelected, "activitypanel.module.css", "rowSelected"),
  rowContent: requireClass(styles.rowContent, "activitypanel.module.css", "rowContent"),
  rowMain: requireClass(styles.rowMain, "activitypanel.module.css", "rowMain"),
  rowMeta: requireClass(styles.rowMeta, "activitypanel.module.css", "rowMeta"),
  rowLabel: requireClass(styles.rowLabel, "activitypanel.module.css", "rowLabel"),
  rowDetail: requireClass(styles.rowDetail, "activitypanel.module.css", "rowDetail"),
  rowStatusText: requireClass(styles.rowStatusText, "activitypanel.module.css", "rowStatusText"),
  rowToggle: requireClass(styles.rowToggle, "activitypanel.module.css", "rowToggle"),
  rowActions: requireClass(styles.rowActions, "activitypanel.module.css", "rowActions"),
  rowContinuation: requireClass(styles.rowContinuation, "activitypanel.module.css", "rowContinuation"),
  group: requireClass(styles.group, "activitypanel.module.css", "group"),
};

function statusDotState(status: string, terminal?: boolean): "idle" | "working" | "needs-you" | "failed" | "ended" {
  const normalized = status.trim().toLowerCase();
  if (
    normalized === "running" ||
    normalized === "working" ||
    normalized === "queued" ||
    normalized === "starting" ||
    normalized === "resuming"
  ) {
    return "working";
  }
  if (normalized === "failed" || normalized === "exhausted" || normalized === "error") return "failed";
  if (normalized === "needs-you" || normalized === "blocked") return "needs-you";
  if (terminal === true || normalized === "completed" || normalized === "cancelled" || normalized === "stopped")
    return "ended";
  return "idle";
}

function pushUnique(ids: string[], id: string): string[] {
  return ids.includes(id) ? ids : [...ids, id];
}

function removeID(ids: string[], id: string): string[] {
  return ids.filter((value) => value !== id);
}

function delegateStatus(delegate: ActivityDelegate): string {
  const latest = delegate.turns.at(-1);
  if (latest) return latest.status;
  return delegate.child?.aggregate ?? "unknown";
}

function delegateHasActiveChild(delegate: ActivityDelegate): boolean {
  return (delegate.child?.counts.active ?? 0) > 0;
}

function delegateError(delegate: ActivityDelegate): string | undefined {
  return delegate.child?.branch.error ?? delegate.branch.error;
}

function delegateContinuation(delegate: ActivityDelegate): { token: string; targetID: string } | undefined {
  const childContinuation = delegate.child?.branch.continuation;
  if (childContinuation) {
    return {
      token: childContinuation,
      targetID: activityNodeID({ kind: "delegate", delegateId: delegate.delegateId }),
    };
  }
  if (delegate.branch.continuation) {
    return {
      token: delegate.branch.continuation,
      targetID: activityNodeID({ kind: "delegate", delegateId: delegate.delegateId }),
    };
  }
  return undefined;
}

function buildJobNode(
  job: ActivityJob,
  level: number,
  parentID: string,
  source: "session" | "delegate-turn",
): RenderNode {
  const id = activityNodeID({ kind: "shell", jobId: job.jobId });
  return {
    id,
    parentID,
    level,
    expanded: false,
    hasChildren: false,
    selection: { kind: "job", id, parentID, level, job, source },
    label: job.description,
    detail: source === "delegate-turn" ? "delegate turn" : (job.command ?? job.task),
    statusText: job.status,
    children: [],
  };
}

function buildSessionNode(
  session: ActivitySessionNode,
  expanded: ReadonlySet<string>,
  level: number,
  parentID?: string,
): RenderNode {
  const id = activityNodeID(session);
  const children: RenderNode[] = session.entries.map((entry) =>
    entry.kind === "shell"
      ? buildJobNode(entry.job, level + 1, id, "session")
      : buildDelegateNode(entry.delegate, expanded, level + 1, id),
  );
  return {
    id,
    parentID,
    level,
    expanded: expanded.has(id),
    hasChildren: children.length > 0,
    selection: { kind: "session", id, parentID, level, session },
    label: session.label,
    detail: `${session.counts.active} active · ${session.counts.failed} failed · ${session.counts.completed} completed`,
    statusText: session.aggregate,
    branchError: session.branch.error,
    continuation: session.branch.continuation ? { token: session.branch.continuation, targetID: id } : undefined,
    children,
  };
}

function buildDelegateNode(
  delegate: ActivityDelegate,
  expanded: ReadonlySet<string>,
  level: number,
  parentID: string,
): RenderNode {
  const id = activityNodeID({ kind: "delegate", delegateId: delegate.delegateId });
  const children: RenderNode[] = delegate.child ? [buildSessionNode(delegate.child, expanded, level + 1, id)] : [];
  const childLabel = delegate.child?.label ?? delegate.childSessionId;
  const mandate = delegate.mandate;
  const useMandateLabel = mandate !== undefined && delegateHasActiveChild(delegate);
  return {
    id,
    parentID,
    level,
    expanded: expanded.has(id),
    hasChildren: children.length > 0,
    selection: { kind: "delegate", id, parentID, level, delegate },
    label: useMandateLabel ? mandate : childLabel,
    detail: useMandateLabel ? childLabel : (mandate ?? "delegate"),
    statusText: delegateStatus(delegate),
    branchError: delegateError(delegate),
    continuation: delegateContinuation(delegate),
    children,
  };
}

function flattenVisible(nodes: RenderNode[]): RenderNode[] {
  const flat: RenderNode[] = [];
  const visit = (node: RenderNode) => {
    flat.push(node);
    if (node.hasChildren && node.expanded) {
      for (const child of node.children) visit(child);
    }
  };
  for (const node of nodes) visit(node);
  return flat;
}

function findNode(nodes: RenderNode[], id: string): RenderNode | undefined {
  for (const node of nodes) {
    if (node.id === id) return node;
    const child = findNode(node.children, id);
    if (child) return child;
  }
  return undefined;
}

export function findActivitySelection(
  tree: ActivityTreeData | undefined,
  id: string | undefined,
): ActivitySelectionNode | undefined {
  if (!tree || !id) return undefined;
  const root = buildSessionNode(tree.root, new Set<string>(), 1);
  return findNode([root], id)?.selection;
}

export const ActivityTree = forwardRef<ActivityTreeHandle, ActivityTreeProps>(function ActivityTree(
  {
    tree,
    expandedIDs,
    selectedID,
    continuationFailures = {},
    onExpandedChange,
    onSelect,
    onContinue,
    loadingContinuationID,
  },
  ref,
) {
  const [localExpandedIDs, setLocalExpandedIDs] = useState<string[]>(expandedIDs);
  useLayoutEffect(() => {
    setLocalExpandedIDs(expandedIDs);
  }, [expandedIDs]);

  const expanded = useMemo(() => new Set(localExpandedIDs), [localExpandedIDs]);
  const roots = useMemo(() => [buildSessionNode(tree.root, expanded, 1)], [tree, expanded]);
  const visible = useMemo(() => flattenVisible(roots), [roots]);
  const indexByID = useMemo(() => new Map(visible.map((node, index) => [node.id, index])), [visible]);
  const treeRef = useRef<HTMLDivElement>(null);
  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const pendingRefocusIDRef = useRef<string | null>(null);
  const [focusedID, setFocusedID] = useState<string | null>(selectedID ?? visible[0]?.id ?? null);
  const effectiveFocusedID =
    focusedID && indexByID.has(focusedID)
      ? focusedID
      : selectedID && indexByID.has(selectedID)
        ? selectedID
        : (visible[0]?.id ?? null);

  const focusRow = useCallback((id: string) => {
    rowRefs.current.get(id)?.focus();
  }, []);

  useImperativeHandle(ref, () => ({ focusRow }), [focusRow]);

  if (
    focusedID !== null &&
    !indexByID.has(focusedID) &&
    effectiveFocusedID !== null &&
    treeRef.current?.contains(document.activeElement)
  ) {
    pendingRefocusIDRef.current = effectiveFocusedID;
  }

  useLayoutEffect(() => {
    if (pendingRefocusIDRef.current === null) return;
    const id = pendingRefocusIDRef.current;
    pendingRefocusIDRef.current = null;
    rowRefs.current.get(id)?.focus();
  });

  function setExpanded(nodeID: string, nextExpanded: boolean) {
    const nextIDs = nextExpanded ? pushUnique(localExpandedIDs, nodeID) : removeID(localExpandedIDs, nodeID);
    setLocalExpandedIDs(nextIDs);
    onExpandedChange(nextIDs);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>, node: RenderNode) {
    const index = indexByID.get(node.id);
    if (index === undefined) return;
    switch (event.key) {
      case "ArrowDown": {
        event.preventDefault();
        const next = visible[index + 1];
        if (next) focusRow(next.id);
        break;
      }
      case "ArrowUp": {
        event.preventDefault();
        const previous = visible[index - 1];
        if (previous) focusRow(previous.id);
        break;
      }
      case "ArrowRight": {
        event.preventDefault();
        if (node.hasChildren && !node.expanded) setExpanded(node.id, true);
        else if (node.hasChildren) {
          const firstChild = node.children[0];
          if (firstChild) focusRow(firstChild.id);
        }
        break;
      }
      case "ArrowLeft": {
        event.preventDefault();
        if (node.hasChildren && node.expanded) setExpanded(node.id, false);
        else if (node.parentID) focusRow(node.parentID);
        break;
      }
      case "Enter":
      case " ":
      case "Spacebar":
      case "Space": {
        event.preventDefault();
        onSelect(node.id);
        break;
      }
      default:
        break;
    }
  }

  function renderNodes(nodes: RenderNode[]): ReactNode[] {
    return nodes.flatMap((node) => {
      const selected = node.id === selectedID;
      const continuationFailure = continuationFailures[node.id];
      const row = (
        <div
          key={node.id}
          ref={(element) => {
            if (element) rowRefs.current.set(node.id, element);
            else rowRefs.current.delete(node.id);
          }}
          role="treeitem"
          aria-label={node.label}
          aria-level={node.level}
          aria-expanded={node.hasChildren ? node.expanded : undefined}
          aria-selected={selected ? "true" : undefined}
          tabIndex={node.id === effectiveFocusedID ? 0 : -1}
          className={`${CLASS.row}${selected ? ` ${CLASS.rowSelected}` : ""}`}
          onFocus={() => setFocusedID(node.id)}
          onKeyDown={(event) => handleKeyDown(event, node)}
          onClick={() => onSelect(node.id)}
        >
          <div className={CLASS.rowContent}>
            {node.hasChildren ? (
              <button
                type="button"
                tabIndex={-1}
                className={CLASS.rowToggle}
                onClick={(event: MouseEvent<HTMLButtonElement>) => {
                  event.stopPropagation();
                  setExpanded(node.id, !node.expanded);
                }}
              >
                <Chevron direction={node.expanded ? "down" : "right"} />
              </button>
            ) : (
              <span className={CLASS.rowToggle} aria-hidden="true" />
            )}
            <StatusDot
              state={statusDotState(
                node.statusText,
                node.selection.kind === "job" ? node.selection.job.terminal : undefined,
              )}
            />
            <div className={CLASS.rowMain}>
              <span className={CLASS.rowLabel}>{node.label}</span>
              {node.detail && <span className={CLASS.rowDetail}>{node.detail}</span>}
              {node.branchError && <span className={CLASS.rowDetail}>{node.branchError}</span>}
            </div>
            <div className={CLASS.rowMeta}>
              <span className={CLASS.rowStatusText}>{node.statusText}</span>
            </div>
          </div>
          {(node.continuation || continuationFailure) &&
            onContinue &&
            (() => {
              const continuation = node.continuation;
              return (
                <div className={CLASS.rowActions}>
                  <span className={CLASS.rowContinuation}>
                    {continuationFailure ?? node.branchError ?? "This branch is partially retained."}
                  </span>
                  {continuation && (
                    <Button
                      variant="quiet"
                      size="xs"
                      tabIndex={-1}
                      disabled={loadingContinuationID === continuation.targetID}
                      onClick={(event) => {
                        event.stopPropagation();
                        onContinue(continuation.targetID, continuation.token);
                      }}
                    >
                      {loadingContinuationID === continuation.targetID ? "Loading…" : "Load more"}
                    </Button>
                  )}
                </div>
              );
            })()}
        </div>
      );
      if (!(node.hasChildren && node.expanded)) return [row];
      return [
        row,
        // role="group" is the WAI-ARIA treeview pattern's nested-children container.
        // biome-ignore lint/a11y/useSemanticElements: role="group" is deliberate tree semantics
        <div role="group" className={CLASS.group} key={`${node.id}-group`}>
          {renderNodes(node.children)}
        </div>,
      ];
    });
  }

  return (
    <div ref={treeRef} role="tree" className={CLASS.tree}>
      {renderNodes(roots)}
    </div>
  );
});
