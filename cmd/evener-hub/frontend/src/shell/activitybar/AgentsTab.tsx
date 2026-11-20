import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The selected session's subtree delegates: active branches stay visible and
// nested under their parent, while settled runs fold into one bottom
// disclosure grouped by parent. Visible page boundaries add demand to the
// shared owner without acquiring another reader or retry loop.

import {
  activityNodeID,
  isDisclosureOpenIn,
  resolveDelegateParents,
  type SessionDelegate,
} from "@evener/appwire-client";
import { Fragment, type ReactNode, useMemo } from "react";
import { openTranscript } from "../../panes/session/transcript/openTranscript";
import { enterAgentCascade } from "../../panes/zoom/actions";
import { useSessionActivity } from "../../stores/sessionActivity";
import { Chevron, Disclosure, EmptyState, IconButton, InlineNotice, Skeleton } from "../../widgets";
import { isDisclosureOpen, setDisclosureOpen, useDisclosureSnapshot } from "../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../widgets/internal/requireClass";
import { isMobileViewport } from "../useIsMobile";
import { useActivityScrollProgress } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import { activityPlaceholderLines } from "./activityPlaceholders";
import { AgentRow, delegateRowName } from "./activityRows";
import type { ActivityTabBodyProps } from "./activityTabs";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  childBranch: requireClass(styles.childBranch, "activitybar.module.css", "childBranch"),
};

const MAX_VISUAL_DEPTH = 3;
const EMPTY_ROWS: readonly SessionDelegate[] = [];

interface DelegateTreeNode {
  id: string;
  sub: SessionDelegate;
  children: DelegateTreeNode[];
  chain: readonly SessionDelegate[];
  hasActive: boolean;
  hasSettled: boolean;
  settledCount: number;
}

function buildDelegateTree(rows: readonly SessionDelegate[], rootRefs: readonly string[]): DelegateTreeNode[] {
  const { parentOf, cyclic } = resolveDelegateParents(rows, rootRefs);
  const nodeByRow = new Map<SessionDelegate, DelegateTreeNode>();
  for (const row of rows) {
    nodeByRow.set(row, {
      id: activityNodeID({ ...row, kind: "delegate" }),
      sub: row,
      children: [],
      chain: [],
      hasActive: !row.terminal,
      hasSettled: row.terminal === true,
      settledCount: 0,
    });
  }
  const roots: DelegateTreeNode[] = [];
  for (const node of nodeByRow.values()) {
    const parent = !cyclic.has(node.sub) ? parentOf.get(node.sub) : undefined;
    const parentNode = parent ? nodeByRow.get(parent) : undefined;
    if (parentNode) parentNode.children.push(node);
    else roots.push(node);
  }
  const finalize = (node: DelegateTreeNode, ancestors: readonly SessionDelegate[]): void => {
    node.chain = [...ancestors, node.sub];
    for (const child of node.children) {
      finalize(child, node.chain);
      if (child.hasActive) node.hasActive = true;
      if (child.hasSettled) node.hasSettled = true;
      node.settledCount += (child.sub.terminal ? 1 : 0) + child.settledCount;
    }
  };
  for (const root of roots) finalize(root, []);
  return roots;
}

function branchFoldID(foldPrefix: string, node: DelegateTreeNode): string {
  return `${foldPrefix}\0branch\0${node.id}`;
}

function visibleChildrenOf(node: DelegateTreeNode, mode: "active" | "inactive"): DelegateTreeNode[] {
  return node.children.filter((child) => (mode === "active" ? child.hasActive : child.hasSettled));
}

function countVisibleActiveRows(
  node: DelegateTreeNode,
  foldPrefix: string,
  defaultExpanded: boolean,
  isOpen: (id: string, fallback: boolean) => boolean,
): number {
  if (!isOpen(branchFoldID(foldPrefix, node), defaultExpanded)) return 0;
  let count = 0;
  for (const child of visibleChildrenOf(node, "active")) {
    if (!child.sub.terminal) count += 1;
    count += countVisibleActiveRows(child, foldPrefix, defaultExpanded, isOpen);
  }
  return count;
}

function branchClass(depth: number): string | undefined {
  return depth < MAX_VISUAL_DEPTH ? CLASS.childBranch : undefined;
}

function branchToggle(name: string, expanded: boolean, onToggle: () => void): ReactNode {
  return (
    <IconButton
      size="xs"
      variant="quiet"
      label={expanded ? `Collapse ${name}` : `Expand ${name}`}
      aria-expanded={expanded}
      onClick={onToggle}
      icon={<Chevron direction={expanded ? "down" : "right"} size={12} />}
    />
  );
}

