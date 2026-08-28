import { useId } from "react";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import { useThreadsStore } from "../../../stores/threads";
import {
  disclosureScopeForSession,
  expandDetailsByDefault,
  type TranscriptRenderContextValue,
  useTranscriptRenderContext,
} from "../../../transcriptDisplay/renderContext";
import {
  disclosureDefault,
  isDisclosureOpen,
  scopedDisclosureId,
  toggleDisclosure,
} from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ToolCallItem } from "./ToolCallItem";
import { ToolRow } from "./ToolRow";
import { toolRendererFor } from "./toolRenderers";
import { consequenceRank } from "./tools/consequenceRank";
import styles from "./turnblock.module.css";
import { threadFingerprintForItem } from "./types";

interface ToolCallClusterProps {
  items: ItemModel[];
  turn: TurnModel;
  sessionRef?: string;
  renderContext?: TranscriptRenderContextValue;
  thread?: ThreadModel;
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
): { id: string; summary: string; summaryLink: string | undefined } {
  const lead = leadItem(items);
  const descriptor = toolRendererFor(lead.toolName ?? "");
  return {
    id: lead.id,
    summary: `${items.length} steps · ${descriptor.summary(lead, { cwd })}`,
    summaryLink: descriptor.summaryLink?.(lead),
  };
}

// A cluster has its own local disclosure because it is a derived presentation
// of a current adjacent run, not a durable per-item disclosure. The existing
// dynamic VirtualList measures the row after this body changes height; no
// viewport or scroll state belongs in this component. The wrapper is a plain
// div (not a native <details>): ToolRow renders the real disclosure button
// with aria-expanded, and the body below is a sibling rendered on `open`.
function ToolCallClusterBody({
  items,
  turn,
  sessionRef,
  legacyCwd,
  renderContext,
  thread,
}: ToolCallClusterProps & { legacyCwd?: string }) {
  const bodyId = useId();
  const providerContext = useTranscriptRenderContext();
  const context = renderContext ?? providerContext;
  const { config } = context;
  const disclosureScope = disclosureScopeForSession(context, sessionRef);
  // Same by-ref selector ToolCallItem.tsx's own summaryCwd/openBesideCwd use
  // (copied from fileOpenBeside.tsx) - snapshot-only ThreadModel state, so a
  // shell-led folded cluster's header strips its redundant "cd <cwd> && "
  // prefix exactly like the per-call row does.
  const cwd = thread?.cwd ?? context.thread?.cwd ?? legacyCwd;
  const header = clusterHeader(items, cwd);
  const clusterId = header.id;
  const disclosureKey = scopedDisclosureId(disclosureScope, clusterId);
  const disclosureFallback = expandDetailsByDefault(config) || disclosureDefault(disclosureScope, clusterId, false);
  const open = isDisclosureOpen(disclosureKey, disclosureFallback);
  return (
    <div className={CLASS.cluster} data-testid="tool-call-cluster">
      <ToolRow
        summary={header.summary}
        summaryLink={header.summaryLink}
        failed={false}
        expandable
        expanded={open}
        onToggle={() => toggleDisclosure(disclosureKey, disclosureFallback)}
        bodyId={bodyId}
      />
      {open && (
        <div id={bodyId} className={CLASS.body} data-testid="tool-call-cluster-body">
          {items.map((item) => (
            <ToolCallItem
              key={item.id}
              item={item}
              turn={turn}
              live={item.status === "inProgress"}
              sessionRef={sessionRef}
              renderContext={context}
              thread={thread}
              threadFingerprint={threadFingerprintForItem(
                item,
                thread,
                toolRendererFor(item.toolName ?? "").summarySuffix?.(item, thread),
              )}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function LegacyToolCallCluster(props: ToolCallClusterProps) {
  const legacyCwd = useThreadsStore((state) =>
    props.sessionRef === undefined ? undefined : state.threads.get(props.sessionRef)?.cwd,
  );
  return <ToolCallClusterBody {...props} legacyCwd={legacyCwd} />;
}

export function ToolCallCluster(props: ToolCallClusterProps) {
  return props.renderContext === undefined ? <LegacyToolCallCluster {...props} /> : <ToolCallClusterBody {...props} />;
}
