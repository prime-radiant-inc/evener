import {
  selectCanMutate,
  selectCurrentVoiceLevel,
  selectCurrentVoiceStep,
} from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Icon } from "../shared/Icon";
import { ScreenState } from "../shared/ScreenState";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";

export interface VoiceViewProps {
  state: PrototypeState;
  sessionId: string;
  dispatch(action: PrototypeAction): void;
}

export function VoiceView({ state, sessionId, dispatch }: VoiceViewProps) {
  const session = state.projection.fixture.sessions.find(
    ({ id }) => id === sessionId,
  );
  const step = selectCurrentVoiceStep(state);
  if (isBlockingRouteState(state)) {
    return (
      <BlockingRouteState
        state={state}
        routeLabel="Voice"
        dispatch={dispatch}
      />
    );
  }
  if (!session || state.selectedSessionId !== sessionId || !step) {
    return (
      <ScreenState
        state={{
          kind: "error",
          title: "Voice state unavailable",
          detail: "Close voice and choose a session with a complete fixture.",
        }}
        retryAction={() => dispatch({ type: "goBack" })}
      />
    );
  }
  const level = selectCurrentVoiceLevel(state);
  const displayedLevel = state.voice.muted || state.voice.stopped ? 0 : level;
  const canMutate = selectCanMutate(state);

  return (
    <div className="sw-voice sw-route-enter">
      <OfflineNotice
        state={state}
        routeLabel="Voice"
        mutationDetail="Voice lifecycle mutations are unavailable; close to return to saved transcript evidence."
        dispatch={dispatch}
      />
      <section
        className="sw-voice-stage"
        data-voice-state={step.state}
        data-muted={state.voice.muted}
        data-stopped={state.voice.stopped}
        aria-labelledby="sw-voice-title"
      >
        <p className="sw-eyebrow">{session.title}</p>
        <h2 id="sw-voice-title">
          {state.voice.stopped
            ? "Voice stopped"
            : state.voice.muted
              ? "Microphone muted"
              : `${step.state.charAt(0).toUpperCase()}${step.state.slice(1)} voice`}
        </h2>
        <div
          className="sw-voice-meter"
          role="progressbar"
          aria-label="Voice level"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={displayedLevel}
        >
          <span style={{ transform: `scale(${displayedLevel / 100})` }} />
          <Icon name="voice" decorative />
        </div>
        <p>{step.caption}</p>
        <p className="sw-voice-privacy">
          Completed fictional speech becomes a session instruction. Partial
          speech is never sent, and this spike invokes no microphone service.
        </p>
      </section>

      <section
        className="sw-voice-lifecycle"
        aria-labelledby="sw-lifecycle-title"
      >
        <header>
          <h2 id="sw-lifecycle-title">Voice lifecycle</h2>
          <span>
            Step {state.voice.stepIndex + 1} of{" "}
            {state.projection.fixture.voiceSteps.length}
          </span>
        </header>
        <div className="sw-voice-states">
          {state.projection.fixture.voiceSteps.map((voiceStep) => (
            <button
              type="button"
              aria-label={`Set voice state: ${voiceStep.state}`}
              aria-pressed={voiceStep.id === step.id}
              disabled={!canMutate}
              key={voiceStep.id}
              onClick={() =>
                dispatch({ type: "setVoiceState", state: voiceStep.state })
              }
            >
              <span>{voiceStep.state}</span>
              <small>{voiceStep.level}</small>
            </button>
          ))}
        </div>
      </section>

      <fieldset className="sw-voice-controls">
        <legend className="sw-visually-hidden">Voice controls</legend>
        <button
          type="button"
          disabled={state.voice.stopped || !canMutate}
          onClick={() => dispatch({ type: "advanceVoice" })}
        >
          Advance voice state
        </button>
        <button
          type="button"
          disabled={!canMutate}
          onClick={() => dispatch({ type: "toggleVoiceMute" })}
        >
          {state.voice.muted ? "Unmute" : "Mute"}
        </button>
        <button
          type="button"
          disabled={state.voice.stopped || !canMutate}
          onClick={() => dispatch({ type: "stopVoice" })}
        >
          <Icon name="stop" decorative />
          Stop
        </button>
        <button
          className="sw-danger-action"
          type="button"
          disabled={!canMutate}
          onClick={() => dispatch({ type: "endVoice" })}
        >
          End
        </button>
      </fieldset>
    </div>
  );
}
