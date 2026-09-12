// use_skill descriptor (parity checklist §2's useSkillRenderer). Ground
// truth: agent/session_tools_communicate.go's Exec returns
// "Skill: %s\nLocation: %s\n\n---\n\n%s" as plain output text (not a
// StateResult, so no raw/tool_state either); internal/appprojector's own
// skill-activation race note documents one edge case where the completed
// item's Output can be left empty even though the skill did activate - the
// legacy useSkillRenderer's own "body hidden when nothing to show" rule
// still applies cleanly here since it keys off the same signal (blank
// text), not the tool_state field legacy actually read.

import type { ItemModel } from "../../../../protocol/model";
import { Markdown } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { parseArgs, str } from "./helpers";

function UseSkillBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <Markdown source={output} />;
}

registerToolRenderer({
  match: "use_skill",
  fold: "never", // which skill is running is never a footnote
  icon: "skill",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const skillName = str(args, "skill_name") ?? str(args, "name") ?? "";
    return `Activated skill: ${skillName}`;
  },
  body: UseSkillBody,
});
