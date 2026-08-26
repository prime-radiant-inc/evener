// AskComposer — structured ask_user question cards that replace the normal
// Composer during askPending. Renders interactive cards from conversation
// items where kind === "question", each showing header, question text,
// selectable options (single or multi), free-text, decide, fallback, and
// skip actions, plus an optional note field. A single "Send answers" action
// composes the [answers] text (byte-exact, matching the daemon's reply
// parser) and submits via conversationStore.send.
//
// Ask mode unmounts normal inputs so hidden controls cannot focus or submit.
// No retry on conflict: the composed answers text is restored as the draft
// and the error is surfaced. Settlement from another client (askPending
// becomes false) stops rendering the cards.
//
// The [answers] composition is ported verbatim from the Hub's
// askCompose.ts (renderer.js:6980-7031) — every character is load-bearing
// because the text round-trips through the daemon's reply parser.

import { type JSX, useCallback, useMemo, useState } from "react";
import type { StoreApi, UseBoundStore } from "zustand";
import type { InputItem } from "../../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  AskBatch,
  MobileAskQuestion,
  MobileTimelineItem,
} from "../../conversation/model";
import type { ConversationService } from "../../services/conversation";
import type { AttachmentState } from "../../state/attachments";
import type { ConversationState } from "../../state/conversation";
import type { AskAnswerItem, AskResolution } from "./composeAskAnswers";
import { composeAskAnswers } from "./composeAskAnswers";
import "./Composer.css";

export interface AskComposerProps {
  readonly conversationStore: UseBoundStore<StoreApi<ConversationState>>;
  readonly conversationService: ConversationService;
  readonly attachmentStore: UseBoundStore<StoreApi<AttachmentState>>;
}

// --- [answers] composition is shared via ./composeAskAnswers ---------------
// The byte-exact [answers] composition (ported from Hub askCompose.ts) now
// lives in composeAskAnswers.ts so the canonical AskComposer and the Plan 2
// live intent dispatcher share one implementation.

// --- per-question UI state -------------------------------------------------

// One question's editable state: resolution + note. Keyed by question.key
// so re-renders are stable across batch updates.
interface QuestionState {
  resolution: AskResolution | null;
  note: string;
  freeText: string;
  decideLeaning: string;
}

function initialQuestionState(): QuestionState {
  return {
    resolution: null,
    note: "",
    freeText: "",
    decideLeaning: "",
  };
}

// --- collect all pending question batches from the conversation ------------

function collectQuestions(
  items: MobileTimelineItem[],
): { key: string; question: MobileAskQuestion; batch: AskBatch }[] {
  const out: { key: string; question: MobileAskQuestion; batch: AskBatch }[] =
    [];
  for (const item of items) {
    if (item.kind !== "question") continue;
    for (const q of item.batch.questions) {
      out.push({ key: q.key, question: q, batch: item.batch });
    }
  }
  return out;
}

// --- component --------------------------------------------------------------

