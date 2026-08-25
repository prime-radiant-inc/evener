import type { TranscriptItem } from "../../core/model";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Disclosure } from "../shared/Disclosure";
import { Icon } from "../shared/Icon";
import { ScreenState } from "../shared/ScreenState";
import { StatusLabel } from "../shared/StatusLabel";
import { QuestionCard } from "./QuestionCard";

export interface ConversationViewProps {
  state: PrototypeState;
  sessionId: string;
  dispatch(action: PrototypeAction): void;
}

function TranscriptContent({
  item,
  state,
  dispatch,
}: {
  item: TranscriptItem;
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}) {
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
            expanded={state.expandedToolIds.has(item.id)}
            onToggle={() => dispatch({ type: "toggleTool", itemId: item.id })}
          >
            <div className="sw-tool-detail">
              <StatusLabel state={item.status} />
              <dl>
                <div>
                  <dt>Arguments</dt>
                  <dd>{item.arguments}</dd>
                </div>
                <div>
                  <dt>Output</dt>
                  <dd>
                    <pre>{item.output}</pre>
                  </dd>
                </div>
              </dl>
            </div>
          </Disclosure>
        </div>
      );
    case "question": {
      const question = state.projection.fixture.questions.find(
        ({ id }) => id === item.questionId,
      );
      return question ? (
        <QuestionCard question={question} state={state} dispatch={dispatch} />
      ) : (
        <ScreenState
          state={{
            kind: "error",
            title: "Question unavailable",
            detail: "This transcript item has no canonical question fixture.",
          }}
        />
      );
    }
    case "error":
      return (
        <section className="sw-transcript-error" role="alert">
          <StatusLabel state="failed" />
          <h3>{item.title}</h3>
          <p>{item.detail}</p>
        </section>
      );
    case "attachment":
      return (
        <section className="sw-attachment">
          <p className="sw-eyebrow">Attachment · {item.mediaType}</p>
          <h3>{item.name}</h3>
          <p>{item.description}</p>
        </section>
      );
  }
}

export function ConversationView({
  state,
  sessionId,
  dispatch,
}: ConversationViewProps) {
  const session = state.projection.fixture.sessions.find(
    ({ id }) => id === sessionId,
  );
  if (!session || state.selectedSessionId !== sessionId) {
    return (
      <ScreenState
        state={{
          kind: "error",
          title: "Session unavailable",
          detail: "Return to Sessions and choose an available fixture.",
        }}
        retryAction={() => dispatch({ type: "goBack" })}
      />
    );
  }
  const transcript = state.projection.fixture.transcript.filter(
    (item) => item.sessionId === sessionId,
  );
  const running = state.syntheticTurn === "starting";

  return (
    <div className="sw-conversation sw-route-enter">
      <section className="sw-session-summary" aria-label="Session summary">
        <div>
          <p className="sw-eyebrow">{session.project}</p>
          <h2>{session.title}</h2>
          <p>{session.summary}</p>
        </div>
        <StatusLabel state={session.state} />
      </section>

      <fieldset className="sw-session-actions">
        <legend className="sw-visually-hidden">Session tools</legend>
        <button
          type="button"
          onClick={() => dispatch({ type: "openWork", sessionId })}
        >
          <Icon name="work" decorative />
          Work
        </button>
        <button
          type="button"
          onClick={() => dispatch({ type: "openVoice", sessionId })}
        >
          <Icon name="voice" decorative />
          Voice
        </button>
      </fieldset>

      <section className="sw-transcript" aria-label="Transcript">
        {transcript.length === 0 ? (
          <div className="sw-inline-state">
            <h2>No transcript yet</h2>
            <p>Send the first message to begin this local prototype thread.</p>
          </div>
        ) : (
          transcript.map((item) => (
            <article
              className={`sw-transcript-item sw-transcript-item--${item.kind}`}
              data-transcript-item-id={item.id}
              data-focused={state.focusedItemId === item.id ? "true" : "false"}
              key={item.id}
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

      {state.syntheticTurn !== "none" ? (
        <section
          className="sw-synthetic-turn sw-resolve-enter"
          data-synthetic-turn={state.syntheticTurn}
          role="status"
        >
          <span>
            {running
              ? "Response is running"
              : state.syntheticTurn === "complete"
                ? "Response completed"
                : "Response stopped"}
          </span>
          {running ? (
            <span className="sw-synthetic-turn__actions">
              <button
                type="button"
                onClick={() => dispatch({ type: "completeSyntheticTurn" })}
              >
                Complete response
              </button>
              <button
                type="button"
                onClick={() => dispatch({ type: "stopSyntheticTurn" })}
              >
                Stop response
              </button>
            </span>
          ) : null}
        </section>
      ) : null}

      <section className="sw-composer" aria-label="Message composer">
        <fieldset className="sw-composer__modes">
          <legend className="sw-visually-hidden">Composer mode</legend>
          {(["send", "steer", "queue"] as const).map((mode) => (
            <button
              type="button"
              aria-pressed={state.composerMode === mode}
              disabled={running}
              key={mode}
              onClick={() => dispatch({ type: "setComposerMode", mode })}
            >
              {mode.charAt(0).toUpperCase() + mode.slice(1)}
            </button>
          ))}
        </fieldset>
        <label>
          <span className="sw-visually-hidden">Message</span>
          <textarea
            aria-label="Message"
            placeholder="Message or steer…"
            value={state.draft}
            disabled={running}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
        <button
          className="sw-primary-action sw-composer__submit"
          type="button"
          aria-label="Submit message"
          disabled={state.draft.trim().length === 0 || running}
          onClick={() => dispatch({ type: "submitComposer" })}
        >
          <Icon name="send" decorative />
          <span>Submit message</span>
        </button>
      </section>
    </div>
  );
}
