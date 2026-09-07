// The tool-renderer registry: one descriptor per tool name (or a job_*-style
// predicate family), keyed off ItemModel.toolName for "commandExecution"
// items. Wave 4 T1 ships the registry + the raw-output fallback; T3
// registers the real per-tool descriptors (read/grep/ls/glob/shell/diff/
// patch/web fetch+search/delegate/job_*/ask_user/sandbox escalation).
import { type ComponentType, createElement, Fragment } from "react";
import type { ItemModel, ThreadModel } from "../../../protocol/model";
import type { ToolIconKind } from "../../../widgets";
import { MCPToolArguments } from "./MCPToolArguments";
import { RawToolOutput } from "./RawToolOutput";

export interface ToolRenderProps {
  item: ItemModel;
  live: boolean;
  // The enclosing session's ref (ToolCallItem's own sessionRef prop,
  // threaded straight through) - undefined for the same reasons ItemRenderProps'
  // own sessionRef is (Session.tsx not yet in the render path, e.g. a future
  // caller that never sets it). The subagent-transcript body is the one
  // consumer today (subagentModule.tsx's openTranscript): a delegate row's
  // "Open transcript" link needs it to record which session to return to
  // (kata 0pzz - a subagent transcript is a child of a specific parent
  // session, and the pane it opens must be able to say so and offer a way
  // back, not just leave the reader to remember it).
  sessionRef?: string;
  /** Snapshot cwd supplied by the owning transcript render context. */
  cwd?: string;
}

// ToolSummaryContext carries render-path facts a descriptor's summary() may
// need but that ItemModel alone can't answer - the session cwd (shell's
// stripRedundantCd, so a habitual "cd <cwd> && " prefix reads as noise, not
// information) is the one case today. Optional throughout: every descriptor
// but shell's own ignores it.
export interface ToolSummaryContext {
  cwd?: string; // session working directory, when the render path knows it
}

