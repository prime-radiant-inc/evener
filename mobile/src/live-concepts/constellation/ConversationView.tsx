import {
  mutationAxLabel,
  transcriptItemAxLabel,
} from "../accessibility-semantics";
import type {
  LiveConceptIntent,
  LiveConceptState,
  QuestionDraft,
} from "../contract";
import type { LiveTranscriptItem } from "../model";
import { Disclosure } from "../shared/Disclosure";
import { Icon } from "../shared/Icon";
import { StatusLabel } from "../shared/StatusLabel";

export interface ConversationViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

const COMPOSER_MODES = ["send", "steer", "queue"] as const;
type ComposerMode = (typeof COMPOSER_MODES)[number];

function capabilityForMode(
  composer: LiveConceptState["composer"],
  mode: ComposerMode,
): boolean {
  if (mode === "send") return composer.canSend;
  if (mode === "steer") return composer.canSteer;
  return composer.canQueue;
}

/* --------------------------- question card --------------------------- */

function isQuestionValid(draft: QuestionDraft): boolean {
  return draft.selectedOptionKeys.length > 0;
}

function QuestionCard({
  questionKey,
  state,
  dispatch,
}: {
  questionKey: string;
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}) {
  const conversation = state.conversation;
  const composer = state.composer;
  const draft = state.ui.questionDrafts[questionKey] ?? {
    selectedOptionKeys: [],
    note: "",
    resolution: null,
  };
  const pending = composer.pending !== null;

  if (!conversation) {
    return (
      <section
        className="co-question-card co-question-card--missing"
        data-question-missing="true"
      >
        <p>Question unavailable</p>
        <p>This transcript item has no linked question view.</p>
      </section>
    );
  }

  const question = conversation.questions.find((q) => q.key === questionKey);

  if (!question) {
    return (
      <section
        className="co-question-card co-question-card--missing"
        data-question-missing="true"
        role="alert"
      >
        <p className="co-question-card__error">Question unavailable</p>
        <p>This transcript item has no linked question view.</p>
      </section>
    );
  }

  const valid = isQuestionValid(draft);

  return (
    <form
      className="co-question-card"
      data-question-key={question.key}
      onSubmit={(event) => {
        event.preventDefault();
        dispatch({ type: "submitQuestion", key: question.key });
      }}
    >
      <fieldset>
        <legend>
          <span className="co-eyebrow">
            {question.multiple ? "Choose one or more" : "Choose one"}
          </span>
          <span>{question.prompt}</span>
        </legend>
        <div className="co-question-options">
          {question.options.map((option) => {
            const detailId = `co-question-${question.key}-option-${option.key}`;
            const checked = draft.selectedOptionKeys.includes(option.key);
            return (
              <label className="co-question-option" key={option.key}>
                <input
                  type={question.multiple ? "checkbox" : "radio"}
                  name={`co-question-${question.key}`}
                  aria-label={option.label}
                  aria-describedby={detailId}
                  checked={checked}
                  disabled={pending}
                  onChange={() => {
                    const next = question.multiple
                      ? checked
                        ? draft.selectedOptionKeys.filter(
                            (k) => k !== option.key,
                          )
                        : [...draft.selectedOptionKeys, option.key]
                      : checked
                        ? []
                        : [option.key];
                    dispatch({
                      type: "setQuestionDraft",
                      key: question.key,
                      value: { ...draft, selectedOptionKeys: next },
                    });
                  }}
                />
                <span>
                  <strong>{option.label}</strong>
                  <small id={detailId}>{option.detail}</small>
                </span>
              </label>
            );
          })}
        </div>
      </fieldset>

      <label className="co-field">
        <span>Note</span>
        <textarea
          aria-label="Note"
          value={draft.note}
          disabled={pending}
          onChange={(event) =>
            dispatch({
              type: "setQuestionDraft",
              key: question.key,
              value: { ...draft, note: event.currentTarget.value },
            })
          }
        />
      </label>

      <div className="co-question-actions">
        <button
          className="co-primary-action"
          type="submit"
          disabled={!valid || pending}
        >
          Submit answer
        </button>
      </div>
    </form>
  );
}

/* --------------------------- transcript content --------------------------- */

function TranscriptContent({
  item,
  state,
  dispatch,
}: {
  item: LiveTranscriptItem;
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}) {
  switch (item.kind) {
    case "user":
      return <p className="co-user-message">{item.body}</p>;
    case "assistant":
      return <p className="co-assistant-message">{item.body}</p>;
    case "tool":
      return (
        <div className="co-tool-disclosure">
          <Disclosure
            summary={<span>{item.label}</span>}
            expanded={state.ui.expandedToolKeys.has(item.key)}
            onToggle={() => dispatch({ type: "toggleTool", key: item.key })}
          >
            <div className="co-tool-detail">
              <StatusLabel state={item.tone} />
              <p>{item.body}</p>
            </div>
          </Disclosure>
        </div>
      );
    case "question":
      if (item.questionKey) {
        return (
          <QuestionCard
            questionKey={item.questionKey}
            state={state}
            dispatch={dispatch}
          />
        );
      }
      return (
        <section
          className="co-question-card co-question-card--missing"
          data-question-missing="true"
          role="alert"
        >
          <p className="co-question-card__error">Question unavailable</p>
          <p>This transcript item has no linked question.</p>
        </section>
      );
    case "failure":
      return (
        <section className="co-transcript-error" role="alert">
          <StatusLabel state="failed" />
          <p>{item.body}</p>
        </section>
      );
    case "attachment":
      return (
        <section className="co-attachment">
          <p className="co-eyebrow">Attachment</p>
          <p>{item.body}</p>
        </section>
      );
  }
}

