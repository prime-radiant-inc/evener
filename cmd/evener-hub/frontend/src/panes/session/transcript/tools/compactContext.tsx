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
// with it, so the body renders it only when the row shows no stated intent
// of its own.

import { clip, parseArgs, str, toolStepSummary } from "@evener/appwire-client";
import { CodeBlock, Markdown } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
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

// What the pretty blocks did not consume: keys outside the tool's schema,
// and known keys whose values came in the wrong type (a non-string note or
// instructions, a reload_skills that is not an array of strings). Rendered
// as one raw JSON block so the body is never less honest than the default
// renderer's raw dump - nothing is silently dropped.
function leftoverArgs(args: Record<string, unknown>, consumed: ReadonlySet<string>): Record<string, unknown> {
  const leftovers: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(args)) {
    if (consumed.has(key) || value === undefined || value === null) continue;
    leftovers[key] = value;
  }
  return leftovers;
}

function CompactContextBody({ item }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const note = str(args, "note_to_self");
  const instructions = str(args, "compaction_instructions");
  const skills = reloadSkillsOf(args);
  const intent = str(args, "intent");
  const statedIntent = item.description?.trim();

  const consumed = new Set<string>();
  if (note !== undefined) consumed.add("note_to_self");
  if (instructions !== undefined) consumed.add("compaction_instructions");
  if (skills !== undefined) consumed.add("reload_skills");
  // The intent key is consumed whether its value rendered here or already
  // rides the row's intent line - either way it must not also land in the
  // additional-arguments block. A wrong-typed (non-string) intent stays
  // unconsumed and surfaces there.
  if (intent !== undefined) consumed.add("intent");
  const leftovers = leftoverArgs(args, consumed);

  return (
    <div>
      {intent !== undefined && intent.trim() !== "" && statedIntent === undefined && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Intent</div>
          <Markdown source={clip(intent, COMPACT_TEXT_MAX_CHARS)} />
        </div>
      )}
      {note !== undefined && note.trim() !== "" && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Note to self</div>
          <Markdown source={clip(note, COMPACT_TEXT_MAX_CHARS)} />
        </div>
      )}
      {instructions !== undefined && instructions.trim() !== "" && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Compaction instructions</div>
          <Markdown source={clip(instructions, COMPACT_TEXT_MAX_CHARS)} />
        </div>
      )}
      {skills !== undefined && (
        <div className={CLASS.field}>
          <div className={CLASS.label}>Skills to reload</div>
          <div className={CLASS.skills}>{skills.length > 0 ? skills.join(", ") : "None."}</div>
        </div>
      )}
      {Object.keys(leftovers).length > 0 && (
        <section aria-label="Additional arguments">
          <CodeBlock text={JSON.stringify(leftovers, null, 2)} copyLabel="Copy arguments" fold={false} />
        </section>
      )}
      {item.output !== undefined && item.output !== "" && <div>{item.output}</div>}
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
});
