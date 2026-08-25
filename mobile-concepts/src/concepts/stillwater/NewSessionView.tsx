import { selectNewSessionValidity } from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";

export interface NewSessionViewProps {
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function NewSessionView({ state, dispatch }: NewSessionViewProps) {
  const fixture = state.projection.fixture;
  const valid = selectNewSessionValidity(state);
  const starting = state.newSession.outcome === "starting";
  const firstHub = fixture.hubs[0];

  return (
    <div className="sw-new-session sw-route-enter">
      <section className="sw-launch-intro">
        <p className="sw-eyebrow">Deliberate start</p>
        <h2>Start with a known project</h2>
        <p>
          This spike performs no remote work. Completion is an explicit local
          prototype action.
        </p>
      </section>

      <section className="sw-recent-projects" aria-labelledby="sw-recent-title">
        <header>
          <h2 id="sw-recent-title">Recent projects</h2>
          <span>Fixture paths</span>
        </header>
        <div className="sw-project-grid">
          {fixture.recentProjects.map((project) => (
            <article data-project-id={project.id} key={project.id}>
              <button
                type="button"
                aria-pressed={state.newSession.project === project.path}
                onClick={() =>
                  dispatch({
                    type: "selectRecentProject",
                    projectId: project.id,
                  })
                }
              >
                <strong>{project.label}</strong>
                <span>{project.path}</span>
              </button>
            </article>
          ))}
        </div>
      </section>

      <form
        className="sw-launch-form"
        onSubmit={(event) => {
          event.preventDefault();
          dispatch({ type: "submitNewSession" });
        }}
      >
        <div className="sw-launch-context">
          <span className="sw-eyebrow">Hub · fictional</span>
          <strong>{firstHub?.name ?? "No fixture Hub"}</strong>
          <span>{firstHub?.context ?? "No local context"}</span>
        </div>

        <label className="sw-field">
          <span>Project path</span>
          <input
            type="text"
            aria-label="Project path"
            placeholder="Enter an exact fixture path"
            value={state.newSession.project}
            disabled={starting}
            onChange={(event) =>
              dispatch({
                type: "setNewSessionProject",
                value: event.currentTarget.value,
              })
            }
          />
          <small>
            Choose a recent project or type a permitted fixture path.
          </small>
        </label>

        <label className="sw-field">
          <span>Prompt</span>
          <textarea
            aria-label="Prompt"
            placeholder="Describe the first useful outcome"
            value={state.newSession.prompt}
            disabled={starting}
            onChange={(event) =>
              dispatch({
                type: "setNewSessionPrompt",
                value: event.currentTarget.value,
              })
            }
          />
        </label>

        <div className="sw-field-pair">
          <label className="sw-field">
            <span>Model</span>
            <select
              aria-label="Model"
              value={state.newSession.modelId}
              disabled={starting}
              onChange={(event) =>
                dispatch({
                  type: "setNewSessionModel",
                  modelId: event.currentTarget.value,
                })
              }
            >
              {fixture.models.map((model) => (
                <option value={model.id} key={model.id}>
                  {model.label} · {model.provider}
                </option>
              ))}
            </select>
          </label>
          <label className="sw-field">
            <span>Effort</span>
            <select
              aria-label="Effort"
              value={state.newSession.effort}
              disabled={starting}
              onChange={(event) => {
                const effort = event.currentTarget.value;
                if (
                  effort === "low" ||
                  effort === "medium" ||
                  effort === "high"
                ) {
                  dispatch({ type: "setNewSessionEffort", effort });
                }
              }}
            >
              {fixture.efforts.map((effort) => (
                <option value={effort} key={effort}>
                  {effort}
                </option>
              ))}
            </select>
          </label>
        </div>

        <section className="sw-launch-summary" aria-label="Before starting">
          <strong>Before starting</strong>
          <p>
            The fictional agent receives project access. No service, background
            audio, or network request is created by this prototype.
          </p>
        </section>

        <button
          className="sw-primary-action"
          type="submit"
          disabled={!valid || starting}
        >
          {starting ? "Starting…" : "Start session"}
        </button>
      </form>

      {starting ? (
        <section
          className="sw-outcome-panel sw-resolve-enter"
          data-new-session-state="starting"
          role="status"
        >
          <h2>Starting local fixture session</h2>
          <p>Choose the deterministic completion for this prototype run.</p>
          <div>
            <button
              type="button"
              data-action="complete-new-session-success"
              onClick={() =>
                dispatch({ type: "completeNewSession", result: "success" })
              }
            >
              Complete successfully
            </button>
            <button
              type="button"
              data-action="complete-new-session-failure"
              onClick={() =>
                dispatch({ type: "completeNewSession", result: "failure" })
              }
            >
              Complete with failure
            </button>
          </div>
        </section>
      ) : null}

      {state.newSession.outcome === "failure" ? (
        <section
          className="sw-outcome-panel sw-outcome-panel--error"
          data-new-session-state="failure"
          role="alert"
        >
          <h2>Session did not start</h2>
          <p>
            Synthetic error: {state.newSession.errorCode}. Your project, prompt,
            model, and effort remain available to edit.
          </p>
        </section>
      ) : null}
    </div>
  );
}
