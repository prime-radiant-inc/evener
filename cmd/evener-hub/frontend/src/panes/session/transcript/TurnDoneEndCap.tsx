import type { TurnModel } from "@evener/appwire-client";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatWorkDuration } from "../chrome/statusFormat";
import styles from "./turndone.module.css";

const CLASS = {
  cap: requireClass(styles.cap, "turndone.module.css", "cap"),
  separator: requireClass(styles.separator, "turndone.module.css", "separator"),
};

/** The quiet factual close of a settled turn: how long it took, and how many
 * tool calls it carries. Both come from the turn itself, so the cap never
 * invents a summary or a success the wire did not report. Finished and
 * interrupted turns therefore stop looking identical without a second
 * status surface. */
export function TurnDoneEndCap({ turn }: { turn: TurnModel }) {
  const durationMs = turn.durationMs;
  if (durationMs === undefined || durationMs <= 0) return null;
  const hasAgentOutput = turn.items.some((item) => item.type === "agentMessage" || (item.toolName ?? "") !== "");
  if (!hasAgentOutput) return null;
  const toolCalls = turn.items.filter((item) => (item.toolName ?? "") !== "").length;
  return (
    <div className={CLASS.cap} data-testid="turn-done" data-turn-duration-ms={durationMs}>
      <span>Done in {formatWorkDuration(durationMs)}</span>
      {toolCalls > 0 && (
        <>
          <span className={CLASS.separator} aria-hidden="true">
            ·
          </span>
          <span>
            {toolCalls} {toolCalls === 1 ? "tool call" : "tool calls"}
          </span>
        </>
      )}
    </div>
  );
}
