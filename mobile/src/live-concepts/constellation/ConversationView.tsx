import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveTranscriptItem } from "../model";
import { Disclosure } from "../shared/Disclosure";
import { Icon } from "../shared/Icon";
import { StatusLabel } from "../shared/StatusLabel";

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
      return (
        <section className="co-transcript-question">
          <StatusLabel state={item.tone} />
          <p>{item.body}</p>
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

export function ConversationView({ state, dispatch }: ConversationViewProps) {
  const conversation = state.conversation;
  const composer = state.composer;
  const ui = state.ui;
  const running = composer.pending !== null;
  const disabled = running || !composer.canSend;

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
      <section className="co-session-summary" aria-label="Session summary">
        <div>
          <p className="co-eyebrow">{conversation.project}</p>
          <h2>{conversation.title}</h2>
          <p>{conversation.status}</p>
        </div>
        <StatusLabel
          state={conversation.status === "running" ? "running" : "idle"}
        />
      </section>

      <fieldset className="co-session-actions">
        <legend className="co-visually-hidden">Session tools</legend>
        <button type="button" onClick={() => dispatch({ type: "openWork" })}>
          <Icon name="work" decorative />
          Work
        </button>
      </fieldset>

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
        >
          <span>
            {composer.pending.status === "failed" ? "Failed" : "Sending"}{" "}
            {composer.pending.kind}…
          </span>
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
          {(["send", "steer", "queue"] as const).map((mode) => (
            <button
              type="button"
              aria-pressed={ui.composerMode === mode}
              disabled={running}
              key={mode}
              onClick={() => dispatch({ type: "submit", mode })}
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
            disabled={disabled}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
        <button
          className="co-primary-action co-composer__submit"
          type="button"
          aria-label="Submit message"
          disabled={composer.draft.trim().length === 0 || running}
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
