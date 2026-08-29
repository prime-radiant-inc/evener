import {
  conversationItemAxLabel,
  mutationAxLabel,
} from "../accessibility-semantics";
import type {
  LiveConceptIntent,
  LiveConceptState,
  QuestionDraft,
} from "../contract";
import type {
  BoundedDisplayText,
  ConversationDisplayItem,
  LiveQuestionView,
} from "../model";
import { Icon } from "../shared/Icon";
import { StatusLabel } from "../shared/StatusLabel";
import { StatusDisclosure } from "./StatusDisclosure";

// Live adaptation of the Field Notes conversation surface. Preserves the
// chronology rail, stable item sequence markers (adapter sequenceLabel),
// user/assistant margin labels, and current-record state. Chronology uses
// the authoritative updatedLabel when non-null, otherwise an honest "Live
// record" label. The summary uses conversation.tone (never hardcoded).
// Full questions link via item.questionKey. No synthetic turn controls,
// no fixture, no index-derived chapter numbers.

export interface ConversationViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

const MODE_CAPABILITY: Readonly<
  Record<"send" | "steer" | "queue", "canSend" | "canSteer" | "canQueue">
> = {
  send: "canSend",
  steer: "canSteer",
  queue: "canQueue",
};

function chronologyStampLabel(conversation: {
  updatedLabel: BoundedDisplayText | null;
}): string {
  return conversation.updatedLabel?.text ?? "Live record";
}

function TranscriptContent({
  item,
  state,
  dispatch,
  question,
}: {
  item: ConversationDisplayItem;
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
  question: LiveQuestionView | undefined;
}) {
  switch (item.sourceKind) {
    case "user":
      return (
        <div className="fn-entry-copy">
          <p className="fn-margin-label" data-margin-label="user">
            Your note
          </p>
          <p className="fn-user-message fn-editorial" data-editorial-reading>
            {item.body.text}
          </p>
        </div>
      );
    case "assistant":
      return (
        <div className="fn-entry-copy">
          <p className="fn-margin-label" data-margin-label="assistant">
            Assistant · response
          </p>
          <p
            className="fn-assistant-message fn-editorial"
            data-editorial-reading
          >
            {item.body.text}
          </p>
        </div>
      );
    case "notice":
    case "system":
    case "reasoning":
    case "tool":
    case "diagnostic":
    case "unknown":
      return (
        <div className="fn-tool-disclosure">
          <StatusDisclosure
            label={item.label.text}
            status={item.tone}
            expanded={state.ui.expandedToolKeys.has(item.key)}
            onToggle={() => dispatch({ type: "toggleTool", key: item.key })}
          >
            <div className="fn-tool-detail">
              <dl>
                <div>
                  <dt>Body</dt>
                  <dd>{item.preview?.text}</dd>
                </div>
              </dl>
            </div>
          </StatusDisclosure>
        </div>
      );
    case "question":
      return question ? (
        <QuestionCard question={question} state={state} dispatch={dispatch} />
      ) : (
        <section
          className="fn-question-missing"
          data-question-missing
          role="alert"
        >
          <h3>Question unavailable</h3>
          <p>
            This transcript item links to a question that is not present in the
            live record.
          </p>
        </section>
      );
    case "failure":
      return (
        <section className="fn-transcript-error" role="alert">
          <StatusLabel state="failed" />
          <h3>{item.label?.text}</h3>
          <p>{item.body.text}</p>
        </section>
      );
    case "attachment":
      return (
        <section className="fn-attachment">
          <p className="fn-eyebrow">Attachment · {item.label.text}</p>
          <h3>{item.label.text}</h3>
        </section>
      );
  }
}

