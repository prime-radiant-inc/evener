import type { TurnModel } from "@evener/appwire-client";
import { useTranscriptRenderContext } from "../../../transcriptDisplay/renderContext";
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
  const { metadata } = useTranscriptRenderContext();
  const durationMs = turn.durationMs;
  if (durationMs === undefined) return null;
  let toolCalls = 0;
  let hasAgentOutput = false;
  for (const item of turn.items) {
    if ((item.toolName ?? "") !== "") {
      toolCalls += 1;
      hasAgentOutput = true;
    } else if (item.type === "agentMessage") {
      hasAgentOutput = true;
    }
  }
  if (!hasAgentOutput) return null;
  return (
    <div className={CLASS.cap} data-testid="turn-done" data-turn-duration-ms={durationMs}>
      <span>{durationMs > 0 && !metadata.roundTimings ? `Done in ${formatWorkDuration(durationMs)}` : "Done"}</span>
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
