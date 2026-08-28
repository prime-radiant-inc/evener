import { usageAxLabel, workItemAxLabel } from "../accessibility-semantics";
import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveWorkItem } from "../model";
import { formatDuration, formatUsage } from "../shared/format";
import { StatusDisclosure } from "./StatusDisclosure";

// Live adaptation of the Field Notes work surface. Renders the live work tree
// (LiveWorkItem already carries children) with parent-id lists, visible
// ledger annotations, current-record work markers, and live usage. No
// fixture, no prototype primitives.

export interface WorkViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

function WorkTree({
  items,
  parentKey,
  parentTitle,
  depth,
  state,
  dispatch,
}: {
  items: readonly LiveWorkItem[];
  parentKey: string | null;
  parentTitle: string | null;
  depth: number;
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}) {
  if (items.length === 0) return null;
  return (
    <ul
      className="fn-work-tree"
      data-relationship-list
      data-work-ledger={depth === 0 ? "true" : undefined}
      aria-label={`Work items level ${depth + 1}`}
    >
      {items.map((item) => (
        <li key={item.key}>
          <article
            className="fn-work-node"
            data-work-node-id={item.key}
            data-work-kind={item.kind}
            data-work-parent-id={parentKey ?? "root"}
            data-work-depth={depth}
            data-current-work={item.tone === "running" ? "true" : undefined}
            aria-label={workItemAxLabel(item)}
          >
            <p className="fn-work-node__context" data-ledger-annotation>
              Level {depth + 1} · Parent: {parentTitle ?? "Session"}
            </p>
            <StatusDisclosure
              label={item.title}
              status={item.tone}
              expanded={state.ui.expandedWorkKeys.has(item.key)}
              onToggle={() => dispatch({ type: "toggleWork", key: item.key })}
            >
              <div className="fn-work-node__detail">
                <dl>
                  <div>
                    <dt>Type</dt>
                    <dd>{item.kind}</dd>
                  </div>
                  <div>
                    <dt>Detail</dt>
                    <dd>{item.detail}</dd>
                  </div>
                </dl>
              </div>
            </StatusDisclosure>
          </article>
          <WorkTree
            items={item.children}
            parentKey={item.key}
            parentTitle={item.title}
            depth={depth + 1}
            state={state}
            dispatch={dispatch}
          />
        </li>
      ))}
    </ul>
  );
}

export function WorkView({ state, dispatch }: WorkViewProps) {
  const { activity, conversation, connection } = state;
  if (!activity) {
    return (
      <section className="fn-inline-state">
        <h2>No work available</h2>
        <p>Open a record with active work to inspect its hierarchy.</p>
      </section>
    );
  }

  const offline =
    connection.status === "offline" || connection.status === "error";
  const workItems = activity.work;
  const usage = activity.usage;

  return (
    <div className="fn-work-panel fn-route-enter">
      {offline ? (
        <div
          className="fn-banner"
          data-connection-status={connection.status}
          role="status"
          aria-live="polite"
        >
          <span>Offline. Work evidence remains locally readable.</span>
        </div>
      ) : null}

      <section className="fn-work-heading">
        <p className="fn-eyebrow">
          {conversation?.project ?? "Live record"} · One layer down
        </p>
        <h2>{conversation?.title ?? "Work ledger"}</h2>
        <p>Tasks, delegates, and jobs retain their canonical hierarchy.</p>
      </section>

      <section className="fn-work-tasks" aria-label="Task summary">
        {activity.tasks.map((task) => (
          <span key={task.status}>
            {task.count} {task.status}
          </span>
        ))}
      </section>

      {workItems.length === 0 ? (
        <section className="fn-inline-state">
          <h2>No active work</h2>
          <p>This record has no work hierarchy to disclose.</p>
        </section>
      ) : (
        <section className="fn-work-list" aria-labelledby="fn-work-list-title">
          <header>
            <h2 id="fn-work-list-title">Work hierarchy</h2>
            <span>{countWork(workItems)} items</span>
          </header>
          <WorkTree
            items={workItems}
            parentKey={null}
            parentTitle={conversation?.title ?? null}
            depth={0}
            state={state}
            dispatch={dispatch}
          />
        </section>
      )}

      <section className="fn-usage" data-work-usage aria-label={usageAxLabel()}>
        <header>
          <p className="fn-eyebrow">Resource view</p>
          <h2 id="fn-usage-title">Usage</h2>
        </header>
        <dl>
          <div>
            <dt>Tokens</dt>
            <dd>
              {usage.totalTokens != null ? formatUsage(usage.totalTokens) : "—"}
            </dd>
          </div>
          <div>
            <dt>Cost</dt>
            <dd>{usage.cost ?? "—"}</dd>
          </div>
          <div>
            <dt>Duration</dt>
            <dd>
              {usage.durationMs != null
                ? formatDuration(usage.durationMs)
                : "—"}
            </dd>
          </div>
          <div>
            <dt>Context</dt>
            <dd>
              {usage.contextPressure != null
                ? `${Math.round(usage.contextPressure * 100)}%`
                : "—"}
            </dd>
          </div>
        </dl>
        {usage.contextPressure != null ? (
          <div
            className="fn-context-meter"
            role="progressbar"
            aria-label="Context used"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(usage.contextPressure * 100)}
          >
            <span
              style={{ width: `${Math.round(usage.contextPressure * 100)}%` }}
            />
          </div>
        ) : null}
      </section>
    </div>
  );
}

function countWork(items: readonly LiveWorkItem[]): number {
  let total = 0;
  for (const item of items) {
    total += 1;
    total += countWork(item.children);
  }
  return total;
}
