import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveActivityView, LiveWorkItem } from "../model";
import { Disclosure } from "../shared/Disclosure";
import { formatDuration, formatUsage } from "../shared/format";
import { StatusLabel } from "../shared/StatusLabel";

export interface WorkViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

interface FlattenedWorkNode {
  node: LiveWorkItem;
  parentId: string;
  depth: number;
  parentTitle: string | null;
  children: FlattenedWorkNode[];
}

function flattenWork(
  work: readonly LiveWorkItem[],
  parentId: string = "root",
  depth: number = 0,
  parentTitle: string | null = null,
): FlattenedWorkNode[] {
  return work.map((node) => ({
    node,
    parentId,
    depth,
    parentTitle,
    children: flattenWork(node.children, node.key, depth + 1, node.title),
  }));
}

function WorkTree({
  items,
  state,
  dispatch,
}: {
  items: readonly FlattenedWorkNode[];
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}) {
  if (items.length === 0) return null;
  const level = items[0]?.depth ?? 0;
  return (
    <ul
      className="co-work-tree"
      data-relationship-list
      data-testid="relationship-list"
      aria-label={`Work items level ${level + 1}`}
    >
      {items.map((item) => (
        <li key={item.node.key}>
          <article
            className="co-work-node"
            data-work-node-id={item.node.key}
            data-testid={`work-node-${item.node.key}`}
            data-work-kind={item.node.kind}
            data-work-parent-id={item.parentId}
            data-work-depth={item.depth}
            data-current-work={
              item.node.tone === "running" ? "true" : undefined
            }
          >
            <p className="co-work-node__context">
              Level {item.depth + 1} · Parent: {item.parentTitle ?? "Session"}
            </p>
            <Disclosure
              summary={<span>{item.node.title}</span>}
              expanded={state.ui.expandedWorkKeys.has(item.node.key)}
              onToggle={() =>
                dispatch({ type: "toggleWork", key: item.node.key })
              }
            >
              <div className="co-work-node__detail">
                <StatusLabel state={item.node.tone} />
                <dl>
                  <div>
                    <dt>Type</dt>
                    <dd>{item.node.kind}</dd>
                  </div>
                  <div>
                    <dt>Detail</dt>
                    <dd>{item.node.detail}</dd>
                  </div>
                </dl>
              </div>
            </Disclosure>
          </article>
          <WorkTree items={item.children} state={state} dispatch={dispatch} />
        </li>
      ))}
    </ul>
  );
}

function hasWork(
  activity: LiveActivityView | null,
): activity is LiveActivityView {
  return activity !== null && activity.work.length > 0;
}

export function WorkView({ state, dispatch }: WorkViewProps) {
  const activity = state.activity;

  if (!hasWork(activity)) {
    return (
      <div className="co-work-panel co-route-enter">
        <section className="co-inline-state">
          <h2>No active work</h2>
          <p>This session has no task hierarchy to disclose right now.</p>
        </section>
      </div>
    );
  }

  const work = activity.work;
  const usage = activity.usage;
  const hierarchy = flattenWork(work);

  return (
    <div className="co-work-panel co-route-enter">
      <section className="co-work-heading">
        <p className="co-eyebrow">One layer down</p>
        <h2>Work constellation</h2>
        <p>Tasks, subagents, and jobs retain their canonical hierarchy.</p>
      </section>

      <section className="co-work-tasks" aria-label="Task summary">
        {activity.tasks.map((task) => (
          <span key={task.status}>
            {task.count} {task.status}
          </span>
        ))}
      </section>

      <section className="co-work-list" aria-labelledby="co-work-list-title">
        <header>
          <h2 id="co-work-list-title">Work hierarchy</h2>
          <span>{work.length} items</span>
        </header>
        <WorkTree items={hierarchy} state={state} dispatch={dispatch} />
      </section>

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
          {usage.totalTokens !== undefined ? (
            <div>
              <dt>Tokens</dt>
              <dd>{formatUsage(usage.totalTokens)}</dd>
            </div>
          ) : null}
          {usage.cost !== undefined ? (
            <div>
              <dt>Cost</dt>
              <dd>{usage.cost}</dd>
            </div>
          ) : null}
          {usage.durationMs !== undefined ? (
            <div>
              <dt>Duration</dt>
              <dd>{formatDuration(usage.durationMs)}</dd>
            </div>
          ) : null}
          {usage.contextPressure !== undefined ? (
            <div>
              <dt>Context</dt>
              <dd>{usage.contextPressure}%</dd>
            </div>
          ) : null}
        </dl>
        {usage.contextPressure !== undefined ? (
          <div
            className="co-context-meter"
            role="progressbar"
            aria-label="Context used"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={usage.contextPressure}
          >
            <span style={{ width: `${usage.contextPressure}%` }} />
          </div>
        ) : null}
      </section>
    </div>
  );
}
