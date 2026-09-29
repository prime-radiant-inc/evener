// What a transcript step says, in both clients: which transcript a
// read_transcript call read and how much of it, and what a
// find_session_transcripts call searched for and found. The web's tool rows
// and the phone's step lines read these.

import type { ItemModel } from "./model";
import { composeStepWords, type StepWords, withDetail } from "./stepWords";
import { clip, parseArgs, str } from "./toolCallText";
import { lastLine, outputTails, toolJSONResult } from "./toolEvidence";

/** The parts of a transcript step its words read. */
export type TranscriptStep = Pick<ItemModel, "argumentsJSON" | "output">;

/** "1 turn", "3 turns". */
export function turns(n: number): string {
  return `${n} ${n === 1 ? "turn" : "turns"}`;
}

// --- read_transcript ------------------------------------------------------------

// A transcript_ref is "local:<ULID>" / "job:<job_id>" / a bare session id. The
// scheme prefix is machinery; the id is what a human recognizes.
function refId(ref: string): string {
  const colon = ref.indexOf(":");
  return colon === -1 ? ref : ref.slice(colon + 1);
}

/** A read_transcript result, whichever of its three shapes came back. */
export interface TranscriptEnvelope {
  ref: string;
  turnsTotal?: number;
  turnsRendered?: number;
  elidedTurns?: number;
  content?: string;
  expandTurn?: number;
}

function num(obj: Record<string, unknown>, key: string): number | undefined {
  const value = obj[key];
  return typeof value === "number" ? value : undefined;
}

// The result, whichever of the three shapes came back, read from the tool's
// own JSON before any intervention the registry appended after it. The
// outline envelope is flat and the markdown/jsonl ones nest their counts
// under `meta`, so both spellings are read; absence stays absence (never
// defaulted to 0, which would claim a count the tool never reported).
export function readTranscriptEnvelope(item: TranscriptStep): TranscriptEnvelope | undefined {
  const parsed = toolJSONResult(item.output);
  if (!parsed) return undefined;
  const meta = typeof parsed.meta === "object" && parsed.meta !== null ? (parsed.meta as Record<string, unknown>) : {};
  const expansion =
    typeof parsed.expansion === "object" && parsed.expansion !== null
      ? (parsed.expansion as Record<string, unknown>)
      : undefined;
  return {
    ref: str(parsed, "transcript_ref") ?? "",
    turnsTotal: num(parsed, "turns_total") ?? num(meta, "turns_total"),
    turnsRendered: num(meta, "turns_rendered"),
    elidedTurns: num(parsed, "elided_turns") ?? num(meta, "elided_turns"),
    content: str(parsed, "content"),
    expandTurn: expansion ? num(expansion, "expand_turn") : undefined,
  };
}

// resolvedRef is the ref this call actually read: the caller's own argument
// first, the envelope's echo of it as the fallback (a hydrated item whose args
// were dropped still has the envelope).
function resolvedRef(item: TranscriptStep): string {
  const fromArgs = str(parseArgs(item.argumentsJSON), "transcript_ref")?.trim();
  if (fromArgs !== undefined && fromArgs !== "") return fromArgs;
  return readTranscriptEnvelope(item)?.ref ?? "";
}

// A "job:<job_id>" ref reads a shell job's output LOG, not a conversation - a
// different kind of thing, and the one case with no turns at all.
function isJobRead(item: TranscriptStep): boolean {
  return resolvedRef(item).startsWith("job:");
}

// What was read, in the reader's terms: a job's output log, an API-log record,
// or a session conversation - and whose (the id, which a client sets apart).
function target(item: TranscriptStep): { what: string; id?: string } {
  const args = parseArgs(item.argumentsJSON);
  const ref = resolvedRef(item);
  if (ref.startsWith("job:")) return { what: "job log", id: refId(ref) };
  if (str(args, "source") === "api_log") return { what: "API log", id: refId(ref) };
  // An absent/"current" ref means the session the agent is already in.
  if (ref === "" || ref === "current") return { what: "this session's transcript" };
  return { what: "transcript", id: refId(ref) };
}

