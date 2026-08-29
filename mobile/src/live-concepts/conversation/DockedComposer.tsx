import { type ReactElement, useLayoutEffect, useRef } from "react";
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

function cssPixels(value: string, fallback: number): number {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function measuredLineHeight(style: CSSStyleDeclaration): number {
  const fontSize = cssPixels(style.fontSize, 16);
  const parsed = Number.parseFloat(style.lineHeight);
  if (!Number.isFinite(parsed)) return fontSize * 1.2;
  return style.lineHeight.trim().endsWith("px") ? parsed : parsed * fontSize;
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
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const pending = composer.pending?.status === "pending";
  const hasTextCapability =
    composer.canSend || composer.canSteer || composer.canQueue;
  const draft = composer.draft;
  const canSubmit =
    canUseMode(mode, composer) && draft.trim().length > 0 && !pending;
  useLayoutEffect(() => {
    const textarea = textareaRef.current;
    if (textarea === null) return;
    if (textarea.value !== draft) return;
    textarea.style.blockSize = "auto";
    const style = window.getComputedStyle(textarea);
    const lineHeight = measuredLineHeight(style);
    const padding =
      cssPixels(style.paddingBlockStart, 0) +
      cssPixels(style.paddingBlockEnd, 0);
    const minimum = lineHeight + padding;
    const maximum = 6 * lineHeight + padding;
    const measured = Math.max(textarea.scrollHeight, minimum);
    textarea.style.blockSize = `${Math.min(measured, maximum)}px`;
    textarea.style.overflowY = measured > maximum ? "auto" : "hidden";
  }, [draft]);

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
          ref={textareaRef}
          aria-label="Message"
          data-live-conversation-message="true"
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
