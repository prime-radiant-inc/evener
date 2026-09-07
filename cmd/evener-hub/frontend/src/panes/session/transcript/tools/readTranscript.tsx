// The read_transcript / read_session_transcript descriptor.
//
// Ground truth (read directly from the Go, not assumed):
//   - The tool name is read_transcript (agent/internal/tool/definitions.go's
//     DefReadTranscript). Args: transcript_ref, format ("outline"|"markdown"|
//     "jsonl", markdown default), range, expand_turn, offset_bytes, max_bytes.
//     A "job:<job_id>" ref reads a shell job's output log instead of a
//     conversation, and then range/expand_turn/offset_bytes/max_bytes are
//     rejected outright (agent/session_tools_transcript.go's
//     execReadTranscript).
//   - Its result is a struct, so the tool registry JSON-marshals it
//     (agent/internal/tool/registry.go's toolValueToString) - item.output is
//     real, parseable JSON, unlike most tools here. Three envelope shapes:
//       markdown/job -> readMarkdownEnvelope {transcript_ref, format,
//           content_type, content, meta{turns_total, range, turns_rendered,
//           truncated, elided_turns, ...}, expansion?, continuation?}
//       outline      -> readOutlineEnvelope, FLAT (no meta block):
//           {transcript_ref, format, turns_total, content, truncated,
//            elided_turns, hint}
//       jsonl        -> readRawEnvelope {..., content, meta{lines_returned,
//           truncated, ...}}
//
// read_session_transcript SHARES this descriptor: it is the same reader with
// the archive-only arguments (source=api_log, attempt_id, body) and returns the
// same three envelopes (agent/session_tools_transcript.go routes both through
// execReadSessionTranscript). find_session_transcripts does NOT: it returns a
// findSessionsEnvelope of match RECORDS (agent/session_tools_find.go), a list,
// not a transcript - a different renderer's job. It has its own descriptor
// (worktreeTool.tsx's findSessionsSummary registration), not this one.

import type { ItemModel } from "../../../../protocol/model";
import { CodeBlock } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { MCPToolArguments } from "../MCPToolArguments";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { clip, clipJobID, parseArgs, parseJSONObject, str } from "./helpers";
import styles from "./readtranscript.module.css";

const CLASS = {
  elision: requireClass(styles.elision, "readtranscript.module.css", "elision"),
};

const CONTENT_MAX_CHARS = 8000;

// A transcript_ref is "local:<ULID>" / "job:<job_id>" / a bare session id. The
// scheme prefix is machinery; the id is what a human recognizes.
function refId(ref: string): string {
  const colon = ref.indexOf(":");
  return colon === -1 ? ref : ref.slice(colon + 1);
}

interface Envelope {
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

// readEnvelope normalizes whichever of the three shapes came back. The outline
// envelope is flat and the markdown/jsonl ones nest their counts under `meta`,
// so both spellings are read; absence stays absence (never defaulted to 0,
// which would claim a count the tool never reported).
function readEnvelope(item: ItemModel): Envelope | undefined {
  const parsed = parseJSONObject(item.output);
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

// What was read, in the reader's terms: a job's output log, an API-log record,
// or a session conversation - and whose.
// resolvedRef is the ref this call actually read: the caller's own argument
// first, the envelope's echo of it as the fallback (a hydrated item whose args
// were dropped still has the envelope).
function resolvedRef(item: ItemModel): string {
  const fromArgs = str(parseArgs(item.argumentsJSON), "transcript_ref")?.trim();
  if (fromArgs !== undefined && fromArgs !== "") return fromArgs;
  return readEnvelope(item)?.ref ?? "";
}

// A "job:<job_id>" ref reads a shell job's output LOG, not a conversation - a
// different kind of thing, and the one case with no turns at all.
function isJobRead(item: ItemModel): boolean {
  return resolvedRef(item).startsWith("job:");
}

function target(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  const ref = resolvedRef(item);
  if (ref.startsWith("job:")) return `job log ${clipJobID(refId(ref))}`;
  if (str(args, "source") === "api_log") return `API log ${refId(ref)}`;
  // An absent/"current" ref means the session the agent is already in.
  if (ref === "" || ref === "current") return "this session's transcript";
  return `transcript ${refId(ref)}`;
}

// How much was read - the honest span, straight off the envelope. Absent when
// the call is still live (no envelope yet) or the output wasn't the JSON this
// tool documents.
function extent(item: ItemModel): string | undefined {
  const envelope = readEnvelope(item);
  if (!envelope) return undefined;
  if (envelope.expandTurn !== undefined) return `turn ${envelope.expandTurn} in full`;
  // A job log has no turns: readJobTranscript hardcodes turns_total/
  // turns_rendered to 1 and range to "shell-log" (agent/
  // session_tools_transcript.go), so reporting "all 1 turns" for a shell output
  // log would be describing an artifact of the envelope, not the read.
  if (isJobRead(item)) return undefined;
  const total = envelope.turnsTotal;
  if (total === undefined) return undefined;
  const rendered = envelope.turnsRendered;
  if (rendered === undefined) return `outline of ${total} turns`;
  return rendered >= total ? `all ${total} turns` : `${rendered} of ${total} turns`;
}

function ReadTranscriptBody(props: ToolRenderProps) {
  const { item } = props;
  const output = item.output ?? "";
  if (output === "") return null;
  const envelope = readEnvelope(item);
  // Not parseable JSON: show what actually came back rather than an empty
  // block - the envelope shape is documented, not guaranteed by this client.
  if (!envelope?.content)
    return (
      <>
        <MCPToolArguments {...props} />
        <CodeBlock text={output} copyLabel="Copy output" />
      </>
    );
  const elided = envelope.elidedTurns ?? 0;
  return (
    <div>
      <MCPToolArguments {...props} />
      {elided > 0 && (
        <div className={CLASS.elision} data-testid="read-transcript-elision">
          {elided} turn{elided === 1 ? "" : "s"} elided by the read's own budget
        </div>
      )}
      <CodeBlock text={clip(envelope.content, CONTENT_MAX_CHARS)} copyLabel="Copy transcript" />
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "read_transcript" || name === "read_session_transcript",
  icon: "transcript",
  fold: "quiet",
  // Says what was read and how much, never a dump of the call.
  summary(item: ItemModel) {
    const how = extent(item);
    return how === undefined ? `Read ${target(item)}` : `Read ${target(item)} · ${how}`;
  },
  body: ReadTranscriptBody,
});
