import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveTranscriptItem } from "../model";
import { Icon } from "../shared/Icon";
import { StatusLabel } from "../shared/StatusLabel";
import { StatusDisclosure } from "./StatusDisclosure";

// Live adaptation of the Field Notes conversation surface. Preserves the
// chronology rail, stable item sequence markers, user/assistant margin
// labels, and current-record state. Dates come from the live conversation
// status; chapter numbers come from item position. No synthetic turn
// controls, no fixture.

export interface ConversationViewProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

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
      return (
        <div className="fn-entry-copy">
          <p className="fn-margin-label" data-margin-label="user">
            Your note
          </p>
          <p className="fn-user-message fn-editorial" data-editorial-reading>
            {item.body}
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
            {item.body}
          </p>
        </div>
      );
    case "tool":
      return (
        <div className="fn-tool-disclosure">
          <StatusDisclosure
            label={item.label}
            status={item.tone}
            expanded={state.ui.expandedToolKeys.has(item.key)}
            onToggle={() => dispatch({ type: "toggleTool", key: item.key })}
          >
            <div className="fn-tool-detail">
              <dl>
                <div>
                  <dt>Body</dt>
                  <dd>{item.body}</dd>
                </div>
              </dl>
            </div>
          </StatusDisclosure>
        </div>
      );
    case "question":
      return (
        <section className="fn-question-summary" data-question-id={item.key}>
          <h3>{item.label}</h3>
          <p>{item.body}</p>
        </section>
      );
    case "failure":
      return (
        <section className="fn-transcript-error" role="alert">
          <StatusLabel state="failed" />
          <h3>{item.label}</h3>
          <p>{item.body}</p>
        </section>
      );
    case "attachment":
      return (
        <section className="fn-attachment">
          <p className="fn-eyebrow">Attachment · {item.label}</p>
          <h3>{item.label}</h3>
          <p>{item.body}</p>
        </section>
      );
    default: {
      // Exhaustive guard: the live transcript kind union is closed.
      const _exhaustive: never = item.kind;
      return <p>{_exhaustive}</p>;
    }
  }
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
  const composerDisabled =
    offline || !composer.canSend || composer.pending !== null;

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

      <section className="fn-session-summary" aria-label="Record summary">
        <div>
          <p className="fn-eyebrow">{conversation.project}</p>
          <h2>{conversation.title}</h2>
          <p>{conversation.status}</p>
        </div>
        <StatusLabel state="running" />
      </section>

      <fieldset className="fn-session-actions">
        <legend className="fn-visually-hidden">Record tools</legend>
        <button type="button" onClick={() => dispatch({ type: "openWork" })}>
          <Icon name="work" decorative />
          Work
        </button>
      </fieldset>

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
          items.map((item, itemIndex) => {
            const isCurrent = item.kind === "tool" && item.tone === "running";
            return (
              <article
                className={`fn-transcript-item fn-transcript-item--${item.kind}`}
                data-transcript-item-id={item.key}
                data-focused={
                  state.ui.focusedItemKey === item.key ? "true" : "false"
                }
                data-spatial-presentation={
                  item.kind === "tool" ? "reading-flow" : undefined
                }
                data-current-record={isCurrent ? "true" : undefined}
                key={item.key}
              >
                <div className="fn-chronology-stamp" data-chronology-marker>
                  <span>{conversation.status}</span>
                  <span>Record {String(itemIndex + 1).padStart(2, "0")}</span>
                </div>
                <TranscriptContent
                  item={item}
                  state={state}
                  dispatch={dispatch}
                />
              </article>
            );
          })
        )}
      </section>

      {composer.error ? (
        <section className="fn-composer-error" role="alert">
          {composer.error}
        </section>
      ) : null}

      <section className="fn-composer" aria-label="Message composer">
        <fieldset className="fn-composer__modes">
          <legend className="fn-visually-hidden">Composer mode</legend>
          {(["send", "steer", "queue"] as const).map((mode) => (
            <button
              type="button"
              aria-pressed={state.ui.composerMode === mode}
              disabled={composer.pending !== null}
              key={mode}
              onClick={() => dispatch({ type: "submit", mode })}
            >
              {mode.charAt(0).toUpperCase() + mode.slice(1)}
            </button>
          ))}
        </fieldset>
        <label>
          <span className="fn-visually-hidden">Message</span>
          <textarea
            aria-label="Message"
            placeholder="Message or steer…"
            value={composer.draft}
            disabled={composerDisabled}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
        <button
          className="fn-primary-action fn-composer__submit"
          type="button"
          aria-label="Submit message"
          disabled={
            composer.draft.trim().length === 0 || composer.pending !== null
          }
          onClick={() =>
            dispatch({ type: "submit", mode: state.ui.composerMode })
          }
        >
          <Icon name="send" decorative />
          <span>Submit message</span>
        </button>
      </section>
    </div>
  );
}
