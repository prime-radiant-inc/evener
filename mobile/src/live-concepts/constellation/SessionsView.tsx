import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveRosterView } from "../model";
import { StatusLabel } from "../shared/StatusLabel";

export interface SessionsViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

function needsAttentionTone(
  tone: LiveRosterView["groups"][number]["rows"][number]["tone"],
): boolean {
  return tone === "attention";
}

export function SessionsView({ state, dispatch }: SessionsViewProps) {
  const roster = state.roster;
  const loading = roster.status === "loading";

  return (
    <div className="co-sessions co-route-enter">
      <div className="co-filter-row">
        <label>
          <span className="co-visually-hidden">Filter sessions</span>
          <input
            type="search"
            aria-label="Filter sessions"
            placeholder="Filter sessions and projects"
            value={roster.query}
            disabled={loading}
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
          disabled={loading}
          onClick={() => dispatch({ type: "refreshRoster" })}
        >
          Refresh
        </button>
      </div>

      <div
        className="co-refresh-status"
        data-refresh-state={roster.status}
        role="status"
        aria-live="polite"
      >
        {loading ? (
          <span>Refreshing the live roster…</span>
        ) : roster.status === "ready" ? (
          <span>Up to date</span>
        ) : null}
      </div>

      {roster.status === "error" && roster.error ? (
        <section className="co-inline-state" data-roster-error="true">
          <h2>Roster unavailable</h2>
          <p>{roster.error}</p>
        </section>
      ) : null}

      {roster.status !== "error" && roster.groups.length === 0 ? (
        <section className="co-inline-state" data-session-filter-empty="true">
          <h2>No matching sessions</h2>
          <p>Try a title or project name from the live roster.</p>
        </section>
      ) : null}

      {roster.groups.length > 0 ? (
        <div className="co-session-groups">
          {roster.groups.map((group) => (
            <section
              className="co-session-group"
              data-session-group-id={group.id}
              data-testid={`session-group-${group.id}`}
              aria-labelledby={`co-session-group-${group.id}`}
              key={group.id}
            >
              <header>
                <h2 id={`co-session-group-${group.id}`}>{group.label}</h2>
                <span>{group.rows.length}</span>
              </header>
              <div className="co-inset-list">
                {group.rows.map((row) => {
                  const attention = needsAttentionTone(row.tone);
                  return (
                    <article
                      className="co-session-row"
                      data-session-id={row.key}
                      data-testid={`session-row-${row.key}`}
                      key={row.key}
                    >
                      <button
                        type="button"
                        onClick={() =>
                          dispatch({ type: "openConversation", key: row.key })
                        }
                      >
                        <span className="co-session-row__copy">
                          <strong>{row.title}</strong>
                          <span>{row.project}</span>
                          <small>{row.summary}</small>
                          {row.connectedWorkCount > 0 ? (
                            <span
                              className="co-relationship-rail"
                              data-relationship-rail
                              data-testid="relationship-rail"
                            >
                              <span
                                className="co-connection-marker"
                                data-connection-marker
                                data-testid="connection-marker"
                                aria-hidden="true"
                              />
                              {row.connectedWorkCount} connected work{" "}
                              {row.connectedWorkCount === 1 ? "item" : "items"}
                            </span>
                          ) : null}
                        </span>
                        <span className="co-session-row__meta">
                          <time>{row.updatedLabel}</time>
                          {attention ? (
                            <span
                              className="co-attention-signal"
                              data-attention-signal="true"
                              data-testid="attention-signal"
                              data-attention-state={row.tone}
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
                            <StatusLabel state={row.tone} />
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
      ) : null}

      {roster.hasMore ? (
        <p className="co-roster-more" data-roster-has-more="true">
          More sessions available — refine the filter to narrow further.
        </p>
      ) : null}
    </div>
  );
}