function QuestionCard({
  question,
  state,
  dispatch,
}: {
  question: LiveQuestionView;
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}) {
  const draft: QuestionDraft = state.ui.questionDrafts[question.key] ?? {
    selectedOptionKeys: [],
    note: "",
    resolution: null,
  };
  const selected = new Set(draft.selectedOptionKeys);
  const isValid = draft.selectedOptionKeys.length > 0;
  const inputType = question.multiple ? "checkbox" : "radio";
  const name = `fn-question-${question.key}`;

  function setOption(key: string, checked: boolean): void {
    const next = question.multiple
      ? checked
        ? [...draft.selectedOptionKeys, key]
        : draft.selectedOptionKeys.filter((k) => k !== key)
      : checked
        ? [key]
        : [];
    dispatch({
      type: "setQuestionDraft",
      key: question.key,
      value: { ...draft, selectedOptionKeys: next },
    });
  }

  return (
    <form
      className="fn-question-card fn-resolve-enter"
      data-question-id={question.key}
      onSubmit={(event) => {
        event.preventDefault();
        dispatch({ type: "submitQuestion", key: question.key });
      }}
    >
      <fieldset>
        <legend>
          <span className="fn-eyebrow">
            {question.multiple ? "Choose one or more" : "Choose one"}
          </span>
          <span>{question.prompt.text}</span>
        </legend>
        <div className="fn-question-options">
          {question.options.map((option) => {
            const detailId = `fn-question-${question.key}-option-${option.key}`;
            return (
              <label className="fn-question-option" key={option.key}>
                <input
                  type={inputType}
                  name={name}
                  aria-label={option.label.text}
                  aria-describedby={detailId}
                  checked={selected.has(option.key)}
                  onChange={(event) =>
                    setOption(option.key, event.currentTarget.checked)
                  }
                />
                <span>
                  <strong>{option.label.text}</strong>
                  <small id={detailId}>{option.detail.text}</small>
                </span>
              </label>
            );
          })}
        </div>
      </fieldset>

      <label className="fn-field">
        <span>Note</span>
        <textarea
          aria-label="Note"
          value={draft.note}
          onChange={(event) =>
            dispatch({
              type: "setQuestionDraft",
              key: question.key,
              value: { ...draft, note: event.currentTarget.value },
            })
          }
        />
      </label>

      <div className="fn-question-actions">
        <button className="fn-primary-action" type="submit" disabled={!isValid}>
          Submit answer
        </button>
      </div>
    </form>
  );
}

