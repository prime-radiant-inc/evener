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
const ASK_ANSWER_LINE_RE = /^(\d+)\.\s(?:\[([^\]\r\n]*)\]|\[("(?:\\.|[^"\\])*")\])\s→\s(.*)$/;

// parseAskAnswerLine reads one line of a composed [answers] reply into its
// number, the header it answers and the text after the arrow, or undefined
// when the line is not an answer line at all.
function parseAskAnswerLine(line: string): { ordinal: number; header: string; rest: string } | undefined {
  const m = ASK_ANSWER_LINE_RE.exec(line);
  if (!m) return undefined;
  let header = m[2] ?? "";
  if (m[3] !== undefined) {
    try {
      const decoded = JSON.parse(m[3]);
      if (typeof decoded !== "string") return undefined;
      header = decoded;
    } catch {
      return undefined;
    }
  }
  return { ordinal: Number(m[1]), header, rest: m[4] ?? "" };
}

// parseAskAnswerLines reads a composed [answers] reply's text back into a
// header -> resolution-text map. The optional trailing " — note: ..." is
// stripped: a note is supplementary context for the model, not part of the
// answer itself, and this map only ever feeds a one-line recap (kata h70z).
function parseAskAnswerLines(text: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const line of text.split("\n")) {
    const answer = parseAskAnswerLine(line);
    if (!answer) continue;
    const noteAt = answer.rest.indexOf(" — note: ");
    map.set(answer.header, noteAt === -1 ? answer.rest : answer.rest.slice(0, noteAt));
  }
  return map;
}

// composedReplyCoversCall answers whether a composed [answers] reply is
// PROVABLY the whole content the recap for this call will show, rather than
// merely parseable into something like it.
//
// The recap is a lossy render (it restates matched headers and drops every
// other word), so it cannot certify its own completeness; a predicate that
// re-derives what the recap "meant" keeps admitting a new case that loses
// text. This instead demands the reply BE the composed answer set for these
// questions, byte for byte, built by the composer itself:
//
//   - the first line is exactly the "[answers]" marker, nothing appended;
//   - the answer lines carry the composer's own 1..N numbering, in order, so
//     a dropped or reordered line breaks the match;
//   - every header is one this call asked, and no header repeats (two
//     questions may share a header, and the recap's header-keyed parse keeps
//     only the last, so a repeat is not provably equal);
//   - no line carries a note, which the recap strips.
//
// Any reply that fails is one the recap does not fully stand in for, and its
// raw row must stay: a duplicate row is cosmetic, a silently dropped answer
// is not.
function composedReplyCoversCall(
  text: string,
  questions: readonly AskUserQuestion[],
  answers: ReadonlyMap<string, string>,
): boolean {
  const askedHeaders = new Set(questions.map((question) => question.header));
  if (askedHeaders.size !== questions.length) return false;
  const lines = text.split("\n");
  if (lines[0] !== "[answers]") return false;
  const seen = new Set<string>();
  let ordinal = 0;
  for (const line of lines.slice(1)) {
    if (line === "") continue;
    const answer = parseAskAnswerLine(line);
    if (!answer) return false;
    ordinal += 1;
    if (answer.ordinal !== ordinal) return false;
    if (!askedHeaders.has(answer.header)) return false;
    if (seen.has(answer.header)) return false;
    seen.add(answer.header);
    if (answer.rest.includes(" — note: ")) return false;
  }
  // Every question this call asked must have an answer line, or the recap
  // would restate fewer answers than the call had.
  return seen.size === questions.length && seen.size === answers.size;
}

