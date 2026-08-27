import type { LiveConceptIntent, LiveConceptState } from "../contract";
import { StatusLabel } from "../shared/StatusLabel";

export interface SessionsViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

export function SessionsView({ state, dispatch }: SessionsViewProps) {
  const { roster, connection } = state;
  const isLoading = roster.status === "loading";
  const isOffline =
    roster.status === "offline" || connection.status === "offline";

  return (
    <div
      className="sw-sessions"
      data-roster-status={roster.status}
      data-roster-has-more={roster.hasMore ? "true" : "false"}
    >
      <div className="sw-filter-row">
        <label>
          <span className="sw-visually-hidden">Filter sessions</span>
          <input
            type="search"
            aria-label="Filter sessions"
            placeholder="Filter sessions and projects"
            value={roster.query}
            onChange={(event) =>
              dispatch({
                type: "setRosterQuery",
                value: event.currentTarget.value,
              })
            }
          />
        </label>
        <button
          type="button"
          disabled={isLoading}
          onClick={() => dispatch({ type: "refreshRoster" })}
        >
          Refresh
        </button>
      </div>

      {isLoading ? (
        <div
          className="sw-refresh-status"
          data-roster-loading="true"
          role="status"
          aria-live="polite"
        >
          <span>Loading sessions…</span>
        </div>
      ) : null}

      {isOffline ? (
        <div
          className="sw-banner"
          data-roster-offline="true"
          role="status"
          aria-live="polite"
        >
          <span>Offline — showing saved sessions. Refresh is unavailable.</span>
        </div>
      ) : null}

      {roster.status === "error" && roster.error ? (
        <div className="sw-banner" role="alert" data-roster-error-text>
          <span>{roster.error}</span>
        </div>
      ) : null}

      {roster.groups.length === 0 ? (
        <section className="sw-inline-state" data-roster-empty="true">
          <h2>No matching sessions</h2>
          <p>Try a title or project name.</p>
        </section>
      ) : (
        <div className="sw-session-groups">
          {roster.groups.map((group) => (
            <section
              className="sw-session-group"
              data-session-group-id={group.id}
              aria-labelledby={`sw-session-group-${group.id}`}
              key={group.id}
            >
              <header>
                <h2 id={`sw-session-group-${group.id}`}>{group.label}</h2>
                <span>{group.rows.length}</span>
              </header>
              <div className="sw-inset-list">
                {group.rows.map((row) => (
                  <article
                    className="sw-session-row"
                    data-session-id={row.key}
                    key={row.key}
                  >
                    <button
                      type="button"
                      onClick={() =>
                        dispatch({ type: "openConversation", key: row.key })
                      }
                    >
                      <span className="sw-session-row__copy">
                        <strong>{row.title}</strong>
                        <span>{row.project}</span>
                        <small>{row.summary}</small>
                      </span>
                      <span className="sw-session-row__meta">
                        {row.updatedLabel ? (
                          <time>{row.updatedLabel}</time>
                        ) : null}
                        <StatusLabel state={row.tone} />
                        {row.connectedWorkCount > 0 ? (
                          <span>{row.connectedWorkCount} work</span>
                        ) : null}
                      </span>
                    </button>
                  </article>
                ))}
              </div>
            </section>
          ))}
          {roster.hasMore ? (
            <p className="sw-roster-more">More sessions available</p>
          ) : null}
        </div>
      )}
    </div>
  );
}
