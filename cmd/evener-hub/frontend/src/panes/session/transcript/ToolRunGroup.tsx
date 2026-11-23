// One row standing in for a run of settled, uneventful tool calls (critique
// R9; toolRuns.ts decides what a run is). It is the shared Disclosure, so its
// open state lives in the shared disclosureStore: a reader's choice survives
// re-projection and the transcript's expand/collapse-all baselines reach it.
//
// The body is MOUNTED ONLY WHILE OPEN: a folded run exists to spare the
// reader (and the render) the rows it stands for, and a closed run that still
// mounted every tool body would spare neither. That is also why the folded
// entries carry no view anchors of their own - TurnBlock
// gives the whole run ONE anchor that lists its members, so a run is one position in the scroll
// coordinator's list whether it is open or closed rather than a set of
// anchors that appear and vanish with a click.
import type { ThreadModel, TurnModel } from "@evener/appwire-client";
import { scopedDisclosureId } from "@evener/appwire-client";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  type TranscriptRenderContextValue,
  useTranscriptRenderContext,
} from "../../../transcriptDisplay/renderContext";
import { Chevron, Disclosure } from "../../../widgets";
import { disclosureDefault } from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ToolCallItem } from "./ToolCallItem";
import { toolRendererFor } from "./toolRenderers";
import { runLabel, type ToolRun } from "./toolRuns";
import styles from "./toolrungroup.module.css";
import { threadFingerprintForItem } from "./types";

const CLASS = {
  summary: requireClass(styles.summary, "toolrungroup.module.css", "summary"),
  label: requireClass(styles.label, "toolrungroup.module.css", "label"),
  body: requireClass(styles.body, "toolrungroup.module.css", "body"),
};

export interface ToolRunGroupProps {
  run: ToolRun;
  /** The turn the folded calls belong to, threaded so an expanded row sees
   * exactly what it would have seen unfolded. */
  turn: TurnModel;
  sessionRef?: string;
  renderContext?: TranscriptRenderContextValue;
  thread?: ThreadModel;
}

export function ToolRunGroup({ run, turn, sessionRef, renderContext, thread }: ToolRunGroupProps) {
  const context = useTranscriptRenderContext();
  const { config } = context;
  const scope = disclosureScopeForSession(context, sessionRef);
  const disclosureKey = scopedDisclosureId(scope, run.id);
  // A reader who has asked for expanded details has asked to see the calls,
  // so the run opens with everything else at that level.
  const fallback = expandDetailsByDefault(config) || disclosureDefault(scope, run.id, false);
  return (
    <Disclosure
      id={disclosureKey}
      defaultOpen={fallback}
      data-testid="tool-run"
      summaryClassName={CLASS.summary}
      leading={(open) => <Chevron open={open} />}
      summary={<span className={CLASS.label}>{runLabel(run, toolRendererFor, { cwd: thread?.cwd })}</span>}
    >
      <div className={CLASS.body}>
        {run.entries.map((entry) => (
          <ToolCallItem
            key={entry.id}
            item={entry.item}
            turn={turn}
            // Every folded entry is a settled call by construction
            // (toolRuns.ts's foldable), so none of them is live.
            live={false}
            sessionRef={sessionRef}
            renderContext={renderContext ?? context}
            thread={thread}
            threadFingerprint={threadFingerprintForItem(
              entry.item,
              thread,
              toolRendererFor(entry.item.toolName ?? "").summarySuffix?.(entry.item, thread),
            )}
          />
        ))}
      </div>
    </Disclosure>
  );
}
