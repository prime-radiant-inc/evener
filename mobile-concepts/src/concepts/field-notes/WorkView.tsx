import type { WorkNode } from "../../core/model";
import type { PlatformPrimitives } from "../../core/platform";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { formatUsage } from "../shared/format";
import { ScreenState } from "../shared/ScreenState";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";
import { StatusDisclosure } from "./StatusDisclosure";

export interface WorkViewProps {
  state: PrototypeState;
  sessionId: string;
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
}

interface WorkHierarchyItem {
  node: WorkNode;
  depth: number;
  parentTitle: string | null;
  children: readonly WorkHierarchyItem[];
}

export function buildWorkHierarchy(
  nodes: readonly WorkNode[],
): readonly WorkHierarchyItem[] {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const childrenByParent = new Map<string, WorkNode[]>();
  const roots: WorkNode[] = [];
  for (const node of nodes) {
    if (
      node.parentId === null ||
      node.parentId === node.id ||
      !byId.has(node.parentId)
    ) {
      roots.push(node);
      continue;
    }
    const children = childrenByParent.get(node.parentId) ?? [];
    children.push(node);
    childrenByParent.set(node.parentId, children);
  }

  const visited = new Set<string>();
  const buildItem = (
    node: WorkNode,
    depth: number,
    ancestors: ReadonlySet<string>,
  ): WorkHierarchyItem => {
    visited.add(node.id);
    const nextAncestors = new Set(ancestors);
    nextAncestors.add(node.id);
    const children = (childrenByParent.get(node.id) ?? [])
      .filter((child) => !nextAncestors.has(child.id) && !visited.has(child.id))
      .map((child) => buildItem(child, depth + 1, nextAncestors));
    return {
      node,
      depth,
      parentTitle: node.parentId
        ? (byId.get(node.parentId)?.title ?? null)
        : null,
      children,
    };
  };

  const hierarchy = roots.map((node) => buildItem(node, 0, new Set()));
  for (const node of nodes) {
    if (!visited.has(node.id)) {
      hierarchy.push(buildItem(node, 0, new Set()));
    }
  }
  return hierarchy;
}

function WorkTree({
  items,
  state,
  dispatch,
}: {
  items: readonly WorkHierarchyItem[];
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}) {
  if (items.length === 0) return null;
  const level = items[0]?.depth ?? 0;
  return (
    <ul
      className="fn-work-tree"
      data-relationship-list
      data-work-ledger={level === 0 ? "true" : undefined}
      aria-label={`Work items level ${level + 1}`}
    >
      {items.map((item) => (
        <li key={item.node.id}>
          <article
            className="fn-work-node"
            data-work-node-id={item.node.id}
            data-work-kind={item.node.kind}
            data-work-parent-id={item.node.parentId ?? "root"}
            data-work-depth={item.depth}
            data-current-work={
              item.node.state === "running" ? "true" : undefined
            }
          >
            <p className="fn-work-node__context" data-ledger-annotation>
              Level {item.depth + 1} · Parent: {item.parentTitle ?? "Session"}
            </p>
            <StatusDisclosure
              label={item.node.title}
              status={item.node.state}
              expanded={state.expandedWorkIds.has(item.node.id)}
              onToggle={() =>
                dispatch({ type: "toggleWork", nodeId: item.node.id })
              }
            >
              <div className="fn-work-node__detail">
                <dl>
                  <div>
                    <dt>Type</dt>
                    <dd>{item.node.kind}</dd>
                  </div>
                  <div>
                    <dt>Phase</dt>
                    <dd>{item.node.phase}</dd>
                  </div>
                  <div>
                    <dt>Elapsed</dt>
                    <dd>{item.node.elapsedLabel}</dd>
                  </div>
                </dl>
                <p>{item.node.output}</p>
              </div>
            </StatusDisclosure>
          </article>
          <WorkTree items={item.children} state={state} dispatch={dispatch} />
        </li>
      ))}
    </ul>
  );
}

export function WorkView({
  state,
  sessionId,
  primitives,
  dispatch,
}: WorkViewProps) {
  const session = state.projection.fixture.sessions.find(
    ({ id }) => id === sessionId,
  );
  if (isBlockingRouteState(state)) {
    return (
      <BlockingRouteState state={state} routeLabel="Work" dispatch={dispatch} />
    );
  }
  if (!session || state.selectedSessionId !== sessionId) {
    return (
      <ScreenState
        state={{
          kind: "error",
          title: "Work unavailable",
          detail: "The selected session is not present in this fixture.",
        }}
        retryAction={() => dispatch({ type: "goBack" })}
      />
    );
  }
  const nodes = state.projection.fixture.work.filter(
    (node) => node.sessionId === sessionId,
  );
  const hierarchy = buildWorkHierarchy(nodes);
  const usage = state.projection.fixture.usage;

  return (
    <div
      className="fn-work-panel fn-route-enter"
      data-work-presentation={primitives.sheet}
    >
      <OfflineNotice
        state={state}
        routeLabel="Work"
        mutationDetail="Task evidence and disclosure remain locally readable."
        dispatch={dispatch}
      />
      <section className="fn-work-heading">
        <p className="fn-eyebrow">{session.project} · One layer down</p>
        <h2>{session.title}</h2>
        <p>Tasks, subagents, and jobs retain their canonical hierarchy.</p>
      </section>

      {nodes.length === 0 ? (
        <ScreenState
          state={{
            kind: "empty",
            title: "No active work",
            detail: "This completed fixture has no task hierarchy to disclose.",
          }}
        />
      ) : (
        <section className="fn-work-list" aria-labelledby="fn-work-list-title">
          <header>
            <h2 id="fn-work-list-title">Work hierarchy</h2>
            <span>{nodes.length} items</span>
          </header>
          <WorkTree items={hierarchy} state={state} dispatch={dispatch} />
        </section>
      )}

      <section
        className="fn-usage"
        data-work-usage
        aria-labelledby="fn-usage-title"
      >
        <header>
          <p className="fn-eyebrow">Resource view</p>
          <h2 id="fn-usage-title">Usage</h2>
        </header>
        <dl>
          <div>
            <dt>Tokens</dt>
            <dd>{formatUsage(usage.tokens)}</dd>
          </div>
          <div>
            <dt>Cost</dt>
            <dd>{usage.costLabel}</dd>
          </div>
          <div>
            <dt>Duration</dt>
            <dd>{usage.durationLabel}</dd>
          </div>
          <div>
            <dt>Context</dt>
            <dd>{usage.contextPercent}%</dd>
          </div>
        </dl>
        <div
          className="fn-context-meter"
          role="progressbar"
          aria-label="Context used"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={usage.contextPercent}
        >
          <span style={{ width: `${usage.contextPercent}%` }} />
        </div>
      </section>
    </div>
  );
}