// answerFor resolves the recap for a SETTLED, answered ask_user call once:
// the reply item the recap would speak for, and the suffix string that
// recap renders. Every gate that decides whether a recap shows at all
// lives here, so the two can never disagree: the call errored (its own row
// already gets the failure treatment), it carries no parseable questions,
// or no later answer reply exists yet (still pending in the composer/dock
// - a LIVE thing, which per the same principle must stay looking
// unresolved). Returns undefined whenever there is nothing to append -
// meaning "show nothing extra, keep the bare 'Asked: [Header]' summary".
// carriesWholeReply says whether the suffix carries the reply's own words in
// full: a one-line free-prose answer does, a multi-line one does not (the
// recap shows its first line only), and the composed form does (its lines
// ARE the answers the suffix restates).
function answerFor(
  model: Pick<ThreadModel, "turns">,
  item: ItemModel,
): { reply: ItemModel; suffix: string; carriesWholeReply: boolean } | undefined {
  if (item.error !== undefined) return undefined;
  const questions = parseAskUserQuestions(item);
  if (!questions) return undefined;
  const items = model.turns.flatMap((turn) => turn.items);
  const at = items.findIndex((i) => i.id === item.id);
  if (at === -1) return undefined;
  const reply = findAnswerReply(items, items.slice(at + 1), item);
  if (!reply) return undefined;
  if (reply.text.startsWith("[answers]")) {
    // The composed form always resolves here, even when it parses to nothing
    // or nothing matches this call's headers (the user edited the draft,
    // spec §6.1): an attempted composed reply is not free prose to echo.
    const answers = parseAskAnswerLines(reply.text);
    const multiple = questions.length > 1;
    const parts = questions
      .map((q) => {
        const a = answers.get(q.header);
        if (a === undefined) return undefined;
        return multiple ? `${q.header}: ${a}` : a;
      })
      .filter((p): p is string => p !== undefined);
    if (parts.length === 0) return undefined;
    return {
      reply,
      suffix: ` — answered: ${parts.join(", ")}`,
      carriesWholeReply: composedReplyCoversCall(reply.text, questions, answers),
    };
  }
  // A linked reply whose text is not the composed form is the spec's other
  // reply form - free prose (§4.3: "either the numbered [answers] form ...
  // or free prose; treat either as the reply to everything you asked") -
  // and the reply's own words are the answer. The recap's home is one
  // collapsed row, so only the reply's first line carries, with an
  // ellipsis when the reply said more.
  const trimmed = reply.text.trim();
  const [first, ...rest] = trimmed.split("\n");
  const more = rest.some((line) => line.trim() !== "");
  if (first === "") return undefined;
  return { reply, suffix: ` — answered: ${first}${more ? " …" : ""}`, carriesWholeReply: !more };
}

// answeredAskUserReply names the reply item whose answer the recap for the
// ask_user call `item` carries IN FULL - the reply a client may drop from
// the raw transcript because the recap already shows everything it said
// (#6328). Nothing when the recap shows nothing, and nothing when the recap
// shows only part of a multi-line free-prose reply. Resolving through this
// rather than re-deriving the link semantics keeps the two from disagreeing:
// the answer is never dropped without the recap standing in for it.
export function answeredAskUserReply(model: Pick<ThreadModel, "turns">, item: ItemModel): ItemModel | undefined {
  const recap = answerFor(model, item);
  if (recap?.carriesWholeReply !== true) return undefined;
  // The recap speaks for the human ("You answered"); a row another actor
  // wrote - a Manager settling a question on its own - carries an
  // attribution the recap cannot show. Hiding it would attribute an
  // automated decision to the person, so the raw row stays authoritative.
  if (recap.reply.actor !== undefined && recap.reply.actor.kind !== "user") return undefined;
  return recap.reply;
}

// answeredAskUserSuffix builds the "— answered: ..." recap for a SETTLED,
// answered ask_user call - kata h70z's fix. Old build precedent: "asked
// [Direction] — answered: 'Celsius to Fahrenheit'" as a single collapsed
// line. The current transcript already collapses a clean ask_user row by
// default (ToolCallItem's generic disclosure contract), but its summary
// only ever showed "Asked: [Header]" - the answer lives in a SEPARATE,
// later reply item that this item alone can't see, forcing the reader to
// cross-reference a second, unstyled message to reconstruct "what did I
// answer". This reads the whole turn list to find that reply and pull
// the answer back in. The gates live in answerFor above.
export function answeredAskUserSuffix(model: Pick<ThreadModel, "turns">, item: ItemModel): string | undefined {
  return answerFor(model, item)?.suffix;
}

// One acked, answerable ask_user call - the only population that can hold a
// round's questions. A rejected call posts no questions (its arguments may
// still parse - the projector stamps it "completed" with error set), and a
// still-streaming call has not posted them yet; neither can be a reply's
// target. Mirrors deriveAskQuestions's isAckedAskUserItem, which gates
// liveness the same way; the two must not drift.
function isAckedAskCall(candidate: ItemModel): boolean {
  return (
    candidate.type === "commandExecution" &&
    candidate.toolName === "ask_user" &&
    candidate.status === "completed" &&
    candidate.error === undefined
  );
}