export function ConversationView({ state, dispatch }: ConversationViewProps) {
  const { conversation, composer, connection } = state;
  if (!conversation) {
    return (
      <section className="fn-inline-state">
        <h2>No conversation selected</h2>
        <p>Open a record from Sessions to read its transcript.</p>
      </section>
    );
  }

  const offline =
    connection.status === "offline" || connection.status === "error";
  const items = conversation.items;
  const questionsByKey = new Map(conversation.questions.map((q) => [q.key, q]));
  const stampLabel = chronologyStampLabel(conversation);
  const pending = composer.pending !== null;
  const anyModeAvailable =
    composer.canSend || composer.canSteer || composer.canQueue;
  const textareaDisabled = offline || pending || !anyModeAvailable;
  const submitDisabled =
    offline || pending || composer.draft.trim().length === 0;

  return (
    <div className="fn-conversation fn-route-enter">
      {offline ? (
        <div
          className="fn-banner"
          data-connection-status={connection.status}
          role="status"
          aria-live="polite"
        >
          <span>
            Offline. You can read saved evidence, but cannot send or resolve
            questions.
          </span>
        </div>
      ) : null}

      <section
        className="fn-session-summary"
        aria-label={`Session ${conversation.title.text}`}
        data-conversation-tone={conversation.tone}
      >
        <div>
          <p className="fn-eyebrow">{conversation.project.text}</p>
          <h2>{conversation.title.text}</h2>
          <p>{conversation.status.text}</p>
        </div>
        <StatusLabel state={conversation.tone} />
      </section>

      <fieldset className="fn-session-actions">
        <legend className="fn-visually-hidden">Record tools</legend>
        <button type="button" onClick={() => dispatch({ type: "openWork" })}>
          <Icon name="work" decorative />
          Work
        </button>
      </fieldset>

      {conversation.olderAvailable ? (
        <button
          className="fn-older-records"
          data-older-available
          type="button"
          disabled={pending}
          onClick={() => dispatch({ type: "loadOlder" })}
        >
          Load older
        </button>
      ) : null}

      <section
        className="fn-transcript"
        aria-label="Transcript"
        data-chronology-rail
      >
        {items.length === 0 ? (
          <div className="fn-inline-state">
            <h2>No transcript yet</h2>
            <p>Send the first message to begin this live thread.</p>
          </div>
        ) : (
          items.map((item) => {
            const isCurrent =
              item.sourceKind === "tool" && item.tone === "running";
            return (
              <article
                className={`fn-transcript-item fn-transcript-item--${item.sourceKind}`}
                data-transcript-item-id={item.key}
                data-focused={
                  state.ui.focusedItemKey === item.key ? "true" : "false"
                }
                data-spatial-presentation={
                  item.sourceKind === "tool" ? "reading-flow" : undefined
                }
                data-current-record={isCurrent ? "true" : undefined}
                data-streaming={
                  "streaming" in item && item.streaming ? "true" : undefined
                }
                data-truncated={
                  ("body" in item ? item.body : item.preview)?.truncated
                    ? "true"
                    : undefined
                }
                aria-label={conversationItemAxLabel(
                  item,
                  item.sourceKind === "question"
                    ? (state.ui.questionDrafts[item.questionKey ?? ""]
                        ?.resolution ?? null)
                    : null,
                )}
                key={item.key}
              >
                <div className="fn-chronology-stamp" data-chronology-marker>
                  <span>{stampLabel}</span>
                  <span>{item.sequence}</span>
                </div>
                <TranscriptContent
                  item={item}
                  state={state}
                  dispatch={dispatch}
                  question={
                    item.sourceKind === "question" && item.questionKey
                      ? questionsByKey.get(item.questionKey)
                      : undefined
                  }
                />
              </article>
            );
          })
        )}
      </section>

      {composer.pending ? (
        <section
          className="fn-composer-pending"
          data-composer-pending
          role="status"
          aria-label={mutationAxLabel(composer) ?? undefined}
          aria-live="polite"
        >
          <span>{mutationAxLabel(composer)}</span>
        </section>
      ) : null}

      {composer.pending === null && composer.accepted ? (
        <section
          className="fn-composer-status"
          role="status"
          aria-label={mutationAxLabel(composer) ?? undefined}
        >
          {mutationAxLabel(composer)}
        </section>
      ) : null}

      {composer.error ? (
        <section className="fn-composer-error" data-composer-error role="alert">
          {composer.error}
        </section>
      ) : null}

      <section className="fn-composer" aria-label="Message composer">
        <fieldset className="fn-composer__modes">
          <legend className="fn-visually-hidden">Composer mode</legend>
          {(["send", "steer", "queue"] as const).map((mode) => {
            const capability = composer[MODE_CAPABILITY[mode]];
            return (
              <button
                type="button"
                aria-label={`Use ${mode} mode`}
                aria-pressed={state.ui.composerMode === mode}
                disabled={!capability || pending}
                key={mode}
                onClick={() => dispatch({ type: "setComposerMode", mode })}
              >
                {mode.charAt(0).toUpperCase() + mode.slice(1)}
              </button>
            );
          })}
        </fieldset>
        <label>
          <span className="fn-visually-hidden">Message</span>
          <textarea
            aria-label="Message"
            placeholder="Message or steer…"
            value={composer.draft}
            disabled={textareaDisabled}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
        <div className="fn-composer__actions">
          <button
            className="fn-primary-action fn-composer__submit"
            type="button"
            aria-label="Submit message"
            disabled={submitDisabled}
            onClick={() =>
              dispatch({ type: "submit", mode: state.ui.composerMode })
            }
          >
            <Icon name="send" decorative />
            <span>Submit message</span>
          </button>
          <button
            className="fn-composer__interrupt"
            type="button"
            aria-label="Interrupt"
            disabled={!composer.canInterrupt}
            onClick={() => dispatch({ type: "interrupt" })}
          >
            <Icon name="stop" decorative />
            <span>Interrupt</span>
          </button>
        </div>
      </section>
    </div>
  );
}
