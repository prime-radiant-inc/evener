// Shared ask_user question/option parsing (wave-5 T4 extraction). Ground
// truth: agent/internal/tool/definitions.go's DefAskUser gives the exact
// argumentsJson shape - {questions:[{header?, question,
// options:[{label,detail,recommended?}], multi_select?, why?,
// if_unanswered?}]}, 1-4 questions - and the registry additionally accepts
// the single-question shorthand {question, options, header?, multi_select?,
// why?, if_unanswered?} at the top level (agent/internal/tool/registry.go's
// normalizeAskUserArgs wraps it into the batch form at the dispatch
// boundary). The transcript records a call's arguments as the model sent
// them, so a recorded shorthand call carries no questions array and both
// shapes must parse here. This used to live private to the web
// transcript's askUser.tsx (the wave-4 read-only tool-call renderer);
// wave 5's composer/askDock/** needed the identical shape check to build
// its interactive answering dock, so it became a leaf module the two
// could share. It is package API now: deriveAskQuestions sits on it and
// both frontends import that. askUser.tsx imports these back and is
// otherwise unchanged (its own rendering/tests stay byte-equivalent).
//
// Parsing is defensive throughout: a malformed argumentsJSON, a missing
// `questions` array, or an individual malformed question/option all
// degrade to a fallback rather than throwing, since this is untrusted wire
// JSON, not a value this file controls the shape of.

import type { ItemModel, ThreadModel } from "./model";
import { parseArgs } from "./toolCallText";

export interface AskUserOption {
  label: string;
  detail: string;
  recommended?: boolean;
}

export interface AskUserQuestion {
  header: string;
  question: string;
  options: AskUserOption[];
  multiSelect?: boolean;
  why?: string;
  ifUnanswered?: string;
}

function parseOption(raw: unknown): AskUserOption | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (typeof obj.label !== "string" || typeof obj.detail !== "string") return undefined;
  return { label: obj.label, detail: obj.detail, recommended: obj.recommended === true };
}

function parseQuestion(raw: unknown, index: number): AskUserQuestion | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const obj = raw as Record<string, unknown>;
  if (
    (obj.header !== undefined && typeof obj.header !== "string") ||
    typeof obj.question !== "string" ||
    !Array.isArray(obj.options)
  ) {
    return undefined;
  }
  const options = obj.options.map(parseOption).filter((o): o is AskUserOption => o !== undefined);
  if (options.length === 0) return undefined;
  return {
    header: typeof obj.header === "string" ? obj.header : `Question ${index + 1}`,
    question: obj.question,
    options,
    multiSelect: obj.multi_select === true,
    why: typeof obj.why === "string" ? obj.why : undefined,
    ifUnanswered: typeof obj.if_unanswered === "string" ? obj.if_unanswered : undefined,
  };
}

// parseAskUserQuestions returns undefined whenever there's nothing usable to
// render - argumentsJSON absent entirely, a JSON syntax error, valid JSON
// with no questions array, or every individual question in that array
// failing its own shape check - without distinguishing WHY here. An empty
// array can't legitimately occur (DefAskUser requires minItems:1) but is
// treated the same as undefined defensively either way. The shorthand form
// is wrapped into a one-element questions array before the per-question
// shape check, mirroring normalizeAskUserArgs, so a call the tool accepted
// never reads as unavailable data. Callers that need to draw their own
// absent-vs-malformed distinction (e.g. AskUserBody's fallback wording) do
// so with their own check alongside this one.
export function parseAskUserQuestions(item: Pick<ItemModel, "argumentsJSON">): AskUserQuestion[] | undefined {
  const args = parseArgs(item.argumentsJSON);
  let raw: unknown = args.questions;
  if (!Array.isArray(raw) && typeof args.question === "string" && Array.isArray(args.options)) {
    // Copy the wrapped fields with normalizeAskUserArgs' own rules rather
    // than passing the raw object through: Go's wrap drops a non-string or
    // empty header (and empty why/if_unanswered), while parseQuestion's
    // shape check treats a present-but-non-string header as a malformed
    // question. A call Go accepted must never read back as unavailable data.
    const wrapped: Record<string, unknown> = { question: args.question, options: args.options };
    if (typeof args.header === "string" && args.header !== "") wrapped.header = args.header;
    if (typeof args.why === "string" && args.why !== "") wrapped.why = args.why;
    if (typeof args.if_unanswered === "string" && args.if_unanswered !== "") wrapped.if_unanswered = args.if_unanswered;
    if (args.multi_select === true) wrapped.multi_select = true;
    raw = [wrapped];
  }
  if (!Array.isArray(raw)) return undefined;
  const questions = raw
    .map((question, index) => parseQuestion(question, index))
    .filter((q): q is AskUserQuestion => q !== undefined);
  return questions.length > 0 ? questions : undefined;
}