export function AskComposer({
  conversationStore,
  conversationService,
  attachmentStore,
}: AskComposerProps): JSX.Element {
  void attachmentStore; // AskComposer does not use attachments; normal inputs are unmounted.

  const conversation = conversationStore((s) => s.conversation);
  const status = conversationStore((s) => s.status);
  const [stateByKey, setStateByKey] = useState<Record<string, QuestionState>>(
    {},
  );
  const [inFlight, setInFlight] = useState(false);

  const questions = useMemo(() => {
    if (conversation === null) return [];
    return collectQuestions(conversation.items);
  }, [conversation]);

  const askPending = conversation?.askPending ?? false;
  const canSend = conversation?.capabilities.send ?? false;
  const connected = status === "open";

  const getQState = useCallback(
    (key: string): QuestionState => stateByKey[key] ?? initialQuestionState(),
    [stateByKey],
  );

  const setQState = useCallback(
    (key: string, update: Partial<QuestionState>) => {
      setStateByKey((prev) => ({
        ...prev,
        [key]: { ...initialQuestionState(), ...prev[key], ...update },
      }));
    },
    [],
  );

  // --- option selection (single) ------------------------------------------
  const selectOption = useCallback(
    (key: string, label: string, multiSelect: boolean) => {
      const cur = getQState(key);
      if (multiSelect) {
        const labels =
          cur.resolution?.kind === "option" ? [...cur.resolution.labels] : [];
        const idx = labels.indexOf(label);
        if (idx >= 0) labels.splice(idx, 1);
        else labels.push(label);
        setQState(key, {
          resolution: labels.length > 0 ? { kind: "option", labels } : null,
        });
      } else {
        setQState(key, { resolution: { kind: "option", labels: [label] } });
      }
    },
    [getQState, setQState],
  );

  // --- decide / fallback / skip / free -------------------------------------
  const setDecide = useCallback(
    (key: string) =>
      setQState(key, {
        resolution: { kind: "decide", leaning: getQState(key).decideLeaning },
      }),
    [getQState, setQState],
  );

  const setFallback = useCallback(
    (key: string) => setQState(key, { resolution: { kind: "fallback" } }),
    [setQState],
  );

  const setSkip = useCallback(
    (key: string) => setQState(key, { resolution: { kind: "skip" } }),
    [setQState],
  );

  const setFreeText = useCallback(
    (key: string, text: string) => {
      setQState(key, {
        freeText: text,
        resolution: text.trim().length > 0 ? { kind: "free", text } : null,
      });
    },
    [setQState],
  );

  const setDecideLeaning = useCallback(
    (key: string, leaning: string) => {
      const cur = getQState(key);
      setQState(key, {
        decideLeaning: leaning,
        resolution:
          cur.resolution?.kind === "decide"
            ? { kind: "decide", leaning }
            : cur.resolution,
      });
    },
    [getQState, setQState],
  );

  const setNote = useCallback(
    (key: string, note: string) => setQState(key, { note }),
    [setQState],
  );

  // --- compose + send ------------------------------------------------------
  const composedText = useMemo(() => {
    const items: AskAnswerItem[] = questions.map(({ key, question }) => {
      const qs = getQState(key);
      return {
        header: question.header,
        resolution: qs.resolution,
        note: qs.note,
        ifUnanswered: question.ifUnanswered,
      };
    });
    return composeAskAnswers(items);
  }, [questions, getQState]);

  const handleSend = useCallback(() => {
    if (inFlight) return;
    if (!canSend || !connected) return;
    setInFlight(true);
    const text = composedText;
    const input: InputItem[] = [{ type: "text", text }];
    const store = conversationStore.getState();
    void store
      .send(conversationService, input)
      .catch(() => {
        // On conflict, restore the composed answers as the draft and surface
        // the error. No retry.
        conversationStore.setState({
          draft: text,
          error: "Failed to send answers",
        });
      })
      .finally(() => {
        setInFlight(false);
      });
  }, [
    inFlight,
    canSend,
    connected,
    composedText,
    conversationStore,
    conversationService,
  ]);

  // --- render --------------------------------------------------------------
  if (!askPending || conversation === null) {
    return <div className="evener-ask-composer" data-testid="ask-composer" />;
  }

  const sendDisabled = !canSend || !connected || inFlight;

  return (
    <div className="evener-ask-composer" data-testid="ask-composer">
      <div className="evener-ask-composer__dock">
        {questions.map(({ key, question }) => {
          const qs = getQState(key);
          const hasOptions = question.options.length > 0;
          return (
            <div
              key={key}
              className="evener-ask-card"
              data-testid={`ask-card-${key}`}
            >
              <div className="evener-ask-card__header">{question.header}</div>
              <div className="evener-ask-card__question">
                {question.question}
              </div>

              {question.why !== undefined && (
                <div className="evener-ask-card__why">{question.why}</div>
              )}
              {question.ifUnanswered !== undefined && (
                <div className="evener-ask-card__if-unanswered">
                  If unanswered: {question.ifUnanswered}
                </div>
              )}

              {hasOptions && (
                <ul className="evener-ask-card__options">
                  {question.options.map((opt) => {
                    const selected =
                      qs.resolution?.kind === "option" &&
                      qs.resolution.labels.includes(opt.label);
                    return (
                      <li key={opt.label}>
                        <button
                          type="button"
                          className="evener-ask-card__option"
                          data-selected={selected}
                          data-testid={`ask-option-${key}-${opt.label}`}
                          onClick={() =>
                            selectOption(key, opt.label, question.multiSelect)
                          }
                        >
                          <span className="evener-ask-card__option-label">
                            {opt.label}
                          </span>
                          <span className="evener-ask-card__option-detail">
                            {opt.detail}
                          </span>
                          {opt.recommended && (
                            <span className="evener-ask-card__option-rec">
                              recommended
                            </span>
                          )}
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}

              {!hasOptions && (
                <textarea
                  className="evener-ask-card__free-text"
                  data-testid={`ask-free-text-${key}`}
                  placeholder="Type your answer…"
                  value={qs.freeText}
                  onChange={(e) => setFreeText(key, e.target.value)}
                  disabled={!connected}
                />
              )}

              <div className="evener-ask-card__actions">
                {hasOptions && (
                  <button
                    type="button"
                    className="evener-ask-card__action"
                    data-testid={`ask-decide-${key}`}
                    onClick={() => setDecide(key)}
                    disabled={!connected}
                  >
                    You decide
                  </button>
                )}
                {question.ifUnanswered !== undefined && (
                  <button
                    type="button"
                    className="evener-ask-card__action"
                    data-testid={`ask-fallback-${key}`}
                    onClick={() => setFallback(key)}
                    disabled={!connected}
                  >
                    Use fallback
                  </button>
                )}
                <button
                  type="button"
                  className="evener-ask-card__action"
                  data-testid={`ask-skip-${key}`}
                  onClick={() => setSkip(key)}
                  disabled={!connected}
                >
                  Skip
                </button>
              </div>

              {qs.resolution?.kind === "decide" && (
                <input
                  type="text"
                  className="evener-ask-card__leaning"
                  data-testid={`ask-decide-leaning-${key}`}
                  placeholder="Optional leaning…"
                  value={qs.decideLeaning}
                  onChange={(e) => setDecideLeaning(key, e.target.value)}
                  disabled={!connected}
                />
              )}

              <textarea
                className="evener-ask-card__note"
                data-testid={`ask-note-${key}`}
                placeholder="Optional note…"
                value={qs.note}
                onChange={(e) => setNote(key, e.target.value)}
                disabled={!connected}
              />
            </div>
          );
        })}

        <button
          type="button"
          className="evener-ask-composer__send"
          data-testid="ask-send-answers"
          disabled={sendDisabled}
          onClick={handleSend}
        >
          Send answers
        </button>
      </div>
    </div>
  );
}