// What a linked reply naming item's turn can say about THIS call. Without
// acked sibling calls in the same execution it is this call's answer. With
// them, free prose names no header and could belong to any sibling, and a
// composed reply is decisive only through a header it answers that THIS
// call asks and no sibling also asks (a shared header could belong to
// either); a composed reply answering only the sibling's headers says
// nothing about this call. (The wire's link names an execution turn, not a
// call - the whole heuristic exists because of that granularity, and
// exposing admission's call-level AskResolution on the wire would replace
// it with an exact match; see the follow-up issue filed from #5836.)
function linkedReplyAnswersThisCall(items: readonly ItemModel[], item: ItemModel, reply: ItemModel): boolean {
  const siblings = items.filter((i) => i.id !== item.id && i.turnId === item.turnId && isAckedAskCall(i));
  if (siblings.length === 0) return true;
  if (!reply.text.startsWith("[answers]")) return false;
  const siblingHeaders = new Set(
    siblings.flatMap((sibling) => (parseAskUserQuestions(sibling) ?? []).map((question) => question.header)),
  );
  const answered = parseAskAnswerLines(reply.text);
  let answersOwn = false;
  for (const question of parseAskUserQuestions(item) ?? []) {
    if (!answered.has(question.header)) continue;
    if (siblingHeaders.has(question.header)) return false;
    answersOwn = true;
  }
  return answersOwn;
}

// isUserAuthoredSteer answers one wire-level question: is this steering item
// the human speaking, as opposed to the daemon injecting one on its own
// (SteeringSourceUser) or a human-note update recorded through the same
// source (session_notes_selfecho.go's steeringOrigin machinery stamps a note
// SteeringKindHumanNote even though a human wrote it - writing a note is not
// the human speaking through the transcript). Shared with web layoutRoles.ts,
// which asks the same wire question for its own, unrelated reason (routing
// the item to a divider instead of a user bubble); callers add their own
// `item.type === "steering"` gate first, since this only reads the fields a
// non-steering item never sets. Lived in deriveAskQuestions until the answer
// carrier below needed it too; it moved here so the shared leaf owns it and
// deriveAskQuestions re-exports (deriveAskQuestions imports this module, so
// the other direction would cycle).
export function isUserAuthoredSteer(item: ItemModel): boolean {
  return item.source === "user" && item.steeringKind !== "human-note";
}

// One user-role item that can carry a round's answer: a recorded user
// message, or the user-authored steering a fenced answer records (the ask
// fence's steering append publishes the same answer evidence - agent's
// session_client_mutation). Daemon-sourced steering is machinery, a
// human-note steer is the whiteboard speaking, and an interrupted steer is
// a cancelled round's boundary marker (session_lifecycle.go) - the ask it
// resolved was never answered, so its marker text must not recap as one.
function isAnswerCarrier(item: ItemModel): boolean {
  return (
    item.type === "userMessage" ||
    (item.type === "steering" && item.steeringKind !== "interrupted" && isUserAuthoredSteer(item))
  );
}

