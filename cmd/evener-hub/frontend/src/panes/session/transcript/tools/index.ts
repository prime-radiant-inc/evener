// Side-effect barrel: importing this module registers every T3 tool
// descriptor (registerToolRenderer, toolRenderers.ts) this directory
// ships. Mirrors TurnBlock.tsx's own precedent for ToolCallItem
// ("registers ToolCallItem for commandExecution items the moment
// TurnBlock itself is ever imported, regardless of what else the app
// happens to have loaded - the real SessionPane composition must never
// depend on import ORDER to get tool calls rendered correctly") - the
// same principle applies here: wherever this module is imported from
// (see the wave-4 task-3 report for the exact integration line), every
// descriptor below registers exactly once, regardless of import order
// relative to anything else.
//
// Not included here (no registerToolRenderer call of their own):
// helpers.ts, bodies.tsx (shared building blocks), subagentModuleStore.ts
// (state only), sandboxEscalation.tsx (thread-level, no
// registerToolRenderer integration point exists for it at all - see its
// own file header for why, and the wave-4 task-3 report for the
// integration handoff this instead needs).
import "./fsTools";
import "./shellTool";
import "./editTools";
import "./webTools";
import "./useSkillTool";
import "./jobTools";
import "./jobWatch";
import "./subagentModule";
import "./askUser";
import "./taskCard";
import "./readTranscript";
import "./worktreeTool";
