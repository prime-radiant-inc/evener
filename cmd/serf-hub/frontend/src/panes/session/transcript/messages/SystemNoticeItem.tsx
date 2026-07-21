// The systemMessage item renderer: quiet lifecycle/skill notices (compaction,
// model switch, skill activation, plugin loads, hook completions, round
// timings, ...). protocol/model.ts's ItemModel does not carry the wire's
// eventKind/description fields (types.gen.ts's ThreadItem has both;
// reducer.ts's wireItemToModel - T1-owned, out of this stream's scope to
// edit - never copies them across), so this renderer cannot distinguish
// "skill activated" from "round timings" from "compaction" etc. the way
// legacy's renderer.js does with its own separate coalescing tracks per
// sub-kind. Given that, every systemMessage item gets the SAME honest,
// quiet treatment uniformly - which already satisfies "system/skill
// notices: collapsed-by-default quiet groups" literally (a skill activation
// IS a systemMessage item, so it already gets the quiet/grouped treatment),
// just without a separate visual identity per sub-kind. See the wave-4 T2
// report for this as a flagged, deliberate scope simplification.
//
// Grouping (systemGrouping.ts's systemRunFor/shouldGroup) is recomputed
// fresh from turn.items on every render: a run of 3+ consecutive
// systemMessage items collapses into one disclosure, rendered only by the
// run's FIRST member (every other member of a grouped run renders nothing -
// its content already appears inside the first member's group). A run
// under 3 renders each item as its own standalone line, matching parity
// (contracts-transcript-scroll-liveness.md #12).

import type { ItemModel } from "../../../../protocol/model";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, registerItemRenderer } from "../types";
import { firstLine } from "./format";
import { type SystemRun, shouldGroup, systemRunFor } from "./systemGrouping";
import styles from "./systemnoticeitem.module.css";

const CLASS = {
  line: requireClass(styles.line, "systemnoticeitem.module.css", "line"),
  group: requireClass(styles.group, "systemnoticeitem.module.css", "group"),
  summary: requireClass(styles.summary, "systemnoticeitem.module.css", "summary"),
  groupBody: requireClass(styles.groupBody, "systemnoticeitem.module.css", "groupBody"),
};

// FALLBACK_LABEL covers a systemMessage item with no text at all (e.g. a
// plugin-loaded event with no resolved plugin name) - a category label
// beats an invisible row, without fabricating specifics the wire didn't
// provide.
const FALLBACK_LABEL = "System event";

function noticeText(item: ItemModel): string {
  return item.text || FALLBACK_LABEL;
}

function SystemLine({ item }: { item: ItemModel }) {
  return (
    <div className={CLASS.line} data-testid="system-notice-line">
      {noticeText(item)}
    </div>
  );
}

function SystemGroup({ run }: { run: SystemRun }) {
  const count = run.items.length;
  const first = firstLine(noticeText(run.items[0]!), 60);
  return (
    <details className={CLASS.group} data-testid="system-notice-group">
      <summary className={CLASS.summary}>
        {count} system events · {first}
      </summary>
      <div className={CLASS.groupBody}>
        {run.items.map((it) => (
          <SystemLine key={it.id} item={it} />
        ))}
      </div>
    </details>
  );
}

export function SystemNoticeItem({ item, turn }: ItemRenderProps) {
  const run = systemRunFor(turn.items, item.id);
  if (!run) return null; // defensive - the registry only dispatches systemMessage items here

  if (shouldGroup(run)) {
    if (!run.isFirst) return null; // absorbed into the run's first member
    return <SystemGroup run={run} />;
  }

  return <SystemLine item={item} />;
}

registerItemRenderer("systemMessage", SystemNoticeItem);