export interface ToolRendererDescriptor {
  match: string | ((toolName: string) => boolean); // exact name or predicate (job_* family)
  summary(item: ItemModel, ctx?: ToolSummaryContext): string; // one-line intent-first summary
  // The tool-FAMILY glyph riding inline at the start of the row's tool-use
  // line (widgets/toolicon), so the kind of work - shell vs file read vs
  // edit vs web - is scannable down a run of calls without reading the
  // summary text. Optional: a descriptor without one renders no icon, and
  // every unregistered tool - including every MCP tool - inherits the
  // DEFAULT_DESCRIPTOR's generic wrench.
  icon?: ToolIconKind;
  // Fixed-width summary text. The summary face is sans by default (Jesse's
  // review call: fixed-width everywhere made every tool read like a
  // terminal); shell opts in because its summary IS a command.
  monoSummary?: boolean;
  // How this tool behaves when a settled turn's tool calls are folded into a
  // single run row (toolRuns.ts):
  //   "never"         - a card the reader must always see on its own: a
  //                     delegate, an ask, a task list, a skill, a job. It
  //                     breaks the run around it rather than joining one;
  //   "consequential" - a mutating step (an edit, a shell command, a
  //                     worktree change). It folds, and it is what a folded
  //                     run's summary names;
  //   "quiet"         - a read-only step (a read, a search, a web fetch). It
  //                     folds and only ever contributes to the count;
  //   unset           - does not fold. Folding is opt-in per descriptor: an
  //                     unregistered tool (every MCP tool inherits
  //                     DEFAULT_DESCRIPTOR) may have side effects the reader
  //                     must see, so it breaks a run exactly like "never".
  fold?: "never" | "quiet" | "consequential";
  body?: ComponentType<ToolRenderProps>; // expanded content; default raw output
  // outputImageSize sizes the generic output-images gallery ToolCallItem
  // renders after the body: undefined keeps the default 96px thumbnails,
  // "large" displays each image whole at up to 600px square. The one case
  // today is read_file, whose image reads are the call's output itself (and
  // whose body renders nothing for them - fsTools.tsx).
  outputImageSize?: "large";
  autoExpand?(item: ItemModel): boolean; // e.g. shell on nonzero exit
  // failed is a TOOL-SPECIFIC failure signal, OR'd with the generic one
  // ToolCallItem derives from ItemModel.error/status. It exists because a
  // clean tool RESULT can still report a failed action: a shell command that
  // ran and exited nonzero carries no ItemModel.error at all (the wire stamps
  // status "completed"), yet it is exactly what the reader needs the failure
  // glyph for. Mirrors the legacy renderer's own toolLooksGood, which likewise
  // treated a nonzero exit_code as not-good (renderer-format.js:593).
  failed?(item: ItemModel): boolean;
  // detail is secondary fact the row must keep REACHABLE without making it the
  // headline - rendered as the row's hover title. The shell exit code is the
  // motivating case (A2: "exit 1" stops being the failure signal, the glyph is).
  detail?(item: ItemModel): string | undefined;
  // suppress removes the whole tool-call row from the transcript when true -
  // no summary, no body, nothing (ToolCallItem renders null). Used for a
  // task_list `action:"view"` (a read that legacy renders nothing for) and a
  // malformed non-mutation call: the same "fully suppressed - no card, no
  // divider, no tool-call row" the legacy renderer applied. An errored call is
  // never suppressed here, so its error still surfaces via the generic
  // failed-row treatment in ToolCallItem.
  suppress?(item: ItemModel): boolean;
  // openBesidePath returns the single file path this tool card references (its
  // file arg), or undefined when it references none - the ONLY tools that opt in
  // are the single-file ones (read_file/edit_file/write_file, floor §3.7;
  // multi-target apply_patch and directory/pattern grep/ls/glob are excluded).
  // ToolCallItem turns a non-undefined path into an "open beside" affordance
  // (relativized against the session cwd, out-of-cwd gated - fileOpenBeside.tsx).
  openBesidePath?(item: ItemModel): string | undefined;
  // openBesideInline moves the "open beside" control from the END of the
  // summary to INLINE, right after the target text inside it - the one case
  // today is read_file, whose summary quotes the path verbatim ("Read <path>
  // · lines N-M"), so the control lands between the file name and the line
  // range it opens. Returns the COMPLETE PREFIX of summary()'s own text up
  // to and including the anchor (e.g. "Read " + the path) - ToolRow verifies
  // this with summary.startsWith(...), never searches for it, because a bare
  // substring (the path alone) is ambiguous whenever it also recurs
  // elsewhere in the summary (kata ledger #97: a file bare-named "lines"
  // collides with readLineRange's own literal "lines" meta text). Undefined
  // means no inline anchor; a value that isn't a
  // literal prefix of summary() falls back to the end placement (same
  // "never a dead anchor" contract as summaryLink).
  openBesideInline?(item: ItemModel): string | undefined;
  // openTranscriptRef returns the transcript ref of the child session this
  // tool call targets, or undefined when it targets none - the one case today
  // is delegate_send, whose raw state carries the messaged delegate's
  // transcript_ref. ToolCallItem turns a non-undefined ref into an
  // OpenTranscriptButton in the row's trailing slot (the same control delegate
  // rows use). A data field, not a ReactNode, for the same
  // reason as openBesidePath: the descriptor declares WHAT it targets,
  // ToolCallItem owns the control that opens it.
  openTranscriptRef?(item: ItemModel): string | undefined;
  // openTranscriptInline moves the "open transcript" control from the END of
  // the summary to INLINE, right after the target text inside it - the one
  // case today is delegate_send, whose summary names the delegate target
  // before the status meta ("Sent a message to delegate <id> · <status>"), so
  // the control lands between the delegate it opens and the running-state
  // words that describe it. Returns the COMPLETE PREFIX of summary()'s own
  // text up to and including the anchor (e.g. "Sent a message to delegate
  // <id>") - ToolRow verifies this with summary.startsWith(...), never
  // searches for it, following the openBesideInline contract. Undefined means
  // no inline anchor; a value that isn't a literal prefix of summary() falls
  // back to the end placement (the same "never a dead anchor" contract as
  // summaryLink).
  openTranscriptInline?(item: ItemModel): string | undefined;
  // summarySuffix appends extra text to the collapsed row's summary, computed
  // from the FULL thread model rather than just this item - the one case
  // today is ask_user's "— answered: ..." recap (kata h70z), which lives in
  // a LATER, separate userMessage item this item alone can't see. Undefined
  // (every other tool) means no suffix. ToolCallItem re-derives this
  // reactively off a live model subscription, so a settled row's summary
  // updates the moment the answer arrives without item itself needing a new
  // identity.
  summarySuffix?(item: ItemModel, model: ThreadModel | undefined): string | undefined;
  // summaryHiddenWhenExpanded drops the row's summary line while the row is
  // open. The one case today is shell: its summary IS the raw one-line
  // command, and the expanded body already renders that same command
  // pretty-printed (ShellCommandBlock), so an open row would show the call
  // twice - the collapsed row keeps the summary, where it is the only glance
  // at the command. Undefined (every other tool) renders the summary in both
  // states, as before.
  summaryHiddenWhenExpanded?: boolean;
  // summaryLink, if present, is a URL that appears verbatim inside this
  // row's own summary() text and should render as a real, clickable link
  // rather than plain text - kata xw3t, the collapsed-row counterpart to
  // tcp9's expanded-body link (web_fetch's "Fetched <url> · N bytes"
  // collapsed-row summary was the one surface tcp9 deliberately left inert;
  // see that field's own kata for why). A parallel field rather than
  // widening summary()'s own return type to ReactNode: summary is ALSO
  // consumed as a plain string by summarySuffix's own concatenation above
  // and ToolRow's collapsed-state truncation (middleSplit) operates on summary
  // as raw characters, not markup - widening the whole contract would touch
  // every one of those for a link only one descriptor has today. Undefined (every
  // descriptor but web_fetch) renders the row exactly as before. When the
  // returned URL is not literally found inside summary(item)'s own text,
  // ToolRow renders the plain text unchanged - never a link pointing
  // somewhere the visible words don't say.
  summaryLink?(item: ItemModel): string | undefined;
}

