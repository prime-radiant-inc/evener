// use_skill descriptor (parity checklist §2's useSkillRenderer). Ground
// truth: agent/session_tools_communicate.go's Exec returns
// "Skill: %s\nLocation: %s\n\n---\n\n%s" as plain output text (not a
// StateResult, so no raw/tool_state either); internal/appprojector's own
// skill-activation race note documents one edge case where the completed
// item's Output can be left empty even though the skill did activate - the
// legacy useSkillRenderer's own "body hidden when nothing to show" rule
// still applies cleanly here since it keys off the same signal (blank
// text), not the tool_state field legacy actually read.

import { skillContext, useSkillSummary } from "@evener/appwire-client";
import { Markdown } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";

// The tool returns a <skill-context> block of JSON; the body shows the
// skill's instructions from it (@evener/appwire-client's skillContext, which
// the phone reads too). An output in any other shape shows as it is.
function UseSkillBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <Markdown source={skillContext(output)?.instructions ?? output} />;
}

registerToolRenderer({
  match: "use_skill",
  fold: "never", // which skill is running is never a footnote
  icon: "skill",
  summary: useSkillSummary,
  body: UseSkillBody,
});