// findAnswerReply locates the reply that answered this call among the items
// after it, in one pass. The durable rule decides first: a reply carrying
// the wire's answer link (ThreadItem.observation.answerToTurnIds, onto the
// model as answerToTurnIds) is server-stamped evidence naming the round it
// answers. Among carriers, the first linked reply naming ANOTHER round ends
// the search: the fence holds the floor until this round is answered, so
// this round's own reply can only sit before any foreign link - everything
// after that link answered a later round. A linked reply naming THIS turn is
// decisive when it can unambiguously answer THIS call
// (linkedReplyAnswersThisCall); one that cannot - free prose beside acked
// sibling calls, or a composed reply answering only the siblings' headers -
// is skipped, not decisive, so a later unambiguous reply can still supply
// this call's recap: the fence keeps a call appended after a scoped answer
// pending (TestAskFenceScopedAnswerPreservesPostAdmissionAppend), and its
// own answer arrives after the earlier one. The composed "[answers]" shape
// matched before a foreign link still stands, though: in a mixed thread (a
// reply recorded by an older daemon that stamped no link), erasing it would
// lose this round's answer to an unrelated later link. With no link
// anywhere (an older daemon, or a reply the observation never reached) the
// shape is the only evidence, exactly as before the link existed.
// resolvesThisCallExactly answers whether admission's per-call stamp can be
// used for this call WITHOUT risk of pairing an answer with a question the
// row does not show (#6323). Four conditions, all required:
//
//   - the stamp names EXACTLY ONE call, and it is this row's own
//     (turnId, callId). A reply that answered several calls cannot attribute
//     its composed lines to any of them: the recap is keyed by header, so two
//     claimed calls sharing a header leave one line for both, and a rule that
//     tries to reason about the sibling set keeps admitting a case where the
//     line lands under the wrong question. One call is the only shape where
//     the mapping is provable.
//   - admission left no question unnamed (resolvedAskLegacyCount). Those
//     questions' headers never reach the wire, so one of them may be the
//     header an answer line names and no named call can account for it.
//   - the questions the call RECORDED, when the page loaded them, are the
//     same ones its arguments carry - headers, prompts, and what each option
//     means. The transcript rows draw their question from the arguments, so
//     where the two disagree a recap would print an answer beneath a question
//     it does not answer; that disagreement is a product decision,
//     deliberately not made here.
//
// False means "use the behavior this client had before the field existed",
// which is the heuristic in findAnswerReply and its shape fallback. That is
// why an unloaded recorded state is not a refusal: nothing can disagree yet,
// and the heuristic's own header comparison still stands in the way of a
// wrong pairing.
function resolvesThisCallExactly(items: readonly ItemModel[], item: ItemModel, reply: ItemModel): boolean {
  const calls = reply.resolvedAskCalls;
  if (calls === undefined || item.callId === undefined) return false;
  if (calls.length !== 1) return false;
  if (reply.resolvedAskLegacyCount !== undefined && reply.resolvedAskLegacyCount > 0) return false;
  const [only] = calls;
  if (only === undefined || only.turnId !== item.turnId || only.callId !== item.callId) return false;
  const recorded = recordedAskQuestions(items, item);
  if (recorded === undefined) return true;
  const sent = parseAskUserQuestions(item);
  if (sent === undefined) return true;
  return sameAskQuestions(recorded, sent);
}

// stampedReplyRulesOutCall answers whether admission's stamp is EVIDENCE
// AGAINST attributing this reply to this call, as opposed to merely failing to
// prove it. The two are not the same, and the difference decides whether the
// heuristic below may still speak:
//
//   - the stamp omits this call: admission says the reply answered other
//     calls, so handing this one to the heuristic would re-pair the reply with
//     a call admission did not name. Reachable whenever the named call is not
//     on the loaded page. Evidence against.
//   - the stamp names this call but the questions it recorded differ from the
//     ones the row displays: the reply answers a question this row does not
//     show, so a recap would print it beneath the wrong prompt. Evidence
//     against.
//   - the stamp names this call and the two question sets agree, but claims
//     several calls: the stamp simply cannot say which line belongs to which.
//     That is not evidence against this call - the heuristic may still prove
//     the line by a header no sibling shares, and does. Falls back.
function stampedReplyRulesOutCall(items: readonly ItemModel[], item: ItemModel, reply: ItemModel): boolean {
  if (reply.resolvedAskCalls === undefined) return false;
  if (item.callId === undefined) return true;
  if (!reply.resolvedAskCalls.some((call) => call.turnId === item.turnId && call.callId === item.callId)) return true;
  const recorded = recordedAskQuestions(items, item);
  if (recorded === undefined) return false;
  const sent = parseAskUserQuestions(item);
  if (sent === undefined) return false;
  return !sameAskQuestions(recorded, sent);
}

// recordedAskQuestions reads the questions a call ACTUALLY asked, from the
// successful result's recorded state (schema.AskUserState, which the wire
// carries on the result item's raw). Undefined when the page holds no such
// state for this call - an older daemon, or a result row it did not load.
function recordedAskQuestions(items: readonly ItemModel[], call: ItemModel): AskUserQuestion[] | undefined {
  const own = askQuestionsFromRecord(call);
  if (own !== undefined) return own;
  if (call.callId === undefined) return undefined;
  // A live item may arrive as two rows, so look for the recorded state on the
  // pair - a call id alone repeats across executions.
  for (const candidate of items) {
    if (candidate === call) continue;
    if (candidate.turnId !== call.turnId || candidate.callId !== call.callId) continue;
    const recorded = askQuestionsFromRecord(candidate);
    if (recorded !== undefined) return recorded;
  }
  return undefined;
}

