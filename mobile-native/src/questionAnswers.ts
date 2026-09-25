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
} from "./projectedRows";
export type QuestionSelections = Record<
  string,
  Pick<AskAnswerItem, "resolution" | "note">
>;
export function pendingQuestions(
  conversation: MobileConversation | null,
): AskQuestionRef[] {
  // Asked of the MODEL, with the package's own rule — the same call the
  // projection's question rows come from (project.ts's askQuestionsByCall,
  // through liveAsksFor's shared scan), so the refs are canonical: the sheet
  // renders, and the answer composer validates against, exactly the labels
  // the agent offered. The timeline rows a reader scrolls carry the display
  // bound's cut copies instead — a label longer than the bound would submit
  // as its truncated remnant if this list read those rows, and two options
  // sharing a prefix longer than the bound cut to the same string and become
  // indistinguishable. The wire's askPending gate still applies: the package's
  // own liveAskQuestions returns nothing while the model says nothing is
  // pending, so the explicit conversation.askPending check is not lost.
  if (conversation === null) return [];
  return [...liveAsksFor(conversation).values()].flat();
}

// The sheet's own rendered text, bounded the same as a timeline row
// (the row module, projectedRows.ts). Shared by questionsIdentity below and
// QuestionSheet.tsx's own display copy, so the identity and what a reader
// actually sees are cut exactly the same way.
export const boundQuestionText = (text: string) =>
  truncateText(text, MAX_ITEM_BYTES);