function AgentBranch({
  node,
  foldPrefix,
  defaultExpanded,
  mode,
  depth,
  now,
  onDrill,
}: {
  node: DelegateTreeNode;
  foldPrefix: string;
  defaultExpanded: boolean;
  mode: "active" | "inactive";
  depth: number;
  now: number;
  onDrill: (sub: SessionDelegate, chain: readonly SessionDelegate[]) => void;
}) {
  const foldKey = branchFoldID(foldPrefix, node);
  const expanded = isDisclosureOpen(foldKey, defaultExpanded);
  const visibleChildren = visibleChildrenOf(node, mode);
  const hasChildren = visibleChildren.length > 0;
  // Group headers fold their children into a disclosure of their own, so only a
  // row path needs the visible-row count behind omitSubagentWait.
  const showsChildren = mode === "active" && hasChildren && expanded && !node.sub.terminal;
  const visibleActiveRows = useDisclosureSnapshot((state) =>
    showsChildren
      ? countVisibleActiveRows(node, foldPrefix, defaultExpanded, (id, fallback) =>
          isDisclosureOpenIn(state, id, fallback),
        )
      : 0,
  );
  const name = delegateRowName(node.sub);
  const renderedChildren = visibleChildren.map((child) => (
    <AgentBranch
      key={child.id}
      node={child}
      foldPrefix={foldPrefix}
      defaultExpanded={defaultExpanded}
      mode={mode}
      depth={depth + 1}
      now={now}
      onDrill={onDrill}
    />
  ));
  const indentClass = branchClass(depth);
  if (mode === "inactive" && !node.sub.terminal) {
    if (!hasChildren) return null;
    return (
      <Disclosure
        open={expanded}
        onOpenChange={(next) => setDisclosureOpen(foldKey, next)}
        summary={<span>{`${name} (${node.settledCount})`}</span>}
      >
        <div className={indentClass ?? CLASS.stack}>{renderedChildren}</div>
      </Disclosure>
    );
  }
  // A settled run that still holds open descendants stays in the active tree as
  // a group header only: its one selectable row and scroll anchor live in the
  // inactive history fold, so no settled run is selectable in two places.
  if (mode === "active" && node.sub.terminal) {
    if (!hasChildren) return null;
    return (
      <Disclosure id={foldKey} defaultOpen={defaultExpanded} summary={<span>{name}</span>}>
        <div className={indentClass ?? CLASS.stack}>{renderedChildren}</div>
      </Disclosure>
    );
  }
  return (
    <Fragment>
      <AgentRow
        sub={node.sub}
        anchorId={node.id}
        now={now}
        omitSubagentWait={showsChildren && visibleActiveRows >= (node.sub.runningSubagentCount ?? 0)}
        trailingAction={
          hasChildren ? branchToggle(name, expanded, () => setDisclosureOpen(foldKey, !expanded)) : undefined
        }
        onDrill={() => onDrill(node.sub, node.chain)}
      />
      {hasChildren && expanded ? (
        indentClass ? (
          <div className={indentClass}>{renderedChildren}</div>
        ) : (
          renderedChildren
        )
      ) : null}
    </Fragment>
  );
}

function drill(sub: SessionDelegate, chain: readonly SessionDelegate[] = [sub], scopeRef?: string): void {
  if (isMobileViewport()) openTranscript(sub.childRef, sub.ownerRef);
  else enterAgentCascade(chain, undefined, scopeRef);
}

function drillInto(scopeRef: string): (sub: SessionDelegate, chain?: readonly SessionDelegate[]) => void {
  return (sub, chain) => drill(sub, chain, scopeRef);
}

export function AgentsTab({ scope, now, onDrill }: ActivityTabBodyProps) {
  const handleDrill = onDrill ?? drillInto(scope.leaf.ref);
  const foldID = `${scope.leaf.ref}\0inactive-delegates`;
  const activeBranchPrefix = `${scope.leaf.ref}\0active`;
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "subtree", "delegates");
  const collection = snapshot?.delegates;
  const summary = snapshot?.summary?.delegates;
  const inactiveTotal = summary?.known ? summary.total - summary.active : null;
  const rows = collection?.rows ?? EMPTY_ROWS;
  const contextRef = snapshot?.context?.ref;
  const roots = useMemo(
    () => buildDelegateTree(rows, contextRef ? [contextRef, scope.leaf.ref] : [scope.leaf.ref]),
    [rows, contextRef, scope.leaf.ref],
  );
  const activeRoots = roots.filter((node) => node.hasActive);
  const settledRoots = roots.filter((node) => node.hasSettled);
  const scrollIds = useMemo(() => rows.map((sub) => activityNodeID({ ...sub, kind: "delegate" })), [rows]);
  useActivityScrollProgress(scrollIds, collection?.complete ?? false, collection?.hasMore ?? false);
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
      {activeRoots.map((node) => (
        <AgentBranch
          key={node.id}
          node={node}
          foldPrefix={activeBranchPrefix}
          defaultExpanded={true}
          mode="active"
          depth={0}
          now={now}
          onDrill={handleDrill}
        />
      ))}
      {settledRoots.length > 0 ? (
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
            {settledRoots.map((node) => (
              <AgentBranch
                key={node.id}
                node={node}
                foldPrefix={foldID}
                defaultExpanded={false}
                mode="inactive"
                depth={0}
                now={now}
                onDrill={handleDrill}
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
