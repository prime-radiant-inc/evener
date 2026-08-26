import type { PlatformPrimitives } from "../../core/platform";
import {
  buildWorkHierarchy,
  type WorkHierarchyItem,
} from "../../core/selectors";
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
      className="co-work-tree"
      data-relationship-list
      aria-label={`Work items level ${level + 1}`}
    >
      {items.map((item) => (
        <li key={item.node.id}>
          <article
            className="co-work-node"
            data-work-node-id={item.node.id}
            data-work-kind={item.node.kind}
            data-work-parent-id={item.node.parentId ?? "root"}
            data-work-depth={item.depth}
            data-current-work={
              item.node.state === "running" ? "true" : undefined
            }
          >
            <p className="co-work-node__context">
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
              <div className="co-work-node__detail">
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
      className="co-work-panel co-route-enter"
      data-work-presentation={primitives.sheet}
    >
      <OfflineNotice
        state={state}
        routeLabel="Work"
        mutationDetail="Task evidence and disclosure remain locally readable."
        dispatch={dispatch}
      />
      <section className="co-work-heading">
        <p className="co-eyebrow">{session.project} · One layer down</p>
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
        <section className="co-work-list" aria-labelledby="co-work-list-title">
          <header>
            <h2 id="co-work-list-title">Work hierarchy</h2>
            <span>{nodes.length} items</span>
          </header>
          <WorkTree items={hierarchy} state={state} dispatch={dispatch} />
        </section>
      )}

      <section
        className="co-usage"
        data-work-usage
        aria-labelledby="co-usage-title"
      >
        <header>
          <p className="co-eyebrow">Resource view</p>
          <h2 id="co-usage-title">Usage</h2>
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
          className="co-context-meter"
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