/* ------------------------------ conversation view ------------------------------ */

export function ConversationView({ state, dispatch }: ConversationViewProps) {
  const conversation = state.conversation;
  const composer = state.composer;
  const ui = state.ui;
  const pending = composer.pending !== null;
  const anyTextMode =
    composer.canSend || composer.canSteer || composer.canQueue;
  const textareaDisabled = pending || !anyTextMode;

  if (!conversation) {
    return (
      <div className="co-conversation co-route-enter">
        <section className="co-inline-state">
          <h2>No conversation open</h2>
          <p>Open a session from the roster to view its transcript.</p>
        </section>
      </div>
    );
  }

  return (
    <div className="co-conversation co-route-enter">
      <section
        className="co-session-summary"
        aria-label={`Session ${conversation.title}`}
      >
        <div>
          <p className="co-eyebrow">{conversation.project}</p>
          <h2>{conversation.title}</h2>
          {conversation.updatedLabel ? (
            <p className="co-session-summary__updated">
              {conversation.updatedLabel}
            </p>
          ) : null}
        </div>
        <StatusLabel state={conversation.tone} />
      </section>

      <fieldset className="co-session-actions">
        <legend className="co-visually-hidden">Session tools</legend>
        <button type="button" onClick={() => dispatch({ type: "openWork" })}>
          <Icon name="work" decorative />
          Work
        </button>
      </fieldset>

      {conversation.olderAvailable ? (
        <div className="co-older-available" data-older-available="true">
          <button
            type="button"
            disabled={pending}
            onClick={() => dispatch({ type: "loadOlder" })}
          >
            Load older messages
          </button>
        </div>
      ) : null}

      <section className="co-transcript" aria-label="Transcript">
        {conversation.items.length === 0 ? (
          <div className="co-inline-state">
            <h2>No transcript yet</h2>
            <p>Send the first message to begin this live thread.</p>
          </div>
        ) : (
          conversation.items.map((item) => (
            <article
              className={`co-transcript-item co-transcript-item--${item.kind}`}
              data-transcript-item-id={item.key}
              data-testid={`transcript-item-${item.key}`}
              data-focused={ui.focusedItemKey === item.key ? "true" : "false"}
              data-streaming={item.streaming ? "true" : undefined}
              data-truncated={item.truncated ? "true" : undefined}
              data-current-work={
                item.kind === "tool" && item.tone === "running"
                  ? "true"
                  : undefined
              }
              aria-label={transcriptItemAxLabel(item)}
              tabIndex={ui.focusedItemKey === item.key ? -1 : undefined}
              key={item.key}
            >
              <TranscriptContent
                item={item}
                state={state}
                dispatch={dispatch}
              />
            </article>
          ))
        )}
      </section>

      {composer.pending ? (
        <section
          className="co-pending-mutation"
          data-pending-mutation="true"
          role="status"
          aria-live="polite"
          aria-label={mutationAxLabel(composer) ?? undefined}
        >
          <span>{mutationAxLabel(composer)}</span>
        </section>
      ) : null}

      {composer.pending === null && composer.accepted ? (
        <section
          className="co-pending-mutation"
          role="status"
          aria-label={mutationAxLabel(composer) ?? undefined}
        >
          {mutationAxLabel(composer)}
        </section>
      ) : null}

      {composer.error ? (
        <section className="co-composer-error" role="alert">
          <p>{composer.error}</p>
        </section>
      ) : null}

      <section className="co-composer" aria-label="Message composer">
        <fieldset className="co-composer__modes">
          <legend className="co-visually-hidden">Composer mode</legend>
          {COMPOSER_MODES.map((mode) => (
            <button
              type="button"
              aria-label={`Use ${mode} mode`}
              aria-pressed={ui.composerMode === mode}
              disabled={pending || !capabilityForMode(composer, mode)}
              key={mode}
              onClick={() => dispatch({ type: "setComposerMode", mode })}
            >
              {mode.charAt(0).toUpperCase() + mode.slice(1)}
            </button>
          ))}
        </fieldset>
        <label>
          <span className="co-visually-hidden">Message</span>
          <textarea
            className="co-composer__input"
            placeholder="Message or steer…"
            value={composer.draft}
            disabled={textareaDisabled}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
        <button
          className="co-primary-action co-composer__submit"
          type="button"
          aria-label="Submit message"
          disabled={composer.draft.trim().length === 0 || pending}
          onClick={() => dispatch({ type: "submit", mode: ui.composerMode })}
        >
          <Icon name="send" decorative />
          <span>Submit message</span>
        </button>
        {composer.canInterrupt ? (
          <button
            type="button"
            aria-label="Interrupt"
            onClick={() => dispatch({ type: "interrupt" })}
          >
            <Icon name="stop" decorative />
            <span>Interrupt</span>
          </button>
        ) : null}
      </section>
    </div>
  );
}
