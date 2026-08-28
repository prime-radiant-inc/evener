import { usageAxLabel, workItemAxLabel } from "../accessibility-semantics";
import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveWorkItem } from "../model";
import { Disclosure } from "../shared/Disclosure";
import { formatDuration, formatUsage } from "../shared/format";
import { StatusLabel } from "../shared/StatusLabel";

export interface WorkViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

function WorkTree({
  items,
  state,
  dispatch,
}: {
  items: readonly LiveWorkItem[];
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}) {
  if (items.length === 0) return null;
  return (
    <ul className="sw-work-tree">
      {items.map((item) => (
        <li key={item.key}>
          <article
            className="sw-work-node"
            data-work-node-id={item.key}
            data-work-kind={item.kind}
            aria-label={workItemAxLabel(item)}
          >
            <Disclosure
              summary={<span>{item.title}</span>}
              expanded={state.ui.expandedWorkKeys.has(item.key)}
              onToggle={() => dispatch({ type: "toggleWork", key: item.key })}
            >
              <div className="sw-work-node__detail">
                <StatusLabel state={item.tone} />
                <dl>
                  <div>
                    <dt>Type</dt>
                    <dd>{item.kind}</dd>
                  </div>
                </dl>
                <p>{item.detail}</p>
              </div>
            </Disclosure>
          </article>
          <WorkTree items={item.children} state={state} dispatch={dispatch} />
        </li>
      ))}
    </ul>
  );
}

export function WorkView({ state, dispatch }: WorkViewProps) {
  const { activity } = state;
  if (!activity) {
    return (
      <section className="sw-inline-state">
        <h2>No work</h2>
        <p>No activity is connected to this session.</p>
      </section>
    );
  }

  const usage = activity.usage;
  const durationLabel = usage.durationMs
    ? formatDuration(usage.durationMs)
    : "—";
  const tokenLabel = usage.totalTokens ? formatUsage(usage.totalTokens) : "—";
  const contextLabel =
    usage.contextPressure !== undefined
      ? `${Math.round(usage.contextPressure * 100)}%`
      : "—";

  return (
    <div className="sw-work-panel">
      <section className="sw-work-heading">
        <p className="sw-eyebrow">Work</p>
        <h2>Activity hierarchy</h2>
        <p>Tasks, subagents, jobs, and watches retain their hierarchy.</p>
      </section>

      <section className="sw-work-tasks">
        {activity.tasks.map((task) => (
          <span key={task.status}>
            {task.count} {task.status}
          </span>
        ))}
      </section>

      {activity.work.length === 0 ? (
        <section className="sw-inline-state">
          <h2>No active work</h2>
          <p>There is no task hierarchy to disclose.</p>
        </section>
      ) : (
        <section className="sw-work-list" aria-labelledby="sw-work-list-title">
          <header>
            <h2 id="sw-work-list-title">Work hierarchy</h2>
            <span>{activity.work.length} items</span>
          </header>
          <WorkTree items={activity.work} state={state} dispatch={dispatch} />
        </section>
      )}

      <section className="sw-usage" data-work-usage aria-label={usageAxLabel()}>
        <header>
          <p className="sw-eyebrow">Resource view</p>
          <h2 id="sw-usage-title">Usage</h2>
        </header>
        <dl>
          <div>
            <dt>Tokens</dt>
            <dd>{tokenLabel}</dd>
          </div>
          <div>
            <dt>Cost</dt>
            <dd>{usage.cost ?? "—"}</dd>
          </div>
          <div>
            <dt>Duration</dt>
            <dd>{durationLabel}</dd>
          </div>
          <div>
            <dt>Context</dt>
            <dd>{contextLabel}</dd>
          </div>
        </dl>
      </section>
    </div>
  );
}
