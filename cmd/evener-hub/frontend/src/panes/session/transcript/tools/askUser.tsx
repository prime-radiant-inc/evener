// ask_user descriptor (parity checklist §2's askUserRenderer + §10's
// interception note - this rewrite renders it as a normal tool-call row,
// unlike legacy's suppress-and-redirect-to-a-dock design, since the
// composer-side answer flow is a separate wave-5 surface). Ground truth:
// agent/internal/tool/definitions.go's DefAskUser gives the exact
// argumentsJson shape - {questions:[{header?, question,
// options:[{label,detail,recommended?}], multi_select?, why?,
// if_unanswered?}]}, 1-4 questions. ask_user's own Output is a single
// FIXED string on success (agent/session_tools_ask.go's askUserAckText,
// verified directly) - carries no per-call information at all, so this
// descriptor reads entirely from argumentsJSON directly - the model
// preserves it through settle like every other tool call (protocol/
// reducer.ts).
//
// This transcript card stays read-only: no answer affordance here (the
// composer's askDock owns that - panes/session/composer/askDock/**). The
// question/option shape check itself (parseAskUserQuestions and its
// AskUserOption/AskUserQuestion types) moved to ../../askShared so askDock
// can reuse the identical parsing without duplicating it or reaching into
// this directory; this file keeps only what's specific to the read-only
// card (the malformed-vs-absent fallback wording and the static markup).

import type { ItemModel } from "../../../../protocol/model";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type AskUserQuestion, answeredAskUserSuffix, parseAskUserQuestions } from "../../askShared";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import styles from "./askuser.module.css";

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
        // questions has no id field of its own (see AskUserQuestion above)
        // and is re-parsed fresh from one tool call's fixed argumentsJSON
        // every render - always the same 1-4 entries in the same order.
        // biome-ignore lint/suspicious/noArrayIndexKey: derived fresh from a fixed, immutable source each render, see above
        <QuestionCard key={i} q={q} />
      ))}
      <div className={CLASS.footer}>Answer in the composer (wave 5).</div>
    </div>
  );
}

registerToolRenderer({
  match: "ask_user",
  fold: "never", // a question put to the reader is never folded away
  icon: "ask",
  summary(item: ItemModel) {
    const questions = parseAskUserQuestions(item);
    if (!questions) return "Asked a question";
    return `Asked: ${questions.map((q) => `[${q.header}]`).join(", ")}`;
  },
  // A settled thing should compress to its outcome (kata h70z): once a
  // later [answers] reply resolves this call, its collapsed row should say
  // what was answered, not just what was asked - matching the old build's
  // "asked [Direction] — answered: 'Celsius to Fahrenheit'" single-line
  // recap. Returns undefined (no suffix, bare "Asked: [Header]") while the
  // question is still live/pending - a live thing stays looking unresolved.
  summarySuffix(item, model) {
    if (model === undefined) return undefined;
    return answeredAskUserSuffix(model, item);
  },
  body: AskUserBody,
});