// cyrb53 (bryc's widely-used non-cryptographic string hash): two 32-bit lanes
// mixed together in one pass, so the collision rate is far below a single
// 32-bit hash while staying dependency-free and fast over megabyte-scale
// prose. Not security-sensitive — only used below to tell "the same
// question" apart from "a different one" at a fixed size, never to answer a
// choice the agent did not offer.
function questionHash(text: string): string {
  let h1 = 0xdeadbeef;
  let h2 = 0x41c6ce57;
  for (let i = 0; i < text.length; i++) {
    const ch = text.charCodeAt(i);
    h1 = Math.imul(h1 ^ ch, 2654435761);
    h2 = Math.imul(h2 ^ ch, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
  return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(36);
}

// A question set's identity for everything that keys, signs or persists it —
// a React key over a batch, the sheet's draft signature, and the definitions
// that guard a persisted answer (draftRepository.ts's questionDefinitions).
// None of those need pendingQuestions()'s canonical, uncut refs: they only
// ever need to tell "the same questions" apart from "different questions",
// same as a reader could from the screen — but a TRUNCATED copy of the
// canonical fields (this used to bound-and-JSON.stringify boundQuestion's
// display copy) collides on any two questions that share a prefix past the
// display bound, and re-serializing megabyte-scale prose on every render is
// the exact cost this identity exists to avoid paying. Hashing the canonical
// fields keeps the identity fixed-size AND collision-resistant regardless of
// input size, and the memo below (keyed on the question-array reference
// reconcileBatches.ts hands back unchanged when nothing changed) means an
// unaffected render or keystroke never rehashes at all.
// Each element also carries the digest of its display-bound copy
// (boundQuestion over boundQuestionText): before this identity existed the
// sheet persisted that bounded copy itself as a draft's definition, and a
// stored truncated copy can never hash to the canonical digest — so the
// comparison (sameQuestion below) accepts either, without putting the
// bounded prose itself back into the signature. The bounded copy hashes
// through the same canonicalQuestion rebuild as every other digest input,
// so a stored bounded element matches whatever field order its era's
// builder wrote — while the package-built copy bounded here serializes
// identically either way.
const questionsIdentityMemo = new WeakMap<AskQuestionRef[], string>();

// The comparable form of one persisted question definition: its {key, digest}
// pair. A signature's elements have carried that pair since questionsIdentity
// signed them, but a draft saved by an older build holds the question object
// its era's builder serialized — canonical, display-wrapped, or the display
// bound's truncated copy — as its definition: same question, an earlier
// era's shape. All normalize to the same pair: an element that already
// carries its digest keeps it, and a legacy question hashes (through
// canonicalQuestion below) to the digest the identity computes for that
// same canonical question, so a pre-identity draft still compares equal
// (draftRepository.ts's questionDefinitions) and an app update never
// silently drops a reader's saved answers.
export interface QuestionDefinition {
  key: string;
  digest: string;
  // The digest of the question's display-bound copy, carried only by the
  // identity's own elements (questionsIdentity below) so a stored definition
  // an older build serialized from the bound rows can still be recognized.
  // A legacy element predating the identity has none.
  boundDigest?: string;
}

// Any element the persisted signature has ever held. The identity's own
// elements carry their digest (and bound digest) as strings; a legacy era's
// element is the question object its builder serialized — the canonical
// fields in that era's own order, the display era's nested twin beside
// them. Every field but key is unknown-typed: the repository's parse has
// validated only the key (draftRepository.ts's questionDefinitions), and
// the normalizer below — not the type — decides what each field
// contributes to a digest.
type PersistedQuestionElement = {
  key: string;
  digest?: unknown;
  boundDigest?: unknown;
  display?: unknown;
  callId?: unknown;
  header?: unknown;
  question?: unknown;
  options?: unknown;
  multiSelect?: unknown;
  why?: unknown;
  ifUnanswered?: unknown;
};

// The one form every era's element is rebuilt into before hashing: the
// wire's AskQuestionRef fields, in the exact order the package's
// liveAskQuestions builds them — the same literal the identity hashes — so
// a digest depends only on WHICH question an element names, never on which
// era's builder serialized it. History's builders each wrote a different
// order or wrapper: #1096's projection put key first and left callId to
// pendingQuestions' trailing spread, #1488's shim appended key and callId
// after the parsed wire fields, and the display era (d0f40080cb's
// withQuestionDisplay) wrapped the canonical ref inside a nested `display`
// twin whose bounded prose is not the question the identity signs — hashing
// any of those as-persisted yields a digest the identity never computes for
// the same question, silently dropping a saved answer on upgrade. The twin
// is stripped and the fields rebuilt here; for a ref the package itself
// built the rebuild is byte-identical to the ref's own serialization, so
// the identity's digests never change and no stored digest that matches
// today stops matching.
function canonicalQuestion(element: PersistedQuestionElement) {
  const { display, ...fields } = element;
  return {
    key: fields.key,
    callId: fields.callId,
    header: fields.header,
    question: fields.question,
    options: fields.options,
    multiSelect: fields.multiSelect,
    ...(fields.why === undefined ? {} : { why: fields.why }),
    ...(fields.ifUnanswered === undefined
      ? {}
      : { ifUnanswered: fields.ifUnanswered }),
  };
}

export function questionDefinition(
  question: PersistedQuestionElement,
): QuestionDefinition {
  const definition: QuestionDefinition = {
    key: question.key,
    digest:
      typeof question.digest === "string"
        ? question.digest
        : questionHash(JSON.stringify(canonicalQuestion(question))),
  };
  if (typeof question.boundDigest === "string")
    definition.boundDigest = question.boundDigest;
  return definition;
}

// Whether a persisted definition names the same question the identity just
// signed. The identity signs the canonical digest, but the sheet that
// predated it serialized the bounded timeline rows themselves, so a draft
// from that window holds the display bound's TRUNCATED copy — whose hash
// matches no canonical digest. The untruncated text is gone from the stored
// row, so the current element carries the digest of its own bounded copy
// alongside the canonical one, and either is "the same question" here. The
// bounded digest cannot see past the display bound — two questions that
// differ only beyond it compare equal, exactly as they did under the era
// that persisted them — and the first save rewrites the definition
// canonically.
export function sameQuestion(
  stored: QuestionDefinition | undefined,
  current: QuestionDefinition,
): boolean {
  if (!stored) return false;
  return (
    stored.digest === current.digest ||
    (current.boundDigest !== undefined &&
      stored.digest === current.boundDigest)
  );
}

// A JSON array of definitions, not a bare hash string: draftRepository.ts's
// questionDefinitions parses this signature expecting an array it can index
// per key (JSON.parse(signature) -> question.key), the same contract a
// bounded-and-stringified question array satisfied before this identity was
// hashed. A bare hash string parses as neither an array nor an object with a
// key, so writeQuestions/readQuestions would throw on every call the moment
// the identity changed shape (#1731 piece E round 4's own High).
export function questionsIdentity(questions: AskQuestionRef[]): string {
  const cached = questionsIdentityMemo.get(questions);
  if (cached !== undefined) return cached;
  const identity = JSON.stringify(
    questions.map((question) => ({
      ...questionDefinition(question),
      boundDigest: questionHash(
        JSON.stringify(
          canonicalQuestion(boundQuestion(question, boundQuestionText)),
        ),
      ),
    })),
  );
  questionsIdentityMemo.set(questions, identity);
  return identity;
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
