import type { PlatformPrimitives } from "../../core/platform";
import {
  buildWorkHierarchy,
  type WorkHierarchyItem,
} from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Disclosure } from "../shared/Disclosure";
import { formatUsage } from "../shared/format";
import { ScreenState } from "../shared/ScreenState";
import { StatusLabel } from "../shared/StatusLabel";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";

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
    <ul className="sw-work-tree" aria-label={`Work items level ${level + 1}`}>
      {items.map((item) => (
        <li key={item.node.id}>
          <article
            className="sw-work-node"
            data-work-node-id={item.node.id}
            data-work-kind={item.node.kind}
            data-work-parent-id={item.node.parentId ?? "root"}
            data-work-depth={item.depth}
          >
            <p className="sw-work-node__context">
              Level {item.depth + 1} · Parent: {item.parentTitle ?? "Session"}
            </p>
            <Disclosure
              summary={<span>{item.node.title}</span>}
              expanded={state.expandedWorkIds.has(item.node.id)}
              onToggle={() =>
                dispatch({ type: "toggleWork", nodeId: item.node.id })
              }
            >
              <div className="sw-work-node__detail">
                <StatusLabel state={item.node.state} />
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
            </Disclosure>
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
      className="sw-work-panel sw-route-enter"
      data-work-presentation={primitives.sheet}
    >
      <OfflineNotice
        state={state}
        routeLabel="Work"
        mutationDetail="Task evidence and disclosure remain locally readable."
        dispatch={dispatch}
      />
      <section className="sw-work-heading">
        <p className="sw-eyebrow">{session.project} · One layer down</p>
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
        <section className="sw-work-list" aria-labelledby="sw-work-list-title">
          <header>
            <h2 id="sw-work-list-title">Work hierarchy</h2>
            <span>{nodes.length} items</span>
          </header>
          <WorkTree items={hierarchy} state={state} dispatch={dispatch} />
        </section>
      )}

      <section
        className="sw-usage"
        data-work-usage
        aria-labelledby="sw-usage-title"
      >
        <header>
          <p className="sw-eyebrow">Resource view</p>
          <h2 id="sw-usage-title">Usage</h2>
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
          className="sw-context-meter"
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
