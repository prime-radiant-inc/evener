// ask_user descriptor (parity checklist §2's askUserRenderer + §10's
// interception note - this rewrite renders it as a normal tool-call row,
// unlike legacy's suppress-and-redirect-to-a-dock design, since the
// composer-side answer flow is explicitly out of scope this wave). Ground
// truth: agent/internal/tool/definitions.go's DefAskUser gives the exact
// argumentsJson shape - {questions:[{header(<=12 chars), question,
// options:[{label,detail,recommended?}], multi_select?, why?,
// if_unanswered?}]}, 1-4 questions. ask_user's own Output is a single
// FIXED string on success (agent/session_tools_ask.go's askUserAckText,
// verified directly) - carries no per-call information at all, so this
// descriptor reads entirely from argumentsJSON directly - the model
// preserves it through settle like every other tool call (protocol/
// reducer.ts).
//
// This wave is read-only: no answer affordance here at all (the composer
// owns that in Wave 5) - every card just says so. Parsing is defensive
// throughout: a malformed argumentsJSON, a missing `questions` array, or
// an individual malformed question all degrade to a fallback rather than
// throwing, since this is untrusted wire JSON, not a value this file
// controls the shape of.

import type { ItemModel } from "../../../../protocol/model";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import styles from "./askuser.module.css";
import { parseArgs } from "./helpers";

interface AskUserOption {
  label: string;
  detail: string;
  recommended?: boolean;
}

interface AskUserQuestion {
  header: string;
  question: string;
  options: AskUserOption[];
  multiSelect?: boolean;
  why?: string;
  ifUnanswered?: string;
}

const CLASS = {
  card: requireClass(styles.card, "askuser.module.css", "card"),
  question: requireClass(styles.question, "askuser.module.css", "question"),
  options: requireClass(styles.options, "askuser.module.css", "options"),
  option: requireClass(styles.option, "askuser.module.css", "option"),
  label: requireClass(styles.label, "askuser.module.css", "label"),
  detail: requireClass(styles.detail, "askuser.module.css", "detail"),
  recommended: requireClass(styles.recommended, "askuser.module.css", "recommended"),
  note: requireClass(styles.note, "askuser.module.css", "note"),
  footer: requireClass(styles.footer, "askuser.module.css", "footer"),
  fallback: requireClass(styles.fallback, "askuser.module.css", "fallback"),
};

function parseOption(raw: unknown): AskUserOption | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (typeof obj["label"] !== "string" || typeof obj["detail"] !== "string") return undefined;
  return { label: obj["label"], detail: obj["detail"], recommended: obj["recommended"] === true };
}

function parseQuestion(raw: unknown): AskUserQuestion | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (typeof obj["header"] !== "string" || typeof obj["question"] !== "string" || !Array.isArray(obj["options"])) {
    return undefined;
  }
  const options = obj["options"].map(parseOption).filter((o): o is AskUserOption => o !== undefined);
  if (options.length === 0) return undefined;
  return {
    header: obj["header"],
    question: obj["question"],
    options,
    multiSelect: obj["multi_select"] === true,
    why: typeof obj["why"] === "string" ? obj["why"] : undefined,
    ifUnanswered: typeof obj["if_unanswered"] === "string" ? obj["if_unanswered"] : undefined,
  };
}

// parseAskUserQuestions returns undefined whenever there's nothing usable to
// render - argumentsJSON absent entirely, a JSON syntax error, valid JSON
// with no questions array, or every individual question in that array
// failing its own shape check - without distinguishing WHY here. An empty
// array can't legitimately occur (DefAskUser requires minItems:1) but is
// treated the same as undefined defensively either way. AskUserBody draws
// its own absent-vs-malformed distinction for its fallback wording; see
// isMalformedArgumentsJSON below.
function parseAskUserQuestions(item: ItemModel): AskUserQuestion[] | undefined {
  const args = parseArgs(item.argumentsJSON);
  const raw = args["questions"];
  if (!Array.isArray(raw)) return undefined;
  const questions = raw.map(parseQuestion).filter((q): q is AskUserQuestion => q !== undefined);
  return questions.length > 0 ? questions : undefined;
}

// isMalformedArgumentsJSON is true only for a genuine JSON syntax error in
// THIS item's own argumentsJSON - the one case AskUserBody's fallback below
// still calls "malformed". Every other reason parseAskUserQuestions can
// fail (argumentsJSON absent entirely - now only a genuinely argless item,
// since the model preserves real argumentsJSON through both settle and
// hydration - or syntactically valid JSON that simply carries no usable
// questions) is honest absence, not corruption, and gets its own wording
// instead of being misdescribed as malformed.
function isMalformedArgumentsJSON(argumentsJSON: string | undefined): boolean {
  if (argumentsJSON === undefined) return false;
  try {
    JSON.parse(argumentsJSON);
    return false;
  } catch {
    return true;
  }
}

function QuestionCard({ q }: { q: AskUserQuestion }) {
  return (
    <div className={CLASS.card}>
      <div className={CLASS.question}>{q.question}</div>
      <ul className={CLASS.options}>
        {q.options.map((opt) => (
          <li key={opt.label} className={CLASS.option}>
            <span className={CLASS.label}>{opt.label}</span>
            <span className={CLASS.detail}>{opt.detail}</span>
            {opt.recommended && <span className={CLASS.recommended}>recommended</span>}
          </li>
        ))}
      </ul>
      {q.multiSelect && <div className={CLASS.note}>Select multiple.</div>}
      {q.why && <div className={CLASS.note}>{q.why}</div>}
      {q.ifUnanswered && <div className={CLASS.note}>If unanswered: {q.ifUnanswered}</div>}
    </div>
  );
}

function AskUserBody({ item }: ToolRenderProps) {
  const questions = parseAskUserQuestions(item);
  if (!questions) {
    // Absence (no argumentsJSON at all, or valid JSON with nothing usable
    // in it) is the common case - e.g. a genuinely argless item - and must
    // not be described as malformed; a genuine JSON syntax error keeps its
    // own, distinct wording.
    return (
      <div className={CLASS.fallback}>
        {isMalformedArgumentsJSON(item.argumentsJSON)
          ? "Couldn't read this question - the data looks malformed."
          : "Question data unavailable."}
      </div>
    );
  }
  return (
    <div>
      {questions.map((q, i) => (
        <QuestionCard key={i} q={q} />
      ))}
      <div className={CLASS.footer}>Answer in the composer (wave 5).</div>
    </div>
  );
}

registerToolRenderer({
  match: "ask_user",
  summary(item: ItemModel) {
    const questions = parseAskUserQuestions(item);
    if (!questions) return "Asked a question";
    return `Asked: ${questions.map((q) => `[${q.header}]`).join(", ")}`;
  },
  body: AskUserBody,
});