// One line of a composed [answers] reply (appwire-client/typescript/askAnswers.ts's composeAskAnswers,
// byte-exact format): "N. [Header] → resolution text[ — note: "..."]". Safe
// headers are raw; headers containing ]/CR/LF are JSON strings, so their
// delimiters cannot be mistaken for the framing brackets.
const ASK_ANSWER_LINE_RE = /^\d+\.\s(?:\[([^\]\r\n]*)\]|\[("(?:\\.|[^"\\])*")\])\s→\s(.*)$/;

// parseAskAnswerLines reads a composed [answers] reply's text back into a
// header -> resolution-text map. The optional trailing " — note: ..." is
// stripped: a note is supplementary context for the model, not part of the
// answer itself, and this map only ever feeds a one-line recap (kata h70z).
function parseAskAnswerLines(text: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const line of text.split("\n")) {
    const m = ASK_ANSWER_LINE_RE.exec(line);
    if (!m) continue;
    let header = m[1] ?? "";
    if (m[2] !== undefined) {
      try {
        const decoded = JSON.parse(m[2]);
        if (typeof decoded !== "string") continue;
        header = decoded;
      } catch {
        continue;
      }
    }
    const rest = m[3] ?? "";
    const noteAt = rest.indexOf(" — note: ");
    map.set(header, noteAt === -1 ? rest : rest.slice(0, noteAt));
  }
  return map;
}

// answeredAskUserSuffix builds the "— answered: ..." recap for a SETTLED,
// answered ask_user call - kata h70z's fix. Old build precedent: "asked
// [Direction] — answered: 'Celsius to Fahrenheit'" as a single collapsed
// line. The current transcript already collapses a clean ask_user row by
// default (ToolCallItem's generic disclosure contract), but its summary
// only ever showed "Asked: [Header]" - the answer lives in a SEPARATE,
// later reply item that this item alone can't see, forcing the reader to
// cross-reference a second, unstyled message to reconstruct "what did I
// answer". This reads the FULL thread model to find that reply and pull
// the answer back in.
//
// Returns undefined - meaning "show nothing extra, keep the bare
// 'Asked: [Header]' summary" - whenever there's nothing to append: the call
// errored (its own row already gets the failure treatment), it carries no
// parseable questions, or no later answer reply exists yet (still
// pending in the composer/dock - a LIVE thing, which per the same
// principle must stay looking unresolved).
export function answeredAskUserSuffix(model: ThreadModel, item: ItemModel): string | undefined {
  if (item.error !== undefined) return undefined;
  const questions = parseAskUserQuestions(item);
  if (!questions) return undefined;
  const items = model.turns.flatMap((turn) => turn.items);
  const at = items.findIndex((i) => i.id === item.id);
  if (at === -1) return undefined;
  const reply = findAnswerReply(items.slice(at + 1), item);
  if (!reply) return undefined;
  const answers = parseAskAnswerLines(reply.text);
  if (answers.size > 0) {
    const multiple = questions.length > 1;
    const parts = questions
      .map((q) => {
        const a = answers.get(q.header);
        if (a === undefined) return undefined;
        return multiple ? `${q.header}: ${a}` : a;
      })
      .filter((p): p is string => p !== undefined);
    return parts.length > 0 ? ` — answered: ${parts.join(", ")}` : undefined;
  }
  // A linked reply whose text is not the composed form is the spec's other
  // reply form - free prose (§4.3: "either the numbered [answers] form ...
  // or free prose; treat either as the reply to everything you asked") -
  // and the reply's own words are the answer.
  const prose = reply.text.trim();
  return prose !== "" ? ` — answered: ${prose}` : undefined;
}

// One user-role item that can carry a round's answer: a recorded user
// message, or the user-sourced steering a fenced answer records (the ask
// fence's steering append publishes the same answer evidence - agent's
// session_client_mutation). Daemon-sourced steering is machinery, not an
// answer.
function isAnswerCarrier(item: ItemModel): boolean {
  return item.type === "userMessage" || (item.type === "steering" && item.source === "user");
}

// findAnswerReply locates the reply that answered this call among the items
// after it. The durable rule decides first: a reply carrying the wire's
// answer link (ThreadItem.observation.answerToTurnIds, onto the model as
// answerToTurnIds) is server-stamped evidence naming the round it answers,
// so the first linked reply after the call answers this call exactly when it
// names this item's turn - and a reply linked to another round is refuted
// even when its text shape looks like this round's composed reply. Without
// any link (an older daemon, or a reply the observation never reached) the
// composed "[answers]" text shape is the only evidence, exactly as before
// the link existed.
function findAnswerReply(later: readonly ItemModel[], item: ItemModel): ItemModel | undefined {
  const linked = later.find((i) => isAnswerCarrier(i) && i.answerToTurnIds !== undefined);
  if (linked !== undefined) return linked.answerToTurnIds?.includes(item.turnId) ? linked : undefined;
  return later.find((i) => i.type === "userMessage" && i.text.startsWith("[answers]"));
}
