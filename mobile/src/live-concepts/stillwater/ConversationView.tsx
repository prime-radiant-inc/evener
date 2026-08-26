import type { ReactNode } from "react";
import type {
  LiveConceptIntent,
  LiveConceptState,
  QuestionDraft,
} from "../contract";
import type { LiveQuestionView, LiveTranscriptItem } from "../model";
import { Disclosure } from "../shared/Disclosure";
import { Icon } from "../shared/Icon";
import { StatusLabel } from "../shared/StatusLabel";

export interface ConversationViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

function QuestionCard({
  question,
  draft,
  dispatch,
}: {
  question: LiveQuestionView;
  draft: QuestionDraft | undefined;
  dispatch(intent: LiveConceptIntent): void;
}) {
  const selected = draft?.selectedOptionKeys ?? [];
  const resolution = draft?.resolution ?? null;

  if (resolution !== null) {
    return (
      <section
        className="sw-question-card sw-question-card--resolved"
        data-question-id={question.key}
        data-question-resolution={resolution}
        role="status"
      >
        <StatusLabel state="success" />
        <h3>{question.prompt}</h3>
        <p>
          {resolution === "answer"
            ? "Answer submitted"
            : resolution === "fallback"
              ? "Fallback selected"
              : resolution === "decide"
                ? "Decision delegated"
                : "Question skipped"}
        </p>
      </section>
    );
  }

  return (
    <form
      className="sw-question-card"
      data-question-id={question.key}
      onSubmit={(event) => {
        event.preventDefault();
        dispatch({ type: "submitQuestion", key: question.key });
      }}
    >
      <fieldset>
        <legend>
          <span className="sw-eyebrow">
            {question.multiple ? "Choose one or more" : "Choose one"}
          </span>
          <span>{question.prompt}</span>
        </legend>
        <div className="sw-question-options">
          {question.options.map((option) => {
            const detailId = `question-${question.key}-option-${option.key}-detail`;
            return (
              <label className="sw-question-option" key={option.key}>
                <input
                  type={question.multiple ? "checkbox" : "radio"}
                  name={`question-${question.key}`}
                  aria-label={option.label}
                  aria-describedby={detailId}
                  checked={selected.includes(option.key)}
                  onChange={(event) =>
                    dispatch({
                      type: "setQuestionDraft",
                      key: question.key,
                      value: {
                        selectedOptionKeys: event.currentTarget.checked
                          ? [...selected, option.key]
                          : selected.filter((key) => key !== option.key),
                        note: draft?.note ?? "",
                        resolution: null,
                      },
                    })
                  }
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

      <label className="sw-field">
        <span>Note</span>
        <textarea
          aria-label="Note"
          value={draft?.note ?? ""}
          onChange={(event) =>
            dispatch({
              type: "setQuestionDraft",
              key: question.key,
              value: {
                selectedOptionKeys: selected,
                note: event.currentTarget.value,
                resolution: null,
              },
            })
          }
        />
      </label>

      <div className="sw-question-actions">
        <button className="sw-primary-action" type="submit">
          Submit answer
        </button>
      </div>
    </form>
  );
}

function TranscriptContent({
  item,
  state,
  dispatch,
}: {
  item: LiveTranscriptItem;
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}): ReactNode {
  switch (item.kind) {
    case "user":
      return <p className="sw-user-message">{item.body}</p>;
    case "assistant":
      return <p className="sw-assistant-message">{item.body}</p>;
    case "tool":
      return (
        <div className="sw-tool-disclosure">
          <Disclosure
            summary={<span>{item.label}</span>}
            expanded={state.ui.expandedToolKeys.has(item.key)}
            onToggle={() => dispatch({ type: "toggleTool", key: item.key })}
          >
            <div className="sw-tool-detail">
              <StatusLabel state={item.tone} />
              <pre>{item.body}</pre>
            </div>
          </Disclosure>
        </div>
      );
    case "question": {
      const question = state.conversation?.questions.find(
        ({ key }) => key === item.key,
      );
      if (!question) return null;
      const draft = state.ui.questionDrafts[question.key];
      return (
        <QuestionCard question={question} draft={draft} dispatch={dispatch} />
      );
    }
    case "failure":
      return (
        <section className="sw-transcript-error" role="alert">
          <StatusLabel state="failed" />
          <h3>{item.label}</h3>
          <p>{item.body}</p>
        </section>
      );
    case "attachment":
      return (
        <section className="sw-attachment">
          <p className="sw-eyebrow">Attachment</p>
          <h3>{item.label}</h3>
          <p>{item.body}</p>
        </section>
      );
  }
}

const composerModes = ["send", "steer", "queue"] as const;

function canSubmit(
  mode: "send" | "steer" | "queue",
  composer: LiveConceptState["composer"],
): boolean {
  if (mode === "send") return composer.canSend;
  if (mode === "steer") return composer.canSteer;
  return composer.canQueue;
}

export function ConversationView({ state, dispatch }: ConversationViewProps) {
  const { conversation, composer, ui } = state;
  if (!conversation) {
    return (
      <section className="sw-inline-state">
        <h2>No conversation</h2>
        <p>Open a session from the roster.</p>
      </section>
    );
  }

  const mode = ui.composerMode;

  return (
    <div className="sw-conversation">
      <section className="sw-session-summary" aria-label="Session summary">
        <div>
          <p className="sw-eyebrow">{conversation.project}</p>
          <h2>{conversation.title}</h2>
          <p>{conversation.status}</p>
        </div>
        <StatusLabel state="running" />
      </section>

      <fieldset className="sw-session-actions">
        <legend className="sw-visually-hidden">Session tools</legend>
        <button type="button" onClick={() => dispatch({ type: "openWork" })}>
          <Icon name="work" decorative />
          Work
        </button>
      </fieldset>

      <section className="sw-transcript" aria-label="Transcript">
        {conversation.items.length === 0 ? (
          <div className="sw-inline-state">
            <h2>No transcript yet</h2>
            <p>Send the first message to begin this thread.</p>
          </div>
        ) : (
          conversation.items.map((item) => (
            <article
              className={`sw-transcript-item sw-transcript-item--${item.kind}`}
              data-transcript-item-id={item.key}
              data-focused={ui.focusedItemKey === item.key ? "true" : "false"}
              data-streaming={item.streaming ? "true" : "false"}
              data-truncated={item.truncated ? "true" : "false"}
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

      {conversation.olderAvailable ? (
        <p className="sw-older-available" data-older-available="true">
          Older messages available
        </p>
      ) : null}

      {composer.pending ? (
        <section
          className="sw-composer-status"
          data-composer-pending={composer.pending.status}
          role="status"
        >
          <span>
            {composer.pending.status === "pending"
              ? `Sending (${composer.pending.kind})`
              : `${composer.pending.kind} failed`}
          </span>
          {composer.pending.status === "pending" && composer.canInterrupt ? (
            <button
              type="button"
              onClick={() => dispatch({ type: "interrupt" })}
            >
              Interrupt
            </button>
          ) : null}
        </section>
      ) : null}

      {composer.error ? (
        <div className="sw-banner" role="alert" data-composer-error>
          {composer.error}
        </div>
      ) : null}

      <section className="sw-composer" aria-label="Message composer">
        <fieldset className="sw-composer__modes">
          <legend className="sw-visually-hidden">Composer mode</legend>
          {composerModes.map((m) => (
            <button
              type="button"
              aria-pressed={mode === m}
              disabled={!canSubmit(m, composer)}
              key={m}
              onClick={() => dispatch({ type: "submit", mode: m })}
            >
              {m.charAt(0).toUpperCase() + m.slice(1)}
            </button>
          ))}
        </fieldset>
        <label>
          <span className="sw-visually-hidden">Message</span>
          <textarea
            aria-label="Message"
            placeholder="Message or steer…"
            value={composer.draft}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
      </section>
    </div>
  );
}
