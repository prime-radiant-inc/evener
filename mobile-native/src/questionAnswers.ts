import {
  type AskAnswerItem,
  composeAskAnswers,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/askDock/askCompose";
import type {
  MobileAskQuestion,
  MobileConversation,
} from "../../mobile/src/conversation/model";
export type QuestionSelections = Record<
  string,
  Pick<AskAnswerItem, "resolution" | "note">
>;
export function pendingQuestions(conversation: MobileConversation | null) {
  return conversation?.askPending
    ? conversation.items.flatMap((item) =>
        item.kind === "question"
          ? item.batch.questions.map((question) => ({
              ...question,
              callId: item.batch.callId,
            }))
          : [],
      )
    : [];
}
export function composeQuestionAnswers(
  questions: MobileAskQuestion[],
  selections: QuestionSelections,
): string | null {
  if (!questions.length) return null;
  for (const question of questions) {
    const resolution = selections[question.key]?.resolution;
    if (!resolution) {
      if (questions.length > 1) return null;
      continue;
    }
    if (
      resolution.kind === "option" &&
      (!resolution.labels.length ||
        (!question.multiSelect && resolution.labels.length !== 1) ||
        new Set(resolution.labels).size !== resolution.labels.length ||
        resolution.labels.some(
          (label) => !question.options.some((option) => option.label === label),
        ))
    )
      return null;
    if (resolution.kind === "fallback" && !question.ifUnanswered) return null;
  }
  return composeAskAnswers(
    questions.map((question) => ({
      ...(selections[question.key] ?? { resolution: null, note: "" }),
      header: question.header,
      ifUnanswered: question.ifUnanswered,
    })),
  );
}

/** Stored input is validated before it becomes interactive state. */
export function decodeQuestionSelections(json: string): QuestionSelections {
  const value: unknown = JSON.parse(json);
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Invalid question draft");
  for (const item of Object.values(value)) {
    if (!item || typeof item !== "object" || typeof item.note !== "string")
      throw new Error("Invalid question draft");
    const r = item.resolution;
    if (r === null) continue;
    if (
      !r ||
      typeof r !== "object" ||
      !(
        (r.kind === "option" &&
          Array.isArray(r.labels) &&
          r.labels.every((label: unknown) => typeof label === "string")) ||
        (r.kind === "free" && typeof r.text === "string") ||
        (r.kind === "decide" && typeof r.leaning === "string") ||
        r.kind === "skip" ||
        r.kind === "fallback"
      )
    )
      throw new Error("Invalid question draft");
  }
  return value as QuestionSelections;
}

/** Match the web dock: seed only untouched answers, never a cleared choice. */
export function seedQuestionAnswers(
  questions: MobileAskQuestion[],
  saved: QuestionSelections,
): QuestionSelections {
  const answers = { ...saved };
  for (const question of questions) {
    if (answers[question.key]) continue;
    const labels = question.options
      .filter((option) => option.recommended)
      .map((option) => option.label);
    if (labels.length)
      answers[question.key] = {
        resolution: { kind: "option", labels },
        note: "",
      };
  }
  return answers;
}

/** The web dock walks forward, then wraps to unanswered questions. */
export function questionAdvanceTarget(
  questions: MobileAskQuestion[],
  answers: QuestionSelections,
  activeIndex: number,
): number | undefined {
  if (questions.length < 2) return undefined;
  if (activeIndex < questions.length - 1) return activeIndex + 1;
  return nextUnansweredQuestion(questions, answers, activeIndex);
}

export function nextUnansweredQuestion(
  questions: MobileAskQuestion[],
  answers: QuestionSelections,
  activeIndex: number,
): number | undefined {
  for (let step = 1; step < questions.length; step++) {
    const index = (activeIndex + step) % questions.length;
    const question = questions[index];
    if (question && !answers[question.key]?.resolution) return index;
  }
  return undefined;
}
