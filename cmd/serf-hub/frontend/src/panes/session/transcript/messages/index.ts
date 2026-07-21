// Wave 4 T2 side-effect module: importing this file registers every
// message item renderer built in this stream (agentMessage/userMessage/
// steering/systemMessage/reasoning) via registerItemRenderer, mirroring
// TurnBlock.tsx's own `import "./ToolCallItem"` pattern for
// commandExecution. SessionPane.tsx (T1-owned) needs one import of this
// module added at merge time - see the wave-4 T2 report for the exact line.
import "./AgentMessageItem";
import "./UserMessageItem";
import "./SteeringItem";
import "./SystemNoticeItem";
import "./ThinkBlock";
import "./WarningItem";

// TurnSeparator is NOT an item renderer (it takes a TurnModel, not an
// ItemModel) so it has no registerItemRenderer call to run as a side
// effect - it's re-exported here so the controller has one place to import
// both "the registrations" and "the one thing that still needs manual
// wiring" from. See TurnSeparator.tsx's own comment and the wave-4 T2
// report for the TurnBlock.tsx wiring this still needs.
export { TurnSeparator } from "./TurnSeparator";
