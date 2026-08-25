import type { PlatformPrimitives } from "../../core/platform";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Disclosure } from "../shared/Disclosure";
import { formatUsage } from "../shared/format";
import { ScreenState } from "../shared/ScreenState";
import { StatusLabel } from "../shared/StatusLabel";

export interface WorkViewProps {
  state: PrototypeState;
  sessionId: string;
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
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
  const usage = state.projection.fixture.usage;

  return (
    <div
      className="sw-work-panel sw-route-enter"
      data-work-presentation={primitives.sheet}
    >
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
          {nodes.map((node) => (
            <article
              className="sw-work-node"
              data-work-node-id={node.id}
              data-work-kind={node.kind}
              data-work-parent-id={node.parentId ?? "root"}
              key={node.id}
            >
              <Disclosure
                summary={<span>{node.title}</span>}
                expanded={state.expandedWorkIds.has(node.id)}
                onToggle={() =>
                  dispatch({ type: "toggleWork", nodeId: node.id })
                }
              >
                <div className="sw-work-node__detail">
                  <StatusLabel state={node.state} />
                  <dl>
                    <div>
                      <dt>Type</dt>
                      <dd>{node.kind}</dd>
                    </div>
                    <div>
                      <dt>Phase</dt>
                      <dd>{node.phase}</dd>
                    </div>
                    <div>
                      <dt>Elapsed</dt>
                      <dd>{node.elapsedLabel}</dd>
                    </div>
                  </dl>
                  <p>{node.output}</p>
                </div>
              </Disclosure>
            </article>
          ))}
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
