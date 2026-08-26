import { useEffect, useRef } from "react";
import type { TranscriptItem } from "../../core/model";
import { selectCanMutate } from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Icon } from "../shared/Icon";
import { ScreenState } from "../shared/ScreenState";
import { StatusLabel } from "../shared/StatusLabel";
import { QuestionCard } from "./QuestionCard";
import {
  BlockingRouteState,
  isBlockingRouteState,
  OfflineNotice,
} from "./RouteState";
import { StatusDisclosure } from "./StatusDisclosure";

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
      return (
        <div className="fn-entry-copy">
          <p className="fn-margin-label">Your note</p>
          <p className="fn-user-message fn-editorial" data-editorial-reading>
            {item.body}
          </p>
        </div>
      );
    case "assistant":
      return (
        <div className="fn-entry-copy">
          <p className="fn-margin-label">Assistant · response</p>
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
            status={item.status}
            expanded={state.expandedToolIds.has(item.id)}
            onToggle={() => dispatch({ type: "toggleTool", itemId: item.id })}
          >
            <div className="fn-tool-detail">
              <dl>
                <div>
                  <dt>Arguments</dt>
                  <dd>{item.arguments}</dd>
                </div>
                <div>
                  <dt>Output</dt>
                  <dd>
                    <section
                      className="fn-tool-output"
                      data-long-tool-output
                      aria-label={`${item.label} output`}
                    >
                      <pre>{item.output}</pre>
                    </section>
                  </dd>
                </div>
              </dl>
            </div>
          </StatusDisclosure>
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
        <section className="fn-transcript-error" role="alert">
          <StatusLabel state="failed" />
          <h3>{item.title}</h3>
          <p>{item.detail}</p>
        </section>
      );
    case "attachment":
      return (
        <section className="fn-attachment">
          <p className="fn-eyebrow">Attachment · {item.mediaType}</p>
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
  const transcriptRef = useRef<HTMLElement>(null);
  const session = state.projection.fixture.sessions.find(
    ({ id }) => id === sessionId,
  );
  const focusedItemId = state.focusedItemId;

  useEffect(() => {
    if (!focusedItemId) return;
    const target = Array.from(
      transcriptRef.current?.querySelectorAll<HTMLElement>(
        "[data-transcript-item-id]",
      ) ?? [],
    ).find((element) => element.dataset.transcriptItemId === focusedItemId);
    if (!target) return;
    target.focus({ preventScroll: true });
    const scroller = target.closest<HTMLElement>(".fn-scroll-owner");
    if (!scroller || typeof scroller.scrollTo !== "function") return;
    const targetBounds = target.getBoundingClientRect();
    const scrollerBounds = scroller.getBoundingClientRect();
    const centeredOffset =
      targetBounds.top -
      scrollerBounds.top -
      (scroller.clientHeight - targetBounds.height) / 2;
    const effectiveReducedMotion =
      state.reducedMotion ||
      document.documentElement.dataset.reducedMotion === "true";
    scroller.scrollTo({
      top: Math.max(0, scroller.scrollTop + centeredOffset),
      behavior: effectiveReducedMotion ? "auto" : "smooth",
    });
  }, [focusedItemId, state.reducedMotion]);

  if (isBlockingRouteState(state)) {
    return (
      <BlockingRouteState
        state={state}
        routeLabel="Conversation"
        dispatch={dispatch}
      />
    );
  }
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
  const canMutate = selectCanMutate(state);

  return (
    <div className="fn-conversation fn-route-enter">
      <OfflineNotice
        state={state}
        routeLabel="Conversation"
        mutationDetail="You can read saved evidence, but cannot send or resolve questions offline."
        dispatch={dispatch}
      />
      <section className="fn-session-summary" aria-label="Session summary">
        <div>
          <p className="fn-eyebrow">{session.project}</p>
          <h2>{session.title}</h2>
          <p>{session.summary}</p>
        </div>
        <StatusLabel state={session.state} />
      </section>

      <fieldset className="fn-session-actions">
        <legend className="fn-visually-hidden">Session tools</legend>
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

      <section
        className="fn-transcript"
        aria-label="Transcript"
        data-chronology-rail
        ref={transcriptRef}
      >
        {transcript.length === 0 ? (
          <div className="fn-inline-state">
            <h2>No transcript yet</h2>
            <p>Send the first message to begin this local prototype thread.</p>
          </div>
        ) : (
          transcript.map((item, itemIndex) => (
            <article
              className={`fn-transcript-item fn-transcript-item--${item.kind}`}
              data-transcript-item-id={item.id}
              data-focused={state.focusedItemId === item.id ? "true" : "false"}
              data-spatial-presentation={
                item.kind === "tool" ? "reading-flow" : undefined
              }
              data-current-work={
                item.kind === "tool" && item.status === "running"
                  ? "true"
                  : undefined
              }
              tabIndex={state.focusedItemId === item.id ? -1 : undefined}
              key={item.id}
            >
              <div className="fn-chronology-stamp" data-chronology-marker>
                <span>Updated {session.updatedLabel}</span>
                <span>Chapter {String(itemIndex + 1).padStart(2, "0")}</span>
              </div>
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
          className="fn-synthetic-turn fn-resolve-enter"
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
            <span className="fn-synthetic-turn__actions">
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

      <section className="fn-composer" aria-label="Message composer">
        <fieldset className="fn-composer__modes">
          <legend className="fn-visually-hidden">Composer mode</legend>
          {(["send", "steer", "queue"] as const).map((mode) => (
            <button
              type="button"
              aria-pressed={state.composerMode === mode}
              disabled={running || !canMutate}
              key={mode}
              onClick={() => dispatch({ type: "setComposerMode", mode })}
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
            value={state.draft}
            disabled={running || !canMutate}
            onChange={(event) =>
              dispatch({ type: "setDraft", value: event.currentTarget.value })
            }
          />
        </label>
        <button
          className="fn-primary-action fn-composer__submit"
          type="button"
          aria-label="Submit message"
          disabled={state.draft.trim().length === 0 || running || !canMutate}
          onClick={() => dispatch({ type: "submitComposer" })}
        >
          <Icon name="send" decorative />
          <span>Submit message</span>
        </button>
      </section>
    </div>
  );
}
