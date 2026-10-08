// compact_context descriptor: the agent's request to fold its own older
// history into a summary checkpoint (agent/session_tools_compact.go). The
// row's summary stays the shared housekeeping words
// (appwire-client/typescript/housekeepingSteps.ts) so the web row, the
// native tray and run lines keep saying the same thing; this descriptor
// owns only the expanded body, which pretty-prints the request's own fields
// where the raw default renderer dumped them as JSON. Field ground truth:
// note_to_self, compaction_instructions and the reload_skills selection come
// from the tool's schema; `intent` is the shared work-tool argument the
// registry injects into every registered tool
// (agent/internal/tool/registry.go's WithIntentParameter) which the session
// promotes into the call event's Description at emit
// (agent/session_tools.go's toolStartDescription) - the row already leads
// with it, so the body renders it only when the row is not already showing
// it.

import type { ItemModel } from "@evener/appwire-client";
import { clip, parseArgs, str, toolStepSummary } from "@evener/appwire-client";
import { CodeBlock, Markdown } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { statedIntentOf } from "../ToolRow";
import { registerToolRenderer, type ToolRenderProps } from "../toolRenderers";
import styles from "./compactcontext.module.css";

const CLASS = {
  field: requireClass(styles.field, "compactcontext.module.css", "field"),
  label: requireClass(styles.label, "compactcontext.module.css", "label"),
  skills: requireClass(styles.skills, "compactcontext.module.css", "skills"),
};

// The same clip budget memory_write's content gets (memoryTools.tsx): the
// note and instructions are authored prose that can run long in a legacy
// transcript, and Markdown handles the rest.
const COMPACT_TEXT_MAX_CHARS = 8000;

// The reload_skills selection, or undefined when it renders nothing here:
// absent and null both mean "no selection" (the tool distinguishes a
// present-but-empty array, which asks for a compaction and reloads none,
// from an absent/null selection - session_tools_compact.go), and a
// wrong-typed value falls through to the additional-arguments block.
function reloadSkillsOf(args: Record<string, unknown>): string[] | undefined {
  const raw = args.reload_skills;
  if (raw === undefined || raw === null) return undefined;
  if (!Array.isArray(raw) || !raw.every((name) => typeof name === "string")) return undefined;
  return raw;
}

// What the body renders for one item, decided once: the body and the
// descriptor's hasBody answer from the same rules, so a row never offers a
// disclosure that opens to nothing.
interface CompactContextParts {
  intentText: string | undefined;
  noteText: string | undefined;
  instructionsText: string | undefined;
  skills: string[] | undefined;
  leftovers: Record<string, unknown>;
  output: string | undefined;
}

function compactContextParts(item: Pick<ItemModel, "argumentsJSON" | "description" | "output">): CompactContextParts {
  const args = parseArgs(item.argumentsJSON);
  const note = str(args, "note_to_self");
  const instructions = str(args, "compaction_instructions");
  const skills = reloadSkillsOf(args);
  const intent = str(args, "intent");

  // The row renders the stated intent by the row's own rule (ToolRow's
  // statedIntentOf: a blank description means no stated intent). The body
  // renders the intent argument only when the row is not already showing
  // it: an argument that differs from the stated intent, or a row with no
  // stated intent at all. Never duplicated, never dropped.
  const intentText =
    intent !== undefined && intent.trim() !== "" && intent.trim() !== statedIntentOf(item) ? intent : undefined;

  // Everything the pretty blocks did not consume: keys outside the tool's
  // schema, and known keys whose values came in the wrong type (a
  // non-string note or instructions, a reload_skills that is not an array
  // of strings). A null is absent-shaped rather than wrong-typed for
  // reload_skills alone (the tool's documented no-selection); any other
  // null-valued key lands here too, because the default renderer's raw dump
  // shows it and this body must never be less honest.
  const consumed = new Set<string>();
  if (note !== undefined) consumed.add("note_to_self");
  if (instructions !== undefined) consumed.add("compaction_instructions");
  if (skills !== undefined) consumed.add("reload_skills");
  // The intent key is consumed whether its value rendered here or already
  // rides the row's intent line - either way it must not also land in the
  // additional-arguments block. A wrong-typed (non-string) intent stays
  // unconsumed and surfaces there.
  if (intent !== undefined) consumed.add("intent");
  const leftovers: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(args)) {
    if (consumed.has(key) || (key === "reload_skills" && value === null)) continue;
    leftovers[key] = value;
  }

  // An empty-string note is the tool's clearing request and renders nothing;
  // a whitespace-only note is a real pinned note (session_tools_compact.go
  // pins any non-empty string), so it renders like any other.
  return {
    intentText,
    noteText: note !== undefined && note !== "" ? note : undefined,
    instructionsText: instructions !== undefined && instructions !== "" ? instructions : undefined,
    skills,
    leftovers,
    output: item.output !== undefined && item.output !== "" ? item.output : undefined,
  };
}

function CompactContextBody({ item }: ToolRenderProps) {
  const { intentText, noteText, instructionsText, skills, leftovers, output } = compactContextParts(item);
  return (
    <div>
      {noteText !== undefined && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Note to self</div>
          <Markdown source={clip(noteText, COMPACT_TEXT_MAX_CHARS)} />
        </div>
      )}
      {instructionsText !== undefined && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Compaction instructions</div>
          <Markdown source={clip(instructionsText, COMPACT_TEXT_MAX_CHARS)} />
        </div>
      )}
      {skills !== undefined && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Skills to reload</div>
          <div className={CLASS.skills}>{skills.length > 0 ? skills.join(", ") : "None."}</div>
        </div>
      )}
      {intentText !== undefined && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Intent</div>
          <Markdown source={clip(intentText, COMPACT_TEXT_MAX_CHARS)} />
        </div>
      )}
      {Object.keys(leftovers).length > 0 && (
        <section aria-label="Additional arguments">
          <CodeBlock text={JSON.stringify(leftovers, null, 2)} copyLabel="Copy arguments" fold={false} />
        </section>
      )}
      {output !== undefined && <div>{output}</div>}
    </div>
  );
}

registerToolRenderer({
  match: "compact_context",
  icon: "fold",
  // The shared housekeeping words (housekeepingSteps.ts) - exactly what the
  // raw default produced, so web, native and run lines never drift.
  summary: (item, ctx) => toolStepSummary(item, ctx),
  body: CompactContextBody,
  // A row whose body would render nothing offers no disclosure that opens
  // to nothing: the live note-clearing call (no fields, no leftovers, no
  // output yet). A settled call carries the tool's own sentence, so it
  // keeps the expandable body, and a failed call is never bodyless
  // (ToolCallItem).
  hasBody(item) {
    const { intentText, noteText, instructionsText, skills, leftovers, output } = compactContextParts(item);
    return Boolean(
      intentText !== undefined ||
        noteText !== undefined ||
        instructionsText !== undefined ||
        skills !== undefined ||
        Object.keys(leftovers).length > 0 ||
        output !== undefined,
    );
  },
});
