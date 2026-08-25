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
    <div className="co-settings co-route-enter" data-offline-prototype="true">
      <OfflineNotice
        state={state}
        routeLabel="Settings"
        mutationDetail="Local display and voice preferences remain usable; Hub states are saved evidence."
        dispatch={dispatch}
      />

      <section className="co-settings-section" aria-labelledby="co-hubs-title">
        <header>
          <div>
            <p className="co-eyebrow">Separated trust contexts</p>
            <h2 id="co-hubs-title">Hubs</h2>
          </div>
          <span>{state.projection.fixture.hubs.length}</span>
        </header>
        <div className="co-inset-list">
          {state.projection.fixture.hubs.map((hub) => (
            <article
              className="co-hub-row"
              data-hub-id={hub.id}
              data-fictional="true"
              key={hub.id}
            >
              <span className="co-hub-mark" aria-hidden="true">
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
        className="co-settings-section"
        aria-labelledby="co-display-title"
      >
        <header>
          <div>
            <p className="co-eyebrow">Native expression</p>
            <h2 id="co-display-title">Display</h2>
          </div>
        </header>
        <div className="co-settings-controls">
          <label className="co-setting-row">
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
          <label className="co-setting-row">
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
          <label className="co-setting-row co-setting-row--check">
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

      <section className="co-settings-section" aria-labelledby="co-voice-title">
        <header>
          <div>
            <p className="co-eyebrow">Voice and privacy</p>
            <h2 id="co-voice-title">Voice</h2>
          </div>
        </header>
        <div className="co-settings-controls">
          <label className="co-setting-row co-setting-row--check">
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
          <label className="co-setting-row">
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

      <section className="co-settings-section co-concept-note">
        <p className="co-eyebrow">Concept 02 · Living System</p>
        <h2>Constellation</h2>
        <p>
          Deep ink, mint signal, violet depth, and disciplined relationship
          rails reveal the shape of active work without turning evidence into a
          graph. Use the header actions to switch concept or open Lab Controls.
        </p>
      </section>
    </div>
  );
}
