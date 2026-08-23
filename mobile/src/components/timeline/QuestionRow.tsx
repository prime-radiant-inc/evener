import type { JSX } from "react";
import type { AskBatch } from "../../conversation/model";

export interface QuestionRowProps {
  readonly batch: AskBatch;
}

/**
 * Placeholder for pending ask_user question batches. Task 5 implements the full
 * interactive ask cards with selectable options and a "Send answers" action.
 * For now, the questions render as plain text so the timeline is complete and
 * no raw protocol JSON is exposed.
 */
export function QuestionRow({ batch }: QuestionRowProps): JSX.Element {
  return (
    <div className="evener-question-row" data-placeholder="true">
      {batch.questions.map((q) => (
        <div key={q.key} className="evener-question-row__item">
          <div className="evener-question-row__header">{q.header}</div>
          <div className="evener-question-row__text">{q.question}</div>
          <ul className="evener-question-row__options">
            {q.options.map((o) => (
              <li key={o.label} className="evener-question-row__option">
                <span className="evener-question-row__option-label">
                  {o.label}
                </span>
                <span className="evener-question-row__option-detail">
                  {o.detail}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}
