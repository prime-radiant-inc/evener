import { selectSearchProjection } from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Icon } from "../shared/Icon";
import { ScreenState } from "../shared/ScreenState";

export interface SearchViewProps {
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function SearchView({ state, dispatch }: SearchViewProps) {
  const projection = selectSearchProjection(state);
  const screenState = state.projection.screenState;

  if (screenState === "loading") {
    return (
      <ScreenState state={{ kind: "loading", title: "Preparing search" }} />
    );
  }
  if (screenState === "error") {
    return (
      <ScreenState
        state={{
          kind: "error",
          title: "Search is unavailable",
          detail: "Reset the scenario or retry from Sessions.",
        }}
      />
    );
  }

  return (
    <div className="sw-search sw-route-enter">
      {screenState === "offline" ? (
        <p className="sw-banner" data-offline-prototype="true" role="status">
          Search is limited to the locally stored fixture while offline.
        </p>
      ) : null}
      <label className="sw-search-field">
        <Icon name="search" decorative />
        <span className="sw-visually-hidden">Search</span>
        <input
          type="search"
          aria-label="Search"
          autoComplete="off"
          placeholder="Search sessions, projects, messages, and work"
          value={state.globalQuery}
          onChange={(event) =>
            dispatch({
              type: "setGlobalQuery",
              value: event.currentTarget.value,
            })
          }
        />
      </label>

      {projection.kind === "prompt" ? (
        <section className="sw-search-prompt" data-search-state="prompt">
          <p className="sw-eyebrow">Local search</p>
          <h2>Find the exact thread</h2>
          <p>
            Search titles and context across sessions, transcript passages,
            tools, projects, and tasks.
          </p>
          <ul aria-label="Search includes">
            <li>Session and project names</li>
            <li>Transcript and tool context</li>
            <li>Task evidence</li>
          </ul>
        </section>
      ) : null}

      {projection.kind === "no-results" ? (
        <section data-search-state="no-results">
          <ScreenState
            state={{
              kind: "empty",
              title: `No results for “${projection.query}”`,
              detail:
                "Try a broader phrase. Search stays on this device fixture.",
            }}
          />
        </section>
      ) : null}

      {projection.kind === "results" ? (
        <section className="sw-search-results" data-search-state="results">
          <header>
            <p className="sw-eyebrow">Matches</p>
            <h2>{projection.results.length} local results</h2>
          </header>
          <div className="sw-inset-list">
            {projection.results.map((result) => (
              <article className="sw-result-row" key={result.id}>
                <a
                  data-search-result-id={result.id}
                  data-search-kind={result.kind}
                  data-search-context={result.context}
                  href={`#session-${encodeURIComponent(result.sessionId)}`}
                  onClick={(event) => {
                    event.preventDefault();
                    dispatch({ type: "openSearchResult", resultId: result.id });
                  }}
                >
                  <span className="sw-result-row__kind">{result.kind}</span>
                  <strong>{result.title}</strong>
                  <span>{result.context}</span>
                  <Icon name="chevron" decorative />
                </a>
              </article>
            ))}
          </div>
        </section>
      ) : null}
    </div>
  );
}
