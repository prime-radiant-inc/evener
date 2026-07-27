import { useState } from "react";
import type { ItemModel, TurnModel } from "../../../protocol/model";
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

function clusterSummary(items: ItemModel[]): string {
  const lead = leadItem(items);
  return `${items.length} steps · ${toolRendererFor(lead.toolName ?? "").summary(lead)}`;
}

// A cluster has its own local disclosure because it is a derived presentation
// of a current adjacent run, not a durable per-item disclosure. The existing
// dynamic VirtualList measures the row after this native details body changes
// height; no viewport or scroll state belongs in this component.
export function ToolCallCluster({ items, turn, sessionRef }: ToolCallClusterProps) {
  const [open, setOpen] = useState(false);
  return (
    <details className={CLASS.cluster} data-testid="tool-call-cluster" open={open}>
      <ToolRow
        summary={clusterSummary(items)}
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
