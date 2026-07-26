// AskQuestionCard renders one ask_user question's full interactive control
// set (parity-m5-composer.md §C / test-ask-card.js): recommended-first
// option chips (checkboxes when multi_select, else radios so native
// keyboard nav and AT semantics both work), a free-text row, a "let serf
// decide" row with an optional leaning, a fallback button when
// if_unanswered is present, a skip button, a dim `why` line, and a
// per-question note. Every control funnels through the two callback props
// so exactly one resolution kind is ever active - the parent (AskDock)
// owns the actual answer state (askDockStore), this component is a pure
// controlled view over it, same convention as every other controlled
// widget in this codebase (Input, Switch).
//
// No RadioGroup/Checkbox widget exists in widgets/** for this wave to
// build on (verified: src/widgets/index.ts's barrel has no such export),
// and Input's fixed prop list has no aria-labelledby passthrough, nor does
// Button accept a caller className (it computes its own) - all three are
// hard requirements here (recommended-first radiogroup, aria-labelledby
// wiring, a visibly distinct pressed state for the fallback/skip toggles),
// so this file uses plain semantic <input>/<button> elements with its own
// CSS module throughout rather than forcing a widget that cannot express
// them.
import { useEffect, useId, useRef } from "react";
import { Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { AskResolution } from "./askCompose";
import type { AskAnswerState } from "./askDockStore";
import styles from "./askquestioncard.module.css";
import type { AskQuestionRef } from "./deriveAskQuestions";

const CLASS = {
  card: requireClass(styles.card, "askquestioncard.module.css", "card"),
  head: requireClass(styles.head, "askquestioncard.module.css", "head"),
  num: requireClass(styles.num, "askquestioncard.module.css", "num"),
  header: requireClass(styles.header, "askquestioncard.module.css", "header"),
  questionText: requireClass(styles.questionText, "askquestioncard.module.css", "questionText"),
  why: requireClass(styles.why, "askquestioncard.module.css", "why"),
  options: requireClass(styles.options, "askquestioncard.module.css", "options"),
  option: requireClass(styles.option, "askquestioncard.module.css", "option"),
  optionChip: requireClass(styles.optionChip, "askquestioncard.module.css", "optionChip"),
  optionDetail: requireClass(styles.optionDetail, "askquestioncard.module.css", "optionDetail"),
  optionTag: requireClass(styles.optionTag, "askquestioncard.module.css", "optionTag"),
  alternativeRow: requireClass(styles.alternativeRow, "askquestioncard.module.css", "alternativeRow"),
  textInput: requireClass(styles.textInput, "askquestioncard.module.css", "textInput"),
  altRow: requireClass(styles.altRow, "askquestioncard.module.css", "altRow"),
  toggleButton: requireClass(styles.toggleButton, "askquestioncard.module.css", "toggleButton"),
  noteRow: requireClass(styles.noteRow, "askquestioncard.module.css", "noteRow"),
  visuallyHidden: requireClass(styles.visuallyHidden, "askquestioncard.module.css", "visuallyHidden"),
};

export interface AskQuestionCardProps {
  question: AskQuestionRef;
  // Global posting order (1-based) across the WHOLE pending batch this
  // question belongs to, not just its own ask_user call - matches the
  // [answers] reply's own numbering (askCompose.ts).
  number: number;
  answer: AskAnswerState;
  onResolutionChange(resolution: AskResolution | null): void;
  onNoteChange(note: string): void;
}

// Recommended goes first (stable sort keeps the model's given order among
// the rest) - a rendering-time concern only; the parsed question data
// itself (deriveAskQuestions/askShared) is never reordered, so the
// read-only transcript card (askUser.tsx) is unaffected.
function orderedOptions(options: AskQuestionRef["options"]) {
  return options
    .map((opt, index) => ({ opt, index }))
    .sort((a, b) => {
      const ar = a.opt.recommended ? 1 : 0;
      const br = b.opt.recommended ? 1 : 0;
      return br - ar || a.index - b.index;
    })
    .map(({ opt }) => opt);
}

export function AskQuestionCard({ question, number, answer, onResolutionChange, onNoteChange }: AskQuestionCardProps) {
  const id = useId();
  const headerId = `${id}-header`;
  const textId = `${id}-text`;
  const groupName = `${id}-group`;
  const freeLabelId = `${id}-free-label`;
  const decideLabelId = `${id}-decide-label`;
  const noteLabelId = `${id}-note-label`;

  const freeInputRef = useRef<HTMLInputElement>(null);
  const decideInputRef = useRef<HTMLInputElement>(null);
  const prevKindRef = useRef<AskResolution["kind"] | null>(null);

  // Focuses the alternative's own text input the moment it becomes active -
  // edge-triggered on the kind actually changing TO free/decide (never on
  // every render, so typing in an already-active input never yanks focus
  // back to itself, and a resolution set by some other means - e.g. a
  // fresh dock rebuild restoring a prior answer - doesn't steal focus a
  // user has already moved on from).
  useEffect(() => {
    const kind = answer.resolution?.kind ?? null;
    const prev = prevKindRef.current;
    prevKindRef.current = kind;
    if (kind === prev) return;
    if (kind === "free") freeInputRef.current?.focus();
    else if (kind === "decide") decideInputRef.current?.focus();
  }, [answer.resolution?.kind]);

  const resolution = answer.resolution;
  const checkedLabels = resolution?.kind === "option" ? new Set(resolution.labels) : new Set<string>();
  const multiSelect = question.multiSelect;

  function handleOptionChange(label: string, checked: boolean) {
    if (!multiSelect) {
      onResolutionChange({ kind: "option", labels: [label] });
      return;
    }
    const current = resolution?.kind === "option" ? resolution.labels : [];
    const next = checked ? [...current, label] : current.filter((l) => l !== label);
    onResolutionChange(next.length > 0 ? { kind: "option", labels: next } : null);
  }

  function activateFree() {
    onResolutionChange({ kind: "free", text: "" });
  }
  function activateDecide() {
    onResolutionChange({ kind: "decide", leaning: "" });
  }
  function deactivate() {
    onResolutionChange(null);
  }

  const freeActive = resolution?.kind === "free";
  const decideActive = resolution?.kind === "decide";
  const fallbackActive = resolution?.kind === "fallback";
  const skipActive = resolution?.kind === "skip";
  const optionsLabelledBy = `${headerId} ${textId}`;
  const radioName = multiSelect ? undefined : groupName;

  // Shared between the group/radiogroup wrapper branches below (identical
  // content either way - only the wrapping role differs, and that has to
  // be a static string per branch, not a ternary, for the accessibility
  // linter to verify aria-labelledby is valid for it).
  const optionsChildren = (
    <>
      {orderedOptions(question.options).map((opt) => (
        <label className={CLASS.option} key={opt.label}>
          <input
            type={multiSelect ? "checkbox" : "radio"}
            name={radioName}
            aria-label={opt.label}
            checked={
              multiSelect
                ? checkedLabels.has(opt.label)
                : resolution?.kind === "option" && resolution.labels[0] === opt.label
            }
            onChange={(e) => handleOptionChange(opt.label, e.target.checked)}
          />
          {/* Quick-reply chip (topic 16 Alt D), replacing plain label text -
              widgets/chip is already token-contract-allowlisted for its own
              tone prop, so this needs no allowlist change of its own; the
              checked ring beside it is --accent (askquestioncard.module.css). */}
          <span className={CLASS.optionChip}>
            <Chip tone="neutral">{opt.label}</Chip>
          </span>
          <span className={CLASS.optionDetail}>{opt.detail}</span>
          {opt.recommended && <span className={CLASS.optionTag}>recommended</span>}
        </label>
      ))}

      <div className={CLASS.alternativeRow}>
        <label>
          <input
            type="radio"
            name={radioName}
            aria-label="Something else…"
            checked={freeActive}
            onClick={(e) => {
              if (freeActive) {
                e.preventDefault();
                deactivate();
              }
            }}
            onChange={() => {
              if (!freeActive) activateFree();
            }}
          />
          <span id={freeLabelId}>Something else…</span>
        </label>
        {freeActive && (
          <input
            ref={freeInputRef}
            type="text"
            className={CLASS.textInput}
            placeholder="type your answer"
            aria-labelledby={`${freeLabelId} ${headerId} ${textId}`}
            value={resolution?.kind === "free" ? resolution.text : ""}
            onChange={(e) => onResolutionChange({ kind: "free", text: e.target.value })}
          />
        )}
      </div>

      <div className={CLASS.alternativeRow}>
        <label>
          <input
            type="radio"
            name={radioName}
            aria-label="let serf decide"
            checked={decideActive}
            onClick={(e) => {
              if (decideActive) {
                e.preventDefault();
                deactivate();
              }
            }}
            onChange={() => {
              if (!decideActive) activateDecide();
            }}
          />
          <span id={decideLabelId}>let serf decide</span>
        </label>
        {decideActive && (
          <input
            ref={decideInputRef}
            type="text"
            className={CLASS.textInput}
            placeholder="leaning (optional)"
            aria-labelledby={`${decideLabelId} ${headerId} ${textId}`}
            value={resolution?.kind === "decide" ? resolution.leaning : ""}
            onChange={(e) => onResolutionChange({ kind: "decide", leaning: e.target.value })}
          />
        )}
      </div>
    </>
  );

  return (
    <div className={CLASS.card} data-ask-question data-ask-key={question.key}>
      <div className={CLASS.head}>
        <span className={CLASS.num}>{number}.</span>
        <span className={CLASS.header} id={headerId}>
          {question.header}
        </span>
      </div>
      <div className={CLASS.questionText} id={textId}>
        {question.question}
      </div>
      {question.why && <div className={CLASS.why}>{question.why}</div>}

      {multiSelect ? (
        <fieldset className={CLASS.options} aria-labelledby={optionsLabelledBy}>
          {optionsChildren}
        </fieldset>
      ) : (
        <div className={CLASS.options} role="radiogroup" aria-labelledby={optionsLabelledBy}>
          {optionsChildren}
        </div>
      )}

      <div className={CLASS.altRow}>
        {question.ifUnanswered && (
          <button
            type="button"
            className={CLASS.toggleButton}
            aria-pressed={fallbackActive}
            onClick={() => onResolutionChange(fallbackActive ? null : { kind: "fallback" })}
          >
            do that: {question.ifUnanswered}
          </button>
        )}
        <button
          type="button"
          className={CLASS.toggleButton}
          aria-pressed={skipActive}
          onClick={() => onResolutionChange(skipActive ? null : { kind: "skip" })}
        >
          skip
        </button>
      </div>

      <div className={CLASS.noteRow}>
        <span className={CLASS.visuallyHidden} id={noteLabelId}>
          note
        </span>
        <input
          type="text"
          className={CLASS.textInput}
          placeholder="note (optional)"
          aria-labelledby={`${headerId} ${textId} ${noteLabelId}`}
          value={answer.note}
          onChange={(e) => onNoteChange(e.target.value)}
        />
      </div>
    </div>
  );
}
