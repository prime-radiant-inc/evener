import type { PrototypeAction, PrototypeState } from "../../core/state";
import { StatusLabel } from "../shared/StatusLabel";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";

export interface SettingsViewProps {
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function SettingsView({ state, dispatch }: SettingsViewProps) {
  if (isBlockingRouteState(state)) {
    return (
      <BlockingRouteState
        state={state}
        routeLabel="Settings"
        dispatch={dispatch}
      />
    );
  }
  return (
    <div className="fn-settings fn-route-enter" data-offline-prototype="true">
      <OfflineNotice
        state={state}
        routeLabel="Settings"
        mutationDetail="Local display and voice preferences remain usable; Hub states are saved evidence."
        dispatch={dispatch}
      />

      <section className="fn-settings-section" aria-labelledby="fn-hubs-title">
        <header>
          <div>
            <p className="fn-eyebrow">Separated trust contexts</p>
            <h2 id="fn-hubs-title">Hubs</h2>
          </div>
          <span>{state.projection.fixture.hubs.length}</span>
        </header>
        <div className="fn-inset-list">
          {state.projection.fixture.hubs.map((hub) => (
            <article
              className="fn-hub-row"
              data-hub-id={hub.id}
              data-fictional="true"
              key={hub.id}
            >
              <span className="fn-hub-mark" aria-hidden="true">
                {hub.name.slice(0, 1)}
              </span>
              <span>
                <strong>{hub.name}</strong>
                <small>{hub.context}</small>
              </span>
              <StatusLabel
                state={hub.state === "connected" ? "complete" : "failed"}
              />
            </article>
          ))}
        </div>
      </section>

      <section
        className="fn-settings-section"
        aria-labelledby="fn-display-title"
      >
        <header>
          <div>
            <p className="fn-eyebrow">Native expression</p>
            <h2 id="fn-display-title">Display</h2>
          </div>
        </header>
        <div className="fn-settings-controls">
          <label className="fn-setting-row">
            <span>
              <strong>Appearance</strong>
              <small>Explicit light, dark, or system preference</small>
            </span>
            <select
              aria-label="Appearance"
              value={state.appearance}
              onChange={(event) => {
                const appearance = event.currentTarget.value;
                if (
                  appearance === "system" ||
                  appearance === "light" ||
                  appearance === "dark"
                ) {
                  dispatch({ type: "setAppearance", appearance });
                }
              }}
            >
              <option value="system">System</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          </label>
          <label className="fn-setting-row">
            <span>
              <strong>Text size</strong>
              <small>Preserves one readable column at every scale</small>
            </span>
            <select
              aria-label="Text size"
              value={state.textScale}
              onChange={(event) => {
                const textScale = event.currentTarget.value;
                if (
                  textScale === "standard" ||
                  textScale === "large" ||
                  textScale === "accessibility"
                ) {
                  dispatch({ type: "setTextScale", textScale });
                }
              }}
            >
              <option value="standard">Standard</option>
              <option value="large">Large</option>
              <option value="accessibility">Accessibility</option>
            </select>
          </label>
          <label className="fn-setting-row fn-setting-row--check">
            <span>
              <strong>Reduce motion</strong>
              <small>
                Removes route, disclosure, insert, and resolve motion
              </small>
            </span>
            <input
              type="checkbox"
              aria-label="Reduce motion"
              checked={state.reducedMotion}
              onChange={(event) =>
                dispatch({
                  type: "setReducedMotion",
                  reducedMotion: event.currentTarget.checked,
                })
              }
            />
          </label>
        </div>
      </section>

      <section className="fn-settings-section" aria-labelledby="fn-voice-title">
        <header>
          <div>
            <p className="fn-eyebrow">Voice and privacy</p>
            <h2 id="fn-voice-title">Voice</h2>
          </div>
        </header>
        <div className="fn-settings-controls">
          <label className="fn-setting-row fn-setting-row--check">
            <span>
              <strong>Speak responses</strong>
              <small>Only while the fictional voice surface is active</small>
            </span>
            <input
              type="checkbox"
              aria-label="Speak responses"
              checked={state.voicePreferences.speakResponses}
              onChange={(event) =>
                dispatch({
                  type: "setSpeakResponses",
                  enabled: event.currentTarget.checked,
                })
              }
            />
          </label>
          <label className="fn-setting-row">
            <span>
              <strong>Speech rate</strong>
              <small>Presentation preference only</small>
            </span>
            <select
              aria-label="Speech rate"
              value={state.voicePreferences.rate}
              onChange={(event) => {
                const rate = event.currentTarget.value;
                if (rate === "slow" || rate === "normal" || rate === "fast") {
                  dispatch({ type: "setSpeechRate", rate });
                }
              }}
            >
              <option value="slow">Slow</option>
              <option value="normal">Normal</option>
              <option value="fast">Fast</option>
            </select>
          </label>
        </div>
      </section>

      <section className="fn-settings-section fn-concept-note">
        <p className="fn-eyebrow">Concept 03 · Editorial Studio</p>
        <h2>Field Notes</h2>
        <p>
          Warm ivory, graphite, and restrained rust shape a transcript studio
          built for long-form reading. Editorial rhythm, margin notes, and clear
          live controls keep the record crafted without making active work feel
          archival. Use the header actions to switch concept or open Lab
          Controls.
        </p>
      </section>
    </div>
  );
}
