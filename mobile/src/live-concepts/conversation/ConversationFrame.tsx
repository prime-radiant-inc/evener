import {
  type ReactElement,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type Ref,
  useLayoutEffect,
  useRef,
} from "react";
import { settleFocusedControlInVisualViewport } from "../../ui/platformPresentation";
import {
  conversationItemAxDescription,
  conversationItemAxLabel,
  mutationAxLabel,
  streamingAnnouncement,
} from "../accessibility-semantics";
import type {
  ConversationDisplayItem,
  LiveQuestionView,
  NarrativeDisplayItem,
} from "../model";
import type {
  ConversationFrameProps,
  ConversationFrameState,
  ConversationSkin,
} from "./contract";
import { DockedComposer } from "./DockedComposer";
import type { ConversationFrameAction, QuestionDraft } from "./primitives";
import { VirtualTranscript } from "./VirtualTranscript";
import "./conversation-frame.css";

const EMPTY_TEXT = Object.freeze({
  text: "",
  truncated: false,
  originalUtf8Bytes: 0,
});

function ConversationChrome({
  skin,
  state,
  dispatch,
  backRef,
}: {
  readonly skin: ConversationSkin;
  readonly state: ConversationFrameState;
  readonly dispatch: (action: ConversationFrameAction) => void;
  readonly backRef: Ref<HTMLButtonElement>;
}): ReactElement {
  const conversation = state.conversation;
  return (
    <header
      className="live-conversation-frame__chrome"
      data-frame-part="chrome"
    >
      <button
        ref={backRef}
        type="button"
        aria-label="Back"
        onFocus={(event) => {
          void settleFocusedControlInVisualViewport(event.currentTarget);
        }}
        onClick={() => dispatch({ type: "goBack" })}
      >
        Back
      </button>
      <div className="live-conversation-frame__chrome-ornament">
        {skin.renderConversationChrome({
          title: conversation?.title ?? EMPTY_TEXT,
          project: conversation?.project ?? EMPTY_TEXT,
          status: conversation?.status ?? EMPTY_TEXT,
          updatedLabel: conversation?.updatedLabel ?? null,
        })}
      </div>
      <nav
        className="live-conversation-frame__chrome-controls"
        aria-label="Conversation tools"
      >
        <button
          type="button"
          aria-label="Work"
          onFocus={(event) => {
            void settleFocusedControlInVisualViewport(event.currentTarget);
          }}
          onClick={() => dispatch({ type: "openWork" })}
        >
          Work
        </button>
        <button
          type="button"
          aria-label="Switch concept"
          onFocus={(event) => {
            void settleFocusedControlInVisualViewport(event.currentTarget);
          }}
          onClick={() => dispatch({ type: "openConceptSwitcher" })}
        >
          Switch concept
        </button>
      </nav>
    </header>
  );
}

function wrapTerminalComposerTab(
  event: ReactKeyboardEvent<HTMLElement>,
  backControl: HTMLButtonElement | null,
): void {
  if (
    event.defaultPrevented ||
    event.key !== "Tab" ||
    event.shiftKey ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey ||
    backControl === null
  ) {
    return;
  }
  const target = event.target;
  if (
    !(target instanceof HTMLTextAreaElement) ||
    target.dataset.liveConversationMessage !== "true"
  ) {
    return;
  }
  const composer = target.closest<HTMLElement>(
    '[data-frame-part="composer"][data-live-conversation-composer="true"]',
  );
  if (composer === null || !event.currentTarget.contains(composer)) return;
  const hasFollowingAction = [
    ...composer.querySelectorAll<HTMLButtonElement>("button"),
  ].some(
    (action) =>
      !action.disabled &&
      action.tabIndex >= 0 &&
      (target.compareDocumentPosition(action) &
        Node.DOCUMENT_POSITION_FOLLOWING) !==
        0,
  );
  if (hasFollowingAction) return;

  event.preventDefault();
  backControl.focus();
}

