import { selectGroupedSessions } from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { StatusLabel } from "../shared/StatusLabel";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";

export interface SessionsViewProps {
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function SessionsView({ state, dispatch }: SessionsViewProps) {
  const groups = selectGroupedSessions(state);

  if (isBlockingRouteState(state)) {
    return (
      <BlockingRouteState
        state={state}
        routeLabel="Sessions"
        dispatch={dispatch}
      />
    );
  }

  return (
    <div className="co-sessions co-route-enter">
      <OfflineNotice
        state={state}
        routeLabel="Sessions"
        mutationDetail="Opening saved sessions is available; refresh is explicit."
        dispatch={dispatch}
      />

      <div className="co-filter-row">
        <label>
          <span className="co-visually-hidden">Filter sessions</span>
          <input
            type="search"
            aria-label="Filter sessions"
            placeholder="Filter sessions and projects"
            value={state.sessionQuery}
            onChange={(event) =>
              dispatch({
                type: "setSessionQuery",
                value: event.currentTarget.value,
              })
            }
          />
        </label>
        <button
          type="button"
          disabled={state.refreshState === "refreshing"}
          onClick={() => dispatch({ type: "refreshSessions" })}
        >
          Refresh
        </button>
      </div>

      <div
        className="co-refresh-status"
        data-refresh-state={state.refreshState}
        role="status"
        aria-live="polite"
      >
        {state.refreshState === "refreshing" ? (
          <>
            <span>Refreshing the local fixture…</span>
            <button
              type="button"
              data-action="complete-refresh"
              onClick={() => dispatch({ type: "completeRefresh" })}
            >
              Complete refresh
            </button>
          </>
        ) : state.refreshState === "complete" ? (
          <span>Refresh complete</span>
        ) : (
          <span>Up to date</span>
        )}
      </div>

      {groups.length === 0 ? (
        <section className="co-inline-state" data-session-filter-empty="true">
          <h2>No matching sessions</h2>
          <p>Try a title or project name from the local fixture.</p>
        </section>
      ) : (
        <div className="co-session-groups">
          {groups.map((group) => (
            <section
              className="co-session-group"
              data-session-group-id={group.id}
              aria-labelledby={`co-session-group-${group.id}`}
              key={group.id}
            >
              <header>
                <h2 id={`co-session-group-${group.id}`}>{group.label}</h2>
                <span>{group.sessions.length}</span>
              </header>
              <div className="co-inset-list">
                {group.sessions.map((session) => {
                  const workCount = state.projection.fixture.work.filter(
                    ({ sessionId }) => sessionId === session.id,
                  ).length;
                  const needsAttention =
                    session.state === "needs-answer" ||
                    session.state === "needs-permission";
                  return (
                    <article
                      className="co-session-row"
                      data-session-id={session.id}
                      key={session.id}
                    >
                      <button
                        type="button"
                        onClick={() =>
                          dispatch({
                            type: "openSession",
                            sessionId: session.id,
                          })
                        }
                      >
                        <span className="co-session-row__copy">
                          <strong>{session.title}</strong>
                          <span>{session.project}</span>
                          <small>{session.summary}</small>
                          {workCount > 0 ? (
                            <span
                              className="co-relationship-rail"
                              data-relationship-rail
                            >
                              <span
                                className="co-connection-marker"
                                data-connection-marker
                                aria-hidden="true"
                              />
                              {workCount} connected work{" "}
                              {workCount === 1 ? "item" : "items"}
                            </span>
                          ) : null}
                        </span>
                        <span className="co-session-row__meta">
                          <time>{session.updatedLabel}</time>
                          {needsAttention ? (
                            <span
                              className="co-attention-signal"
                              data-attention-signal="true"
                              data-attention-state={session.state}
                            >
                              <span
                                className="co-attention-marker"
                                aria-hidden="true"
                              >
                                !
                              </span>
                              Needs attention
                            </span>
                          ) : null}
                          <span data-state-label>
                            <StatusLabel state={session.state} />
                          </span>
                        </span>
                      </button>
                    </article>
                  );
                })}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}