function askQuestionsFromRecord(item: Pick<ItemModel, "raw">): AskUserQuestion[] | undefined {
  const raw = item.raw;
  if (typeof raw !== "object" || raw === null) return undefined;
  const questions = (raw as { questions?: unknown }).questions;
  if (!Array.isArray(questions) || questions.length === 0) return undefined;
  // schema.AskUserQuestion's own omitempty rules, not the arguments shape's.
  const parsed: AskUserQuestion[] = [];
  for (const [index, question] of questions.entries()) {
    if (typeof question !== "object" || question === null) return undefined;
    const obj = question as Record<string, unknown>;
    const header = typeof obj.header === "string" && obj.header !== "" ? obj.header : `Question ${index + 1}`;
    if (typeof obj.question !== "string" || !Array.isArray(obj.options) || obj.options.length === 0) return undefined;
    const options: AskUserOption[] = [];
    for (const option of obj.options) {
      if (typeof option !== "object" || option === null) return undefined;
      const fields = option as Record<string, unknown>;
      if (typeof fields.label !== "string") return undefined;
      options.push({
        label: fields.label,
        detail: typeof fields.detail === "string" ? fields.detail : "",
        recommended: fields.recommended === true,
      });
    }
    parsed.push({
      header,
      question: obj.question,
      options,
      multiSelect: obj.multi_select === true,
      why: typeof obj.why === "string" ? obj.why : undefined,
      ifUnanswered: typeof obj.if_unanswered === "string" ? obj.if_unanswered : undefined,
    });
  }
  return parsed;
}

// sameAskQuestions compares the two question sets a recap must reconcile: the
// header each answer is keyed by, the text the row displays, AND what each
// option means. A hook may keep the header and the question while rewriting
// an option's label or detail, so neither headers alone nor the prompt can
// stand for "the row shows what the reply answers".
function sameAskQuestions(recorded: readonly AskUserQuestion[], sent: readonly AskUserQuestion[]): boolean {
  if (recorded.length !== sent.length) return false;
  for (let index = 0; index < recorded.length; index += 1) {
    const a = recorded[index];
    const b = sent[index];
    if (a === undefined || b === undefined) return false;
    if (a.header !== b.header || a.question !== b.question) return false;
    if (a.options.length !== b.options.length) return false;
    for (let option = 0; option < a.options.length; option += 1) {
      const left = a.options[option];
      const right = b.options[option];
      if (left === undefined || right === undefined) return false;
      if (left.label !== right.label || left.detail !== right.detail) return false;
    }
  }
  return true;
}

function findAnswerReply(
  items: readonly ItemModel[],
  later: readonly ItemModel[],
  item: ItemModel,
): ItemModel | undefined {
  let shapeMatch: ItemModel | undefined;
  for (const candidate of later) {
    if (candidate.answerToTurnIds !== undefined) {
      if (!isAnswerCarrier(candidate)) continue;
      if (!candidate.answerToTurnIds.includes(item.turnId)) return shapeMatch;
      // INVARIANT (#6323): the stamp is consumed ONLY where its recap is
      // provable, which means a stamp naming exactly one call. A composed
      // reply's lines are keyed by header, so a stamp naming several calls
      // leaves one line standing for several questions and no rule can say
      // which - that shape is refused and takes the pre-stamp path below.
      // Within the single-call shape the remaining conditions make the line
      // and the question a bijection: no question unnamed, and the recorded
      // questions equal the ones the row displays. Every other case FALLS
      // BACK to this client's behavior before the field existed, which is why
      // the narrowed rule loses no recap it cannot prove.
      if (resolvesThisCallExactly(items, item, candidate)) return candidate;
      // A stamped reply is authoritative about which calls it answered. When
      // it rules this call out - or cannot attribute its own recap to it -
      // stop considering it rather than letting the header heuristic adopt
      // it, which would re-pair the reply with a call admission did not name.
      if (stampedReplyRulesOutCall(items, item, candidate)) continue;
      if (linkedReplyAnswersThisCall(items, item, candidate)) return candidate;
      continue;
    }
    if (shapeMatch === undefined && candidate.type === "userMessage" && candidate.text.startsWith("[answers]")) {
      shapeMatch = candidate;
    }
  }
  return shapeMatch;
}
