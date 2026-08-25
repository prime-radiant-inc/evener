import { selectGroupedSessions } from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { ScreenState } from "../shared/ScreenState";
import { StatusLabel } from "../shared/StatusLabel";

export interface SessionsViewProps {
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function SessionsView({ state, dispatch }: SessionsViewProps) {
  const { screenState } = state.projection;
  const groups = selectGroupedSessions(state);
  const retry = () => dispatch({ type: "refreshSessions" });

  if (screenState === "loading") {
    return (
      <ScreenState
        state={{
          kind: "loading",
          title: "Loading sessions",
          detail: "Reading the deterministic local fixture.",
        }}
      />
    );
  }
  if (screenState === "empty") {
    return (
      <ScreenState
        state={{
          kind: "empty",
          title: "No sessions yet",
          detail: "Start a session when you have work to continue.",
        }}
      />
    );
  }
  if (screenState === "error") {
    return (
      <ScreenState
        state={{
          kind: "error",
          title: "Sessions could not be shown",
          detail:
            "The prototype fixture is recoverable. Try the explicit refresh.",
        }}
        retryAction={retry}
      />
    );
  }

  return (
    <div className="sw-sessions sw-route-enter">
      {screenState === "offline" ? (
        <section
          className="sw-banner"
          data-offline-prototype="true"
          role="status"
        >
          <strong>Offline prototype</strong>
          <span>
            Showing the last deterministic fixture. New work is unavailable.
          </span>
          <button type="button" onClick={retry}>
            Retry
          </button>
        </section>
      ) : null}

      <div className="sw-filter-row">
        <label>
          <span className="sw-visually-hidden">Filter sessions</span>
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
        className="sw-refresh-status"
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
        <section className="sw-inline-state" data-session-filter-empty="true">
          <h2>No matching sessions</h2>
          <p>Try a title or project name from the local fixture.</p>
        </section>
      ) : (
        <div className="sw-session-groups">
          {groups.map((group) => (
            <section
              className="sw-session-group"
              data-session-group-id={group.id}
              aria-labelledby={`sw-session-group-${group.id}`}
              key={group.id}
            >
              <header>
                <h2 id={`sw-session-group-${group.id}`}>{group.label}</h2>
                <span>{group.sessions.length}</span>
              </header>
              <div className="sw-inset-list">
                {group.sessions.map((session) => (
                  <article
                    className="sw-session-row"
                    data-session-id={session.id}
                    key={session.id}
                  >
                    <button
                      type="button"
                      onClick={() =>
                        dispatch({ type: "openSession", sessionId: session.id })
                      }
                    >
                      <span className="sw-session-row__copy">
                        <strong>{session.title}</strong>
                        <span>{session.project}</span>
                        <small>{session.summary}</small>
                      </span>
                      <span className="sw-session-row__meta">
                        <time>{session.updatedLabel}</time>
                        <StatusLabel state={session.state} />
                      </span>
                    </button>
                  </article>
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}
