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
// The barrel's RadioGroup has no per-option detail line, no recommended
// marker, no checkbox (multi_select) mode, no deselectable trailing option,
// and requires a visible legend where this card labels by aria-labelledby;
// there is no Checkbox widget; and Input's fixed prop list has no
// aria-labelledby passthrough. All of those are hard requirements here, so
// this file uses plain semantic <input> elements with its own CSS module
// throughout rather than forcing a widget that cannot express them.

import type { AskQuestionRef, AskResolution } from "@evener/appwire-client";
import { useEffect, useId, useRef } from "react";
import { VisuallyHidden } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { AskAnswerState } from "./askDockStore";
import styles from "./askquestioncard.module.css";

const CLASS = {
  question: requireClass(styles.question, "askquestioncard.module.css", "question"),
  head: requireClass(styles.head, "askquestioncard.module.css", "head"),
  num: requireClass(styles.num, "askquestioncard.module.css", "num"),
  questionHeader: requireClass(styles.questionHeader, "askquestioncard.module.css", "questionHeader"),
  questionText: requireClass(styles.questionText, "askquestioncard.module.css", "questionText"),
  why: requireClass(styles.why, "askquestioncard.module.css", "why"),
  optionGroup: requireClass(styles.optionGroup, "askquestioncard.module.css", "optionGroup"),
  optionRow: requireClass(styles.optionRow, "askquestioncard.module.css", "optionRow"),
  optionName: requireClass(styles.optionName, "askquestioncard.module.css", "optionName"),
  optionRecommended: requireClass(styles.optionRecommended, "askquestioncard.module.css", "optionRecommended"),
  optionDetail: requireClass(styles.optionDetail, "askquestioncard.module.css", "optionDetail"),
  alternativeRow: requireClass(styles.alternativeRow, "askquestioncard.module.css", "alternativeRow"),
  textInput: requireClass(styles.textInput, "askquestioncard.module.css", "textInput"),
  noteRow: requireClass(styles.noteRow, "askquestioncard.module.css", "noteRow"),
};

export interface AskQuestionCardProps {
  question: AskQuestionRef;
  // Global posting order (1-based) across the WHOLE pending batch this
  // question belongs to, not just its own ask_user call - matches the
  // [answers] reply's own numbering (protocol/askAnswers.ts).
  number: number;
  answer: AskAnswerState;
  // True while the batch's send is in flight (AskBatch.sending): the
  // successful send removes the batch, so an edit made mid-flight would be
  // silently discarded - every editing control disables until the send
  // settles (roborev PR #884 round 10). askDockStore's setAnswer/setNote
  // refuse the same writes at the store boundary.
  disabled: boolean;
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

export function AskQuestionCard({
  question,
  number,
  answer,
  disabled,
  onResolutionChange,
  onNoteChange,
}: AskQuestionCardProps) {
  const id = useId();
  const headerId = `${id}-header`;
  const textId = `${id}-text`;
  const groupName = `${id}-group`;
  const freeLabelId = `${id}-free-label`;
  const noteLabelId = `${id}-note-label`;

  const noteInputRef = useRef<HTMLInputElement>(null);
  // Initialized with the MOUNT-time kind, not null: the dock is a
  // virtualized transcript row, so scrolling away unmounts this card and
  // scrolling back remounts it. A remount with an already-active free
  // answer is not the transition this effect exists for - starting at null
  // would read it as one and steal focus to the answer input (roborev PR
  // #854). Mount-time focus belongs to AskDock's activation effect.
  const prevKindRef = useRef<AskResolution["kind"] | null>(answer.resolution?.kind ?? null);

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
        <label className={CLASS.optionRow} key={opt.label}>
          <input
            type={multiSelect ? "checkbox" : "radio"}
            name={radioName}
            aria-label={opt.label}
            disabled={disabled}
            checked={
              multiSelect
                ? checkedLabels.has(opt.label)
                : resolution?.kind === "option" && resolution.labels[0] === opt.label
            }
            onChange={(e) => handleOptionChange(opt.label, e.target.checked)}
          />{" "}
          {/* Inline bold label (ask-dialog UX rework), replacing the chip
              pill - the whole row is one continuous inline flow (see
              .optionRow in the stylesheet), so label, marker, and detail set
              like prose and wrap at the row's left edge. The explicit
              spaces are load-bearing: JSX concatenates adjacent elements,
              and inline flow needs real whitespace between them. */}
          <span className={CLASS.optionName}>{opt.label}</span>
          {opt.recommended && (
            <span className={CLASS.optionRecommended} data-slot="recommended">
              {" · recommended"}
            </span>
          )}{" "}
          <span className={CLASS.optionDetail}>{opt.detail}</span>
        </label>
      ))}

      <div className={CLASS.alternativeRow}>
        <label>
          <input
            type="radio"
            name={radioName}
            aria-label="Something else…"
            disabled={disabled}
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
    <div className={CLASS.question} data-ask-question data-ask-key={question.key}>
      <div className={CLASS.head}>
        <span className={CLASS.num}>{number}.</span>
        <span className={CLASS.questionHeader} id={headerId}>
          {question.header}
        </span>
      </div>
      <div className={CLASS.questionText} id={textId}>
        {question.question}
      </div>
      {question.why && <div className={CLASS.why}>{question.why}</div>}

      {multiSelect ? (
        <fieldset className={CLASS.optionGroup} aria-labelledby={optionsLabelledBy}>
          {optionsChildren}
        </fieldset>
      ) : (
        <div className={CLASS.optionGroup} role="radiogroup" aria-labelledby={optionsLabelledBy}>
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
        <VisuallyHidden>
          <span id={noteLabelId}>note</span>
        </VisuallyHidden>
        <input
          ref={noteInputRef}
          type="text"
          className={CLASS.textInput}
          disabled={disabled}
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