// A tool call is failed when the wire carries an error/status failure or when
// the tool descriptor has a domain-specific failure signal. Shell's nonzero
// exit code is the important example: the command completed as a tool result,
// but the result itself still failed. Keeping this predicate beside the
// descriptor registry lets ToolCallItem and transcript grouping agree without
// duplicating the registry-specific rule.
export function toolCallFailed(item: ItemModel): boolean {
  const descriptor = toolRendererFor(item.toolName ?? "");
  return (
    (item.error !== undefined && item.error !== "") || item.status === "failed" || (descriptor.failed?.(item) ?? false)
  );
}

const registry: ToolRendererDescriptor[] = [];

export function registerToolRenderer(d: ToolRendererDescriptor): void {
  registry.push(d);
}

// DEFAULT_DESCRIPTOR mirrors legacy renderer-tools.js's own
// toolRenderers.__default__ / defaultRenderer (renderer-tools.js:773-778):
// used for any tool name with no registered descriptor.
const DEFAULT_DESCRIPTOR: ToolRendererDescriptor = {
  match: () => true,
  summary: (item) => item.toolName ?? "tool",
  // The generic tool glyph: MCP tools (and any other unregistered name) have
  // no family descriptor, so they all wear the wrench.
  icon: "wrench",
  body: defaultToolBody,
};

export function defaultToolBody(props: ToolRenderProps) {
  return createElement(Fragment, null, createElement(MCPToolArguments, props), createElement(RawToolOutput, props));
}

// toolRendererFor mirrors legacy renderer-tools.js's own toolRendererFor
// name (renderer-tools.js:17-19). Exact-string matches are checked across
// the WHOLE registry before any predicate, so a specific descriptor
// (e.g. "job_stop") always wins over a broader family predicate
// (e.g. name.startsWith("job_")) regardless of registration order; among
// same-kind matches (two predicates, or two identical exact strings), the
// first-registered one wins. Falls back to DEFAULT_DESCRIPTOR, never throws
// - an unregistered tool name is an everyday case (most tools have no
// dedicated descriptor until T3 lands), not a bug.
export function toolRendererFor(toolName: string): ToolRendererDescriptor {
  const exact = registry.find((d) => d.match === toolName);
  if (exact) return exact;
  const predicate = registry.find((d) => typeof d.match === "function" && d.match(toolName));
  return predicate ?? DEFAULT_DESCRIPTOR;
}