function nextSelection(
  question: LiveQuestionView,
  selected: readonly string[],
  optionKey: string,
  checked: boolean,
): readonly string[] {
  if (!question.multiple) return checked ? [optionKey] : [];
  return checked
    ? [...selected.filter((key) => key !== optionKey), optionKey]
    : selected.filter((key) => key !== optionKey);
}

function QuestionCard({
  question,
  draft,
  canSend,
  pending,
  dispatch,
}: {
  readonly question: LiveQuestionView;
  readonly draft: QuestionDraft | undefined;
  readonly canSend: boolean;
  readonly pending: boolean;
  readonly dispatch: (action: ConversationFrameAction) => void;
}): ReactElement {
  const value: QuestionDraft = draft ?? {
    selectedOptionKeys: [],
    note: "",
    resolution: null,
  };
  if (value.resolution !== null) {
    return (
      <section
        role="status"
        aria-label="Question resolved"
        data-question-resolution={value.resolution}
      >
        <h3>{question.prompt.text}</h3>
        <p>Response recorded</p>
      </section>
    );
  }
  const disabled = !canSend || pending;
  const setDraft = (next: QuestionDraft): void => {
    dispatch({ type: "setQuestionDraft", key: question.key, value: next });
  };
  const submit = (
    resolution: NonNullable<QuestionDraft["resolution"]>,
  ): void => {
    setDraft({ ...value, resolution });
    dispatch({ type: "submitQuestion", key: question.key });
  };

  return (
    <section data-question-id={question.key}>
      <fieldset disabled={disabled}>
        <legend>{question.prompt.text}</legend>
        <div className="live-conversation-frame__question-options">
          {question.options.map((option) => {
            const detailId = `conversation-question-${question.key}-${option.key}-detail`;
            return (
              <label
                className="live-conversation-frame__question-option"
                key={option.key}
              >
                <input
                  type={question.multiple ? "checkbox" : "radio"}
                  name={`conversation-question-${question.key}`}
                  aria-label={option.label.text}
                  aria-describedby={detailId}
                  checked={value.selectedOptionKeys.includes(option.key)}
                  onChange={(event) =>
                    setDraft({
                      ...value,
                      selectedOptionKeys: nextSelection(
                        question,
                        value.selectedOptionKeys,
                        option.key,
                        event.currentTarget.checked,
                      ),
                    })
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
      <label>
        <span>Note</span>
        <textarea
          aria-label="Note"
          value={value.note}
          disabled={disabled}
          onChange={(event) =>
            setDraft({ ...value, note: event.currentTarget.value })
          }
        />
      </label>
      <div className="live-conversation-frame__question-actions">
        <button
          type="button"
          aria-label="Submit answer"
          disabled={disabled || value.selectedOptionKeys.length === 0}
          onClick={() => submit("answer")}
        >
          Submit answer
        </button>
        {question.ifUnanswered !== null ? (
          <button
            type="button"
            aria-label="Use fallback"
            disabled={disabled}
            onClick={() => submit("fallback")}
          >
            Use fallback
          </button>
        ) : null}
        <button
          type="button"
          aria-label="Let Evener decide"
          disabled={disabled}
          onClick={() => submit("decide")}
        >
          Let Evener decide
        </button>
        <button
          type="button"
          aria-label="Skip question"
          disabled={disabled}
          onClick={() => submit("skip")}
        >
          Skip question
        </button>
      </div>
    </section>
  );
}

function NarrativeBody({
  item,
  state,
  dispatch,
}: {
  readonly item: NarrativeDisplayItem;
  readonly state: ConversationFrameState;
  readonly dispatch: (action: ConversationFrameAction) => void;
}): ReactNode {
  if (item.sourceKind !== "question") {
    return (
      <section role={item.sourceKind === "failure" ? "group" : undefined}>
        {item.label !== null ? <h3>{item.label.text}</h3> : null}
        <p>{item.body.text}</p>
      </section>
    );
  }
  const key = item.questionKey;
  const question = state.conversation?.questions.find(
    (candidate) => candidate.key === key,
  );
  if (key === null || question === undefined) {
    return (
      <section role="alert" aria-label="Question unavailable">
        <h3>Question unavailable</h3>
        <p>{item.body.text}</p>
      </section>
    );
  }
  return (
    <QuestionCard
      question={question}
      draft={state.questionDrafts[key]}
      canSend={state.composer.canSend}
      pending={state.composer.pending?.status === "pending"}
      dispatch={dispatch}
    />
  );
}

function TranscriptItem({
  item,
  index,
  total,
  state,
  skin,
  dispatch,
  onFocusIntentChange,
}: {
  readonly item: ConversationDisplayItem;
  readonly index: number;
  readonly total: number;
  readonly state: ConversationFrameState;
  readonly skin: ConversationSkin;
  readonly dispatch: (action: ConversationFrameAction) => void;
  readonly onFocusIntentChange: (key: string | null) => void;
}): ReactElement {
  const description = conversationItemAxDescription(item);
  const descriptionId =
    description === null ? undefined : `conversation-preview-${item.key}`;
  const focused = state.focusedItemKey === item.key;
  const questionResolution =
    item.sourceKind === "question"
      ? (state.questionDrafts[item.questionKey ?? ""]?.resolution ?? null)
      : null;
  const rendered =
    "body" in item
      ? skin.renderNarrativeItem({
          item,
          body: <NarrativeBody item={item} state={state} dispatch={dispatch} />,
          focused,
        })
      : skin.renderActivityMarker({ item, focused });
  return (
    <article
      aria-label={conversationItemAxLabel(item, questionResolution)}
      aria-describedby={descriptionId}
      aria-posinset={index + 1}
      aria-setsize={total}
      data-transcript-item-id={item.key}
      data-focused={focused ? "true" : "false"}
      data-streaming={"streaming" in item && item.streaming ? "true" : "false"}
      tabIndex={focused ? -1 : undefined}
      onFocus={() => onFocusIntentChange(item.key)}
      onBlur={() => onFocusIntentChange(null)}
    >
      {descriptionId !== undefined ? (
        <span className="live-conversation-visually-hidden" id={descriptionId}>
          {description}
        </span>
      ) : null}
      {rendered}
      {item.evidenceKey !== null ? (
        <button
          type="button"
          aria-label="Open evidence"
          onClick={() =>
            dispatch({
              type: "openEvidence",
              evidenceKey: item.evidenceKey as string,
              triggerKey: item.key,
            })
          }
        >
          Evidence
        </button>
      ) : null}
    </article>
  );
}

function ConversationStatus({
  state,
  dispatch,
  streamingMessage,
}: {
  readonly state: ConversationFrameState;
  readonly dispatch: (action: ConversationFrameAction) => void;
  readonly streamingMessage: string | null;
}): ReactElement {
  const mutationLabel = mutationAxLabel(state.composer);
  const mutationFailed = state.composer.pending?.status === "failed";
  const readFailed = state.phase === "read-error";
  const connectionFailed =
    state.connection.status === "error" ||
    state.connection.status === "offline";
  const alert = mutationFailed || readFailed || connectionFailed;
  const noDataReadFailure = readFailed && state.conversation === null;
  const retryRef = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    if (noDataReadFailure) retryRef.current?.focus();
  }, [noDataReadFailure]);
  return (
    <section
      className="live-conversation-frame__status"
      data-frame-part="status"
    >
      {state.phase === "loading" ? (
        <p role="status">Loading conversation</p>
      ) : null}
      {state.phase === "empty" ? <p role="status">No conversation</p> : null}
      {streamingMessage !== null ? (
        <p role="status" aria-live="polite" aria-atomic="true">
          {streamingMessage}
        </p>
      ) : null}
      {!mutationFailed && mutationLabel !== null ? (
        <p role="status" aria-label={mutationLabel}>
          {mutationLabel}
        </p>
      ) : null}
      {alert ? (
        <div role="alert">
          {connectionFailed ? (
            <p>
              {state.connection.status === "offline"
                ? "Offline"
                : "Connection error"}
            </p>
          ) : null}
          {mutationFailed ? <p>{mutationLabel}</p> : null}
          {mutationFailed && state.composer.error !== null ? (
            <p>{state.composer.error.text}</p>
          ) : null}
          {readFailed && !mutationFailed && state.composer.error !== null ? (
            <p>{state.composer.error.text}</p>
          ) : null}
          {readFailed ? (
            <button
              type="button"
              aria-label="Retry conversation"
              ref={retryRef}
              onClick={() => dispatch({ type: "retryRead" })}
            >
              Retry conversation
            </button>
          ) : null}
        </div>
      ) : null}
      {state.unseen > 0 ? (
        <p role="status">{state.unseen} unseen messages</p>
      ) : null}
    </section>
  );
}

export function ConversationFrame({
  state,
  skin,
  dispatch,
  onAnchorChange: _onAnchorChange,
  onUnseenChange: _onUnseenChange,
  onFocusIntentChange,
}: ConversationFrameProps): ReactElement {
  const backRef = useRef<HTMLButtonElement>(null);
  const items = state.conversation?.items ?? [];
  const previousItems = useRef<ReadonlyMap<string, ConversationDisplayItem>>(
    new Map(),
  );
  let streamingMessage: string | null = null;
  for (const item of items) {
    const nextAnnouncement = streamingAnnouncement(
      previousItems.current.get(item.key) ?? null,
      item,
    );
    if (nextAnnouncement !== null) streamingMessage = nextAnnouncement;
  }
  useLayoutEffect(() => {
    previousItems.current = new Map(items.map((item) => [item.key, item]));
  }, [items]);
  const composer =
    state.phase === "loading" || state.conversation === null
      ? {
          ...state.composer,
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: false,
        }
      : state.composer;
  const openEvidence = state.conversation?.evidence.find(
    (evidence) => evidence.key === state.openEvidenceKey,
  );
  return (
    <main
      className={`live-conversation-frame ${skin.className}`}
      data-live-conversation-frame="true"
      aria-label="Conversation"
      onKeyDown={(event) => wrapTerminalComposerTab(event, backRef.current)}
    >
      <ConversationChrome
        skin={skin}
        state={state}
        dispatch={dispatch}
        backRef={backRef}
      />
      <VirtualTranscript data-frame-part="transcript">
        <div
          className="live-conversation-frame__feed"
          role="feed"
          aria-label="Conversation transcript"
        >
          {items.length === 0 && state.phase === "ready" ? (
            <p>No transcript yet</p>
          ) : null}
          {items.map((item, index) => (
            <TranscriptItem
              key={item.key}
              item={item}
              index={index}
              total={items.length}
              state={state}
              skin={skin}
              dispatch={dispatch}
              onFocusIntentChange={onFocusIntentChange}
            />
          ))}
        </div>
        {state.conversation?.olderAvailable ? (
          <button
            type="button"
            aria-label="Load older messages"
            disabled={state.composer.pending?.status === "pending"}
            onClick={() => dispatch({ type: "loadOlder" })}
          >
            Load older messages
          </button>
        ) : null}
        {openEvidence !== undefined ? (
          <section
            role="dialog"
            aria-modal="true"
            aria-label={openEvidence.title.text}
          >
            <h2>{openEvidence.title.text}</h2>
            {openEvidence.sections.map((section) => (
              <section key={`${section.heading.text}:${section.body.text}`}>
                <h3>{section.heading.text}</h3>
                <p>{section.body.text}</p>
              </section>
            ))}
            <button
              type="button"
              aria-label="Close evidence"
              onClick={() => dispatch({ type: "closeEvidence" })}
            >
              Close
            </button>
          </section>
        ) : null}
      </VirtualTranscript>
      <ConversationStatus
        state={state}
        dispatch={dispatch}
        streamingMessage={streamingMessage}
      />
      <section
        data-frame-part="composer"
        data-live-conversation-composer="true"
      >
        <DockedComposer
          composer={composer}
          mode={state.composerMode}
          appearance={skin.composerAppearance}
          dispatch={dispatch}
        />
      </section>
    </main>
  );
}
