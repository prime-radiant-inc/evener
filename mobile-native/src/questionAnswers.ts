import {
  type AskAnswerItem,
  composeAskAnswers,
} from "../../mobile/src/components/composer/composeAskAnswers";
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
        item.kind === "question" ? item.batch.questions : [],
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
    if (!resolution) return null;
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
    if (resolution.kind === "free" && !resolution.text.trim()) return null;
    if (resolution.kind === "fallback" && !question.ifUnanswered) return null;
  }
  return composeAskAnswers(
    questions.map((question) => ({
      ...selections[question.key],
      header: question.header,
      ifUnanswered: question.ifUnanswered,
    })),
  );
}
