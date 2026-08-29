import type { ReactElement } from "react";
import type { ComposerAppearance } from "./contract";
import type {
  ComposerMode,
  ConversationFrameAction,
  LiveComposerView,
} from "./primitives";

const MODES: readonly ComposerMode[] = ["send", "steer", "queue"];

function titleCase(value: string): string {
  return `${value.charAt(0).toUpperCase()}${value.slice(1)}`;
}

function canUseMode(mode: ComposerMode, composer: LiveComposerView): boolean {
  if (mode === "send") return composer.canSend;
  if (mode === "steer") return composer.canSteer;
  return composer.canQueue;
}

export interface DockedComposerProps {
  readonly composer: LiveComposerView;
  readonly mode: ComposerMode;
  readonly appearance: ComposerAppearance;
  readonly dispatch: (action: ConversationFrameAction) => void;
}

export function DockedComposer({
  composer,
  mode,
  appearance,
  dispatch,
}: DockedComposerProps): ReactElement {
  const pending = composer.pending !== null;
  const hasTextCapability =
    composer.canSend || composer.canSteer || composer.canQueue;
  const canSubmit =
    canUseMode(mode, composer) && composer.draft.trim().length > 0 && !pending;

  return (
    <div
      className="live-conversation-composer"
      data-composer-density={appearance.density}
      data-composer-accent={appearance.accent}
    >
      <fieldset className="live-conversation-composer__modes">
        <legend className="live-conversation-visually-hidden">
          Composer mode
        </legend>
        {MODES.map((candidate) => {
          const enabled = canUseMode(candidate, composer);
          const explanationId = `composer-${candidate}-explanation`;
          return (
            <span className="live-conversation-composer__mode" key={candidate}>
              <button
                type="button"
                aria-label={`Use ${candidate} mode`}
                aria-pressed={candidate === mode}
                aria-describedby={enabled ? undefined : explanationId}
                disabled={!enabled || pending}
                onClick={() =>
                  dispatch({ type: "setComposerMode", mode: candidate })
                }
              >
                {titleCase(candidate)}
              </button>
              {!enabled ? (
                <span id={explanationId}>
                  {titleCase(candidate)} is unavailable for this conversation.
                </span>
              ) : null}
            </span>
          );
        })}
      </fieldset>
      <label className="live-conversation-composer__field">
        <span className="live-conversation-visually-hidden">Message</span>
        <textarea
          aria-label="Message"
          placeholder="Message or steer…"
          value={composer.draft}
          disabled={!hasTextCapability || pending}
          onChange={(event) =>
            dispatch({ type: "setDraft", value: event.currentTarget.value })
          }
        />
      </label>
      <button
        className="live-conversation-composer__submit"
        type="button"
        aria-label="Submit message"
        disabled={!canSubmit}
        onClick={() => dispatch({ type: "submit", mode })}
      >
        Submit
      </button>
      {composer.canInterrupt ? (
        <button
          className="live-conversation-composer__interrupt"
          type="button"
          aria-label="Interrupt"
          onClick={() => dispatch({ type: "interrupt" })}
        >
          Interrupt
        </button>
      ) : null}
    </div>
  );
}
