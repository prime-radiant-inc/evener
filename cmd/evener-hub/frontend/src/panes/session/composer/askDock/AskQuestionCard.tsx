// AskQuestionCard renders one ask_user question's interactive control set:
// recommended-first option rows (checkboxes when multi_select, else radios
// so native keyboard nav and AT semantics both work) whose labels are plain
// inline BOLD text - not pills - with an accent "· recommended" suffix on
// the recommended row (ask-dialog UX rework, replacing the old chip +
// far-right RECOMMENDED tag treatment), a "Something else…" alternative
// that turns the question's one text field into the free-text answer
// (instead of adding a second input), and that same field serving as the
// per-question note the rest of the time. The recommended option arrives
// pre-selected (askDockStore seeds it at reconcile time), so there is no
// "let evener decide" row and no skip/fallback pills: every question always
// has a real answer to edit. Every control funnels through the two callback
// props so exactly one resolution kind is ever active - the parent
// (AskDock) owns the actual answer state (askDockStore), this component is
// a pure controlled view over it, same convention as every other controlled
// widget in this codebase (Input, Switch).
//
// No RadioGroup/Checkbox widget exists in widgets/** for this wave to
// build on (verified: src/widgets/index.ts's barrel has no such export),
// and Input's fixed prop list has no aria-labelledby passthrough - both are
// hard requirements here (recommended-first radiogroup, aria-labelledby
// wiring), so this file uses plain semantic <input> elements with its own
// CSS module throughout rather than forcing a widget that cannot express
// them.
import { useEffect, useId, useRef } from "react";
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
  optionLabel: requireClass(styles.optionLabel, "askquestioncard.module.css", "optionLabel"),
  optionRecommended: requireClass(styles.optionRecommended, "askquestioncard.module.css", "optionRecommended"),
  optionDetail: requireClass(styles.optionDetail, "askquestioncard.module.css", "optionDetail"),
  alternativeRow: requireClass(styles.alternativeRow, "askquestioncard.module.css", "alternativeRow"),
  textInput: requireClass(styles.textInput, "askquestioncard.module.css", "textInput"),
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
  const noteLabelId = `${id}-note-label`;

  const noteInputRef = useRef<HTMLInputElement>(null);
  const prevKindRef = useRef<AskResolution["kind"] | null>(null);

  // Focuses the shared text field the moment Something else becomes active
  // - edge-triggered on the kind actually changing TO free (never on every
  // render, so typing in the already-active field never yanks focus back to
  // itself, and a resolution set by some other means - e.g. a fresh dock
  // rebuild restoring a prior answer - doesn't steal focus a user has
  // already moved on from).
  useEffect(() => {
    const kind = answer.resolution?.kind ?? null;
    const prev = prevKindRef.current;
    prevKindRef.current = kind;
    if (kind === prev) return;
    if (kind === "free") noteInputRef.current?.focus();
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
  function deactivate() {
    onResolutionChange(null);
  }

  const freeActive = resolution?.kind === "free";
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
          />{" "}
          {/* Inline bold label (ask-dialog UX rework), replacing the chip
              pill - the whole row is one continuous inline flow (see
              .option in the stylesheet), so label, marker, and detail set
              like prose and wrap at the row's left edge. The explicit
              spaces are load-bearing: JSX concatenates adjacent elements,
              and inline flow needs real whitespace between them. */}
          <span className={CLASS.optionLabel}>{opt.label}</span>
          {opt.recommended && <span className={CLASS.optionRecommended}> · recommended</span>}{" "}
          <span className={CLASS.optionDetail}>{opt.detail}</span>
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

      {/* The question's ONE text field (ask-dialog UX rework): the
          free-text answer while Something else is active (answer-mode
          placeholder, resolution-editing onChange, and data-ask-free-input
          so AskDock's batch-level keydown handler gives a bare Enter here
          the primary action), the per-question note the rest of the time.
          Always mounted, so activating/deactivating the alternative never
          remounts the element mid-edit. */}
      <div className={CLASS.noteRow}>
        <span className={CLASS.visuallyHidden} id={noteLabelId}>
          note
        </span>
        <input
          ref={noteInputRef}
          type="text"
          className={CLASS.textInput}
          placeholder={freeActive ? "type your answer" : "note (optional)"}
          aria-labelledby={freeActive ? `${freeLabelId} ${headerId} ${textId}` : `${headerId} ${textId} ${noteLabelId}`}
          data-ask-free-input={freeActive ? "true" : undefined}
          value={freeActive && resolution?.kind === "free" ? resolution.text : answer.note}
          onChange={(e) =>
            freeActive ? onResolutionChange({ kind: "free", text: e.target.value }) : onNoteChange(e.target.value)
          }
        />
      </div>
    </div>
  );
}
