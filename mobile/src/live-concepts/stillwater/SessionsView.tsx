import type { LiveConceptIntent, LiveConceptState } from "../contract";
import { StatusLabel } from "../shared/StatusLabel";

export interface SessionsViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

export function SessionsView({ state, dispatch }: SessionsViewProps) {
  const { roster } = state;

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
          disabled={roster.status === "loading"}
          onClick={() => dispatch({ type: "refreshRoster" })}
        >
          Refresh
        </button>
      </div>

      {roster.status === "loading" ? (
        <div className="sw-refresh-status" role="status" aria-live="polite">
          <span>Loading sessions…</span>
        </div>
      ) : null}

      {roster.status === "error" && roster.error ? (
        <div className="sw-banner" role="alert">
          <span>{roster.error}</span>
        </div>
      ) : null}

      {roster.groups.length === 0 ? (
        <section className="sw-inline-state">
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
                        <time>{row.updatedLabel}</time>
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
