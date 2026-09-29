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

import { clip, readTranscriptEnvelope, readTranscriptSummary } from "@evener/appwire-client";
import { CodeBlock } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { MCPToolArguments } from "../MCPToolArguments";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import styles from "./readtranscript.module.css";

const CLASS = {
  elision: requireClass(styles.elision, "readtranscript.module.css", "elision"),
};

const CONTENT_MAX_CHARS = 8000;

function ReadTranscriptBody(props: ToolRenderProps) {
  const { item } = props;
  const output = item.output ?? "";
  if (output === "") return null;
  const envelope = readTranscriptEnvelope(item);
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
  summary: readTranscriptSummary,
  body: ReadTranscriptBody,
});
