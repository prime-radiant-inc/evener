import { useState } from "react";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { useThreadsStore } from "../../../stores/threads";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ToolCallItem } from "./ToolCallItem";
import { ToolRow } from "./ToolRow";
import { toolRendererFor } from "./toolRenderers";
import { consequenceRank } from "./tools/consequenceRank";
import styles from "./turnblock.module.css";

interface ToolCallClusterProps {
  items: ItemModel[];
  turn: TurnModel;
  sessionRef?: string;
}

const CLASS = {
  cluster: requireClass(styles.cluster, "turnblock.module.css", "cluster"),
  body: requireClass(styles.clusterBody, "turnblock.module.css", "clusterBody"),
};

function leadItem(items: ItemModel[]): ItemModel {
  return items.reduce((best, item) => (consequenceRank(item) > consequenceRank(best) ? item : best));
}

// The collapsed header a folded run shows before anyone opens it: the step
// count plus the LEAD call's own summary text - and, when that lead carries
// one, the same summaryLink its per-call row uses (kata xw3t), so a
// web_fetch-led run's URL is a real link here too rather than dead text the
// reader has to unfold the run to click (kata 79cs). Text and link come from
// ONE descriptor lookup of ONE item, because they must always describe the
// same call. This row never passes a `purpose`, so ToolRow always renders the
// summary in full - the clamped head/tail split that keeps xw3t's per-call
// row plain text while collapsed cannot arise here.
function clusterHeader(
  items: ItemModel[],
  cwd: string | undefined,
): { summary: string; summaryLink: string | undefined } {
  const lead = leadItem(items);
  const descriptor = toolRendererFor(lead.toolName ?? "");
  return {
    summary: `${items.length} steps · ${descriptor.summary(lead, { cwd })}`,
    summaryLink: descriptor.summaryLink?.(lead),
  };
}

// A cluster has its own local disclosure because it is a derived presentation
// of a current adjacent run, not a durable per-item disclosure. The existing
// dynamic VirtualList measures the row after this native details body changes
// height; no viewport or scroll state belongs in this component.
export function ToolCallCluster({ items, turn, sessionRef }: ToolCallClusterProps) {
  const [open, setOpen] = useState(false);
  // Same by-ref selector ToolCallItem.tsx's own summaryCwd/openBesideCwd use
  // (copied from fileOpenBeside.tsx) - snapshot-only ThreadModel state, so a
  // shell-led folded cluster's header strips its redundant "cd <cwd> && "
  // prefix exactly like the per-call row does.
  const cwd = useThreadsStore((s) => (sessionRef !== undefined ? s.threads.get(sessionRef)?.cwd : undefined));
  const header = clusterHeader(items, cwd);
  return (
    <details className={CLASS.cluster} data-testid="tool-call-cluster" open={open}>
      <ToolRow
        summary={header.summary}
        summaryLink={header.summaryLink}
        failed={false}
        expandable
        expanded={open}
        onToggle={() => setOpen((current) => !current)}
      />
      {open && (
        <div className={CLASS.body} data-testid="tool-call-cluster-body">
          {items.map((item) => (
            <ToolCallItem
              key={item.id}
              item={item}
              turn={turn}
              live={item.status === "inProgress"}
              sessionRef={sessionRef}
            />
          ))}
        </div>
      )}
    </details>
  );
}
