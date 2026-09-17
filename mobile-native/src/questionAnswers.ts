import {
  type AskAnswerItem,
  type AskQuestionRef,
  composeAskAnswers,
} from "@evener/appwire-client";
import {
  boundQuestion,
  liveAsksFor,
  MAX_ITEM_BYTES,
  type MobileConversation,
  truncateText,
} from "../../mobile/src/conversation/project";
export type QuestionSelections = Record<
  string,
  Pick<AskAnswerItem, "resolution" | "note">
>;
export function pendingQuestions(
  conversation: MobileConversation | null,
): AskQuestionRef[] {
  // Asked of the MODEL, with the package's own rule — the same call the
  // projection's question rows come from (project.ts's askQuestionsByCall,
  // through liveAsksFor's shared scan). The refs are therefore canonical:
  // composeQuestionAnswers names the header, the chosen labels and the
  // ifUnanswered text exactly as the agent asked them, while the rows a
  // reader scrolls carry the display bound's cut copies.
  if (conversation === null) return [];
  return [...liveAsksFor(conversation).values()].flat();
}

// The sheet's own rendered text, bounded the same as a timeline row
// (mobile/src/conversation/project.ts). Shared by questionsIdentity below and
// QuestionSheet.tsx's own display copy, so the identity and what a reader
// actually sees are cut exactly the same way.
export const boundQuestionText = (text: string) =>
  truncateText(text, MAX_ITEM_BYTES);

// A question set's identity for everything that keys, signs or persists it —
// a React key over a batch, the sheet's draft signature, and the definitions
// that guard a persisted answer (draftRepository.ts's questionDefinitions).
// None of those need pendingQuestions()'s canonical, uncut refs: they only
// ever need to tell "the same questions" apart from "different questions",
// same as a reader could from the screen. Serializing the bounded copy
// (boundQuestion, the same cut QuestionSheet.tsx renders) keeps the identity
// itself bounded, so an oversized ask_user payload can no longer be
// re-serialized on every render or stored whole in a drafts row.
export function questionsIdentity(questions: AskQuestionRef[]): string {
  return JSON.stringify(
    questions.map((question) => boundQuestion(question, boundQuestionText)),
  );
}

export function composeQuestionAnswers(
  questions: AskQuestionRef[],
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
  questions: AskQuestionRef[],
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
  questions: AskQuestionRef[],
  answers: QuestionSelections,
  activeIndex: number,
): number | undefined {
  if (questions.length < 2) return undefined;
  if (activeIndex < questions.length - 1) return activeIndex + 1;
  return nextUnansweredQuestion(questions, answers, activeIndex);
}

export function nextUnansweredQuestion(
  questions: AskQuestionRef[],
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
