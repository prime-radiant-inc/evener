import type { QuestionFixture } from "../../core/model";
import {
  selectCanMutate,
  selectQuestionSubmitValidity,
} from "../../core/selectors";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { StatusLabel } from "../shared/StatusLabel";

export interface QuestionCardProps {
  question: QuestionFixture;
  state: PrototypeState;
  dispatch(action: PrototypeAction): void;
}

export function QuestionCard({ question, state, dispatch }: QuestionCardProps) {
  const answer = state.answers[question.id];
  const selectedOptionIds = answer?.selectedOptionIds ?? [];
  const submitted = answer?.submitted ?? false;
  const canMutate = selectCanMutate(state);
  const answerValid = selectQuestionSubmitValidity(
    state,
    question.id,
    "answer",
  );

  if (submitted) {
    return (
      <section
        className="fn-question-card fn-question-card--resolved"
        data-question-id={question.id}
        data-question-resolution={answer?.resolution ?? undefined}
        role="status"
      >
        <StatusLabel state="complete" />
        <h3>{question.prompt}</h3>
        <p>
          {answer?.resolution === "answer"
            ? "Answer submitted"
            : answer?.resolution === "fallback"
              ? "Fallback selected"
              : answer?.resolution === "decide"
                ? "Decision delegated"
                : "Question skipped"}
        </p>
      </section>
    );
  }

  return (
    <form
      className="fn-question-card fn-resolve-enter"
      data-question-id={question.id}
      onSubmit={(event) => {
        event.preventDefault();
        dispatch({
          type: "resolveQuestion",
          questionId: question.id,
          resolution: "answer",
        });
      }}
    >
      {!canMutate ? (
        <p className="fn-offline-guard" data-offline-guard="question">
          This saved question is read-only and cannot be resolved offline.
        </p>
      ) : null}
      <fieldset>
        <legend>
          <span className="fn-eyebrow">
            {question.mode === "single" ? "Choose one" : "Choose one or more"}
          </span>
          <span>{question.prompt}</span>
        </legend>
        <div className="fn-question-options">
          {question.options.map((option) => (
            <label className="fn-question-option" key={option.id}>
              <input
                type={question.mode === "single" ? "radio" : "checkbox"}
                name={`question-${question.id}`}
                aria-label={option.label}
                checked={selectedOptionIds.includes(option.id)}
                disabled={!canMutate}
                onChange={(event) =>
                  dispatch({
                    type: "setQuestionOption",
                    questionId: question.id,
                    optionId: option.id,
                    selected: event.currentTarget.checked,
                  })
                }
              />
              <span>
                <strong>{option.label}</strong>
                <small>{option.detail}</small>
              </span>
              {option.recommended ? <em>Recommended</em> : null}
            </label>
          ))}
        </div>
      </fieldset>

      {question.allowNote ? (
        <label className="fn-field">
          <span>Note</span>
          <textarea
            aria-label="Note"
            value={answer?.note ?? ""}
            disabled={!canMutate}
            onChange={(event) =>
              dispatch({
                type: "setQuestionNote",
                questionId: question.id,
                note: event.currentTarget.value,
              })
            }
          />
        </label>
      ) : null}

      <div className="fn-question-actions">
        <button
          className="fn-primary-action"
          type="submit"
          disabled={!answerValid || !canMutate}
        >
          Submit answer
        </button>
        {question.allowFallback ? (
          <button
            type="button"
            data-question-resolution="fallback"
            disabled={!canMutate}
            onClick={() =>
              dispatch({
                type: "resolveQuestion",
                questionId: question.id,
                resolution: "fallback",
              })
            }
          >
            Use fallback
          </button>
        ) : null}
        {question.allowDecide ? (
          <button
            type="button"
            data-question-resolution="decide"
            disabled={!canMutate}
            onClick={() =>
              dispatch({
                type: "resolveQuestion",
                questionId: question.id,
                resolution: "decide",
              })
            }
          >
            You decide
          </button>
        ) : null}
        {question.allowSkip ? (
          <button
            type="button"
            data-question-resolution="skip"
            disabled={!canMutate}
            onClick={() =>
              dispatch({
                type: "resolveQuestion",
                questionId: question.id,
                resolution: "skip",
              })
            }
          >
            Skip
          </button>
        ) : null}
      </div>
    </form>
  );
}
