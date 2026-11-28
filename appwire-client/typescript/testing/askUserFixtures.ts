// Shared ask_user tool-call wire fixtures (ground truth:
// agent/internal/tool/definitions.go's DefAskUser:
// {questions: [{header?, question, options: [{label, detail, recommended?}],
// multi_select?, why?, if_unanswered?}]}, 1-4 questions).

export type AskUserWireOption = {
  label: string;
  detail: string;
  recommended?: boolean;
};

export type AskUserWireQuestion = {
  header?: string;
  question: string;
  options: AskUserWireOption[];
  multi_select?: boolean;
  why?: string;
  if_unanswered?: string;
};

/** Single-question ask_user wire batch with full two-option coverage and a
 * recommended default. */
export const ONE_ASK_QUESTION: readonly [AskUserWireQuestion] = [
  {
    header: "Deploy?",
    question: "Should I deploy this to production now?",
    options: [
      { label: "Yes", detail: "Ship it", recommended: true },
      { label: "No", detail: "Wait" },
    ],
  },
];

/** Builds a single ask_user wire question from ONE_ASK_QUESTION[0]. Pass
 * `recommended: false` to omit option recommendation markers. */
export function askUserQuestion(
  overrides: Partial<AskUserWireQuestion> & { recommended?: boolean } = {},
): AskUserWireQuestion {
  const { recommended, options, ...rest } = overrides;
  const base = ONE_ASK_QUESTION[0];
  const resolvedOptions = options ?? base.options;
  return {
    ...base,
    ...rest,
    options: recommended === false ? resolvedOptions.map(({ label, detail }) => ({ label, detail })) : resolvedOptions,
  };
}

/** Encodes an ask_user tool call's argumentsJSON / argumentsJson wire string. */
export function askUserArgs(questions: readonly unknown[]): string {
  return JSON.stringify({ questions });
}