// How much was read - the honest span, straight off the envelope. Absent when
// the call is still live (no envelope yet) or the output wasn't the JSON this
// tool documents.
function extent(item: TranscriptStep): string | undefined {
  const envelope = readTranscriptEnvelope(item);
  if (!envelope) return undefined;
  if (envelope.expandTurn !== undefined) return `turn ${envelope.expandTurn} in full`;
  // A job log has no turns: readJobTranscript hardcodes turns_total/
  // turns_rendered to 1 and range to "shell-log" (agent/
  // session_tools_transcript.go), so reporting "all 1 turn" for a shell output
  // log would be describing an artifact of the envelope, not the read.
  if (isJobRead(item)) return undefined;
  const total = envelope.turnsTotal;
  if (total === undefined) return undefined;
  const rendered = envelope.turnsRendered;
  if (rendered === undefined) return `outline of ${turns(total)}`;
  return rendered >= total ? `all ${turns(total)}` : `${rendered} of ${turns(total)}`;
}

/** "Read transcript 02wMz5… · all 12 turns", "Read job log job_x". */
export function readTranscriptWords(step: TranscriptStep): StepWords {
  const { what, id } = target(step);
  return withDetail(id ? { verb: `Read ${what}`, target: id } : { verb: `Read ${what}` }, extent(step));
}

/** The read's words as one line. */
export function readTranscriptSummary(step: TranscriptStep): string {
  return composeStepWords(readTranscriptWords(step));
}

/** "Reading transcript 02wMz5…", while the read runs. */
export function readTranscriptProgress(step: Pick<TranscriptStep, "argumentsJSON">): string {
  const { what, id } = target(step);
  return id ? `Reading ${what} ${id}` : `Reading ${what}`;
}

// --- find_session_transcripts ---------------------------------------------------

// What a search asked for. `children_of` leads when both are present, matching
// the tool's own precedence: asking for one session's children is a different
// question from a text search, and the parent ref is the more specific answer.
// With neither, the tool lists the project's recent sessions.
type SessionsSearch = { kind: "children"; ref: string } | { kind: "query"; query: string } | { kind: "catalog" };

function sessionsSearch(step: Pick<TranscriptStep, "argumentsJSON">): SessionsSearch {
  const args = parseArgs(step.argumentsJSON);
  const childrenOf = str(args, "children_of");
  if (childrenOf) return { kind: "children", ref: childrenOf };
  const query = str(args, "query");
  if (query) return { kind: "query", query };
  return { kind: "catalog" };
}

// The tool's text ends with its count (agent/session_tools_find.go's
// formatSessionFindings): "2 matches (scope: …)" after the listing, or "No
// matching sessions (scope: …)." alone. Only that last line is read, so a
// session titled like a count ("3 matches in the parser") never reads as one;
// the registry may append an intervention after it.
const MATCH_COUNT_RE = /^(\d+) match(?:es)? \(scope: [^)]*\)$/;
const NO_MATCHES_RE = /^No matching sessions \(scope: [^)]*\)\.$/;

function findSessionsCount(output: string | undefined): number | undefined {
  if (!output) return undefined;
  for (const tail of outputTails(output)) {
    const last = lastLine(tail.trimEnd());
    if (NO_MATCHES_RE.test(last)) return 0;
    const found = MATCH_COUNT_RE.exec(last)?.[1];
    if (found !== undefined) return Number(found);
  }
  return undefined;
}

/** 'Searched sessions for "settle" · 2 matches', "Listed recent sessions · 1 session". */
export function findSessionsWords(step: TranscriptStep): StepWords {
  const search = sessionsSearch(step);
  const lead: StepWords =
    search.kind === "children"
      ? { verb: "Searched sessions spawned by", target: search.ref }
      : search.kind === "query"
        ? { verb: "Searched sessions for", target: `"${clip(search.query, 60)}"` }
        : { verb: "Listed recent sessions" };
  const count = findSessionsCount(step.output);
  if (count === undefined) return lead;
  // A listing reports sessions; a search, matches.
  const noun = search.kind === "catalog" ? (count === 1 ? "session" : "sessions") : count === 1 ? "match" : "matches";
  return { ...lead, detail: `${count} ${noun}` };
}

/** The search's words as one line. */
export function findSessionsSummary(step: TranscriptStep): string {
  return composeStepWords(findSessionsWords(step));
}

/** 'Searching sessions for "settle"', "Listing recent sessions", while it runs. */
export function findSessionsProgress(step: Pick<TranscriptStep, "argumentsJSON">): string {
  const search = sessionsSearch(step);
  if (search.kind === "children") return `Searching sessions spawned by ${search.ref}`;
  if (search.kind === "query") return `Searching sessions for "${clip(search.query, 60)}"`;
  return "Listing recent sessions";
}
