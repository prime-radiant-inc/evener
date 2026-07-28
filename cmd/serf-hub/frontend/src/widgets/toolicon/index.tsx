// The transcript row-kind icon: one small line-art glyph per tool FAMILY,
// plus the thinking row's lightbulb, drawn on the app's 16x16 icon grid in
// the same grammar as widgets/chevron (stroke currentColor, 1.75 width,
// round caps/joins, fill none, square block box) so every icon in the app
// reads as one family and a consumer's own ink token governs the colour.
//
// Why a widget at all: a tool row's summary is machine text ("Ran git merge
// ...", "Read src/app.ts"), and the row's KIND - shell vs file read vs edit
// vs web fetch vs thought - was only recoverable by reading that text. A
// recognisable leading glyph makes the kind scannable down a long transcript,
// which is the reading it optimises for (an audit, not a narrative).
//
// The tool kinds are per FAMILY, not per tool name: the registry maps each
// descriptor to one kind (toolRenderers.ts's `icon` field), and every
// unregistered tool - which includes every MCP tool - falls through to the
// DEFAULT_DESCRIPTOR's generic `wrench`. A kind therefore names a shape of
// work (a terminal, a document, a search), never a brand.
//
// Single-colour line art by construction: strokes only, no fills, no
// hardcoded colours. The squareness guarantee is the same one chevron makes
// in its own file header - this widget's test asserts it here rather than
// trusting each caller.

export type ToolIconKind =
  | "terminal"
  | "file"
  | "edit"
  | "search"
  | "folder"
  | "globe"
  | "ask"
  | "tasks"
  | "delegate"
  | "transcript"
  | "job"
  | "send"
  | "skill"
  | "wrench"
  | "thought";

// One path string per kind, all on the 16x16 grid. Subpaths within a kind are
// space-separated M-commands in the same string, so the record stays one
// entry per glyph.
const PATHS: Record<ToolIconKind, string> = {
  // A terminal frame with a `>_` prompt.
  terminal: "M2.5 3.5 H13.5 V12.5 H2.5 Z M5 6.8 L7.5 8.8 L5 10.8 M8.6 10.8 H11.2",
  // A document with a folded corner.
  file: "M4 2.5 H9.5 L12 5 V13.5 H4 Z M9.5 2.5 V5 H12",
  // A pencil, tip at lower left.
  edit: "M3.2 12.8 L3.9 10.2 L10.6 3.5 A1.5 1.5 0 0 1 12.7 5.6 L6 12.3 L3.2 12.8 Z",
  // A magnifier.
  search: "M11 7 A4 4 0 1 1 3 7 A4 4 0 1 1 11 7 M9.9 9.9 L13.5 13.5",
  // A folder with a tab.
  folder: "M2.5 3.5 H6 L7.5 5.5 H13.5 V12.5 H2.5 Z",
  // A globe: circle, equator, two meridians.
  globe:
    "M13.5 8 A5.5 5.5 0 1 1 2.5 8 A5.5 5.5 0 1 1 13.5 8 M2.7 8 H13.3 M8 2.6 C6.1 5 6.1 11 8 13.4 M8 2.6 C9.9 5 9.9 11 8 13.4",
  // A speech bubble with a question mark (ask_user).
  ask: "M2.5 3.5 H13.5 V10.5 H7.2 L4.2 13 V10.5 H2.5 Z M7 6.1 A1.1 1.1 0 1 1 8.5 7.2 C8.1 7.5 8 7.8 8 8.3 M8 9.3 L8.01 9.3",
  // A checklist: three ticks beside three lines (task_list).
  tasks:
    "M2.8 4.2 L3.8 5.2 L5.6 3.4 M2.8 8.2 L3.8 9.2 L5.6 7.4 M2.8 12.2 L3.8 13.2 L5.6 11.4 M7.5 4.2 H13.5 M7.5 8.2 H13.5 M7.5 12.2 H13.5",
  // A fork: one source node branching to two (delegate/subagent).
  delegate:
    "M4.5 4 A1.5 1.5 0 1 1 1.5 4 A1.5 1.5 0 1 1 4.5 4 M14.5 3.5 A1.5 1.5 0 1 1 11.5 3.5 A1.5 1.5 0 1 1 14.5 3.5 M14.5 12.5 A1.5 1.5 0 1 1 11.5 12.5 A1.5 1.5 0 1 1 14.5 12.5 M5.8 3.4 C8 2.6 9.6 2.8 11.4 3.3 M5.8 4.6 C8 6.4 9.6 9.8 11.4 11.7",
  // A document with text lines (read_transcript family).
  transcript: "M4 2.5 H12 V13.5 H4 Z M6 5.5 H10 M6 8 H10 M6 10.5 H9",
  // A printer (the job_* family: job -> print job): sheet-feed slot, body,
  // page emerging below. The most silhouette-dense glyph in the set, but
  // unambiguous at 14px where an abstract "queue" stack was not.
  job: "M2.5 2.8 H13.5 V5.6 H2.5 Z M2.5 6.9 H10.5 V9.7 H2.5 Z M2.5 11 H8 V13.8 H2.5 Z",
  // A paper plane (delegate_send / job_send_message).
  send: "M2.5 8 L13.5 2.5 L10 13.5 L7.8 9.7 Z M13.5 2.5 L7.8 9.7",
  // A four-point sparkle (use_skill).
  skill: "M8 2 L9.4 6.6 L14 8 L9.4 9.4 L8 14 L6.6 9.4 L2 8 L6.6 6.6 Z",
  // The generic tool: an open-end wrench (the DEFAULT_DESCRIPTOR's glyph -
  // every unregistered tool, which includes every MCP tool, wears this one).
  // Lucide's wrench path scaled 24 -> 16 (x2/3), radii scaled with it.
  wrench:
    "M9.8 4.2 A0.67 0.67 0 0 0 9.8 5.13 L10.87 6.2 A0.67 0.67 0 0 0 11.8 6.2 L14.31 3.69 A4 4 0 0 1 9.02 8.98 L4.41 13.59 A1.41 1.41 0 0 1 2.41 11.59 L7.02 6.98 A4 4 0 0 1 12.31 1.69 L9.8 4.2 Z",
  // A lightbulb (the thinking row): bulb narrowing into the neck, two base
  // lines below. Not a tool - the reasoning item leads with this so a
  // thought is scannable as a kind alongside the tool calls it precedes.
  thought:
    "M8 2.5 A3.8 3.8 0 0 0 5.8 9.1 C6.4 9.8 6.6 10.4 6.6 11 L6.6 11.6 H9.4 L9.4 11 C9.4 10.4 9.6 9.8 10.2 9.1 A3.8 3.8 0 0 0 8 2.5 Z M6.6 13.2 H9.4 M7.1 14.7 H8.9",
};

export interface ToolIconProps {
  kind: ToolIconKind;
  /** Box edge in px. Square by construction - see this widget's own test. */
  size?: number;
}

// 14px: the same edge Chevron, CloseIcon and BackIcon use, so every icon in
// the app occupies one box size unless it has a reason not to.
const DEFAULT_SIZE = 14;

export function ToolIcon({ kind, size = DEFAULT_SIZE }: ToolIconProps) {
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
      // Inline rather than a class (same rationale as widgets/chevron):
      // `display` here is correctness - an inline SVG would sit in a line box
      // taller than itself, undoing the square box - not styling a consumer
      // should be able to override.
      style={{ display: "block" }}
    >
      <path
        d={PATHS[kind]}
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}
