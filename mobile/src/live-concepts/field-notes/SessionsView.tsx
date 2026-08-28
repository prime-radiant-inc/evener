import { rosterAxLabel, rosterRowAxLabel } from "../accessibility-semantics";
import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveRosterRow } from "../model";
import { StatusLabel } from "../shared/StatusLabel";

// Live adaptation of the Field Notes sessions surface. Renders the live
// roster view (groups and rows) with current-record state markers, the live
// updatedLabel, and the roster query/refresh intents. No prototype navigation,
// no fixture, no Lab controls.

export interface SessionsViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

const ATTENTION_TONES = new Set(["attention"]);
const CURRENT_TONES = new Set(["attention", "running", "idle"]);

function currentState(row: LiveRosterRow): string | undefined {
  if (ATTENTION_TONES.has(row.tone)) return "attention";
  if (CURRENT_TONES.has(row.tone)) return row.tone;
  return undefined;
}

export function SessionsView({ state, dispatch }: SessionsViewProps) {
  const { roster, connection } = state;
  const offline =
    connection.status === "offline" || connection.status === "error";

  return (
    <div className="fn-sessions fn-route-enter">
      {offline ? (
        <div
          className="fn-banner"
          data-connection-status={connection.status}
          role="status"
          aria-live="polite"
        >
          <span>Offline. Saved records are readable; refresh is explicit.</span>
        </div>
      ) : null}

      <div className="fn-filter-row">
        <label>
          <span className="fn-visually-hidden">Filter sessions</span>
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

      <div
        className="fn-refresh-status"
        data-roster-status={roster.status}
        data-refresh-state={roster.status}
        role="status"
        aria-live="polite"
        aria-label={rosterAxLabel(state.concept, roster)}
      >
        {roster.status === "loading" ? (
          <span>Refreshing the roster…</span>
        ) : roster.status === "idle" ? (
          <span>Roster is idle</span>
        ) : roster.status === "offline" ? (
          <span>Offline — refresh unavailable</span>
        ) : roster.status === "error" ? (
          <span>Refresh failed</span>
        ) : (
          <span>Up to date</span>
        )}
      </div>

      {roster.status === "error" && roster.error ? (
        <p className="fn-roster-error" data-roster-error>
          {roster.error}
        </p>
      ) : null}

      {roster.groups.length === 0 ? (
        <section className="fn-inline-state" data-session-filter-empty="true">
          <h2>No matching records</h2>
          <p>Try a title or project name from the live roster.</p>
        </section>
      ) : (
        <div className="fn-session-groups">
          {roster.groups.map((group) => (
            <section
              className="fn-session-group"
              data-session-group-id={group.id}
              aria-labelledby={`fn-session-group-${group.id}`}
              key={group.id}
            >
              <header>
                <h2 id={`fn-session-group-${group.id}`}>{group.label}</h2>
                <span>{group.rows.length}</span>
              </header>
              <div className="fn-inset-list">
                {group.rows.map((row) => {
                  const state = currentState(row);
                  const needsAttention = row.tone === "attention";
                  return (
                    <article
                      className="fn-session-row"
                      data-session-id={row.key}
                      data-current-state={state ?? undefined}
                      key={row.key}
                    >
                      <button
                        type="button"
                        aria-label={rosterRowAxLabel(row)}
                        onClick={() =>
                          dispatch({ type: "openConversation", key: row.key })
                        }
                      >
                        <span className="fn-session-row__copy">
                          <strong>{row.title}</strong>
                          <span>{row.project}</span>
                          <small>{row.summary}</small>
                          {row.connectedWorkCount > 0 ? (
                            <span
                              className="fn-relationship-rail"
                              data-relationship-rail
                            >
                              <span
                                className="fn-connection-marker"
                                data-connection-marker
                                aria-hidden="true"
                              />
                              {row.connectedWorkCount} connected work{" "}
                              {row.connectedWorkCount === 1 ? "item" : "items"}
                            </span>
                          ) : null}
                        </span>
                        <span className="fn-session-row__meta">
                          <time>{row.updatedLabel}</time>
                          {needsAttention ? (
                            <span
                              className="fn-attention-signal"
                              data-attention-signal="true"
                              data-attention-state="attention"
                              aria-live="polite"
                            >
                              <span
                                className="fn-attention-marker"
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
      )}
    </div>
  );
}
