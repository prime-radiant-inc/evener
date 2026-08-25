import { selectSearchProjection } from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Icon } from "../shared/Icon";
import { ScreenState } from "../shared/ScreenState";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";

export interface SearchViewProps {
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function SearchView({ state, dispatch }: SearchViewProps) {
  const projection = selectSearchProjection(state);

  if (isBlockingRouteState(state)) {
    return (
      <BlockingRouteState
        state={state}
        routeLabel="Search"
        dispatch={dispatch}
      />
    );
  }

  return (
    <div className="fn-search fn-route-enter">
      <OfflineNotice
        state={state}
        routeLabel="Search"
        mutationDetail="Search and opening saved results stay local."
        dispatch={dispatch}
      />
      <label className="fn-search-field">
        <Icon name="search" decorative />
        <span className="fn-visually-hidden">Search</span>
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
        <section className="fn-search-prompt" data-search-state="prompt">
          <p className="fn-eyebrow">Local search</p>
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
        <section className="fn-search-results" data-search-state="results">
          <header>
            <p className="fn-eyebrow">Matches</p>
            <h2>{projection.results.length} local results</h2>
          </header>
          <div className="fn-inset-list">
            {projection.results.map((result) => (
              <article className="fn-result-row" key={result.id}>
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
                  <span className="fn-result-row__kind">{result.kind}</span>
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
