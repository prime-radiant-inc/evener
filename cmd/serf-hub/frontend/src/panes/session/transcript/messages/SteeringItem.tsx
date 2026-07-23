// The steering item renderer. Source-discriminated first, then content-
// classified:
//
//   - source === "user" (parity issue #24, appwire_projection.go:588 /
//     apptranscript.go:225-228): steering the human typed themselves is
//     indistinguishable from a normal prompt and reuses UserMessageView.
//   - daemon-originated steering: classifySteering (steeringClassify.ts, a pure
//     port of the legacy renderer-format.js:414-494, driven the same way
//     renderer.js:4706-4756 drove it) routes it to one of three treatments -
//     SUPPRESS (current-task / full-list / task-nudge: task bookkeeping the
//     tasks panel + the task-update card already own, parity-m4 §8:209-217), a
//     NOTIFICATION card per <job-notification>/observer-callback block
//     (contracts §17), or the quiet collapsible DIVIDER for every genuine
//     one-off system note. The divider now carries its own classified label
//     (loop detection / read-only nudge / tasks done / transcript pointer /
//     steering injected) instead of the single generic "Steering injected" the
//     pre-wave-8 renderer showed for every kind.
//
// Daemon-sourced steering images are never rendered as thumbnails - only ever as
// a placeholder baked into the text server-side (apptranscript.go's
// ImagePlaceholder) - so, unlike UserMessageView, there is no images branch.
import { memo } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { NotificationCard } from "./NotificationCard";
import { classifySteering, steeringTreatment } from "./steeringClassify";
import styles from "./steeringitem.module.css";
import { UserMessageView } from "./UserMessageItem";

const CLASS = {
  details: requireClass(styles.details, "steeringitem.module.css", "details"),
  summary: requireClass(styles.summary, "steeringitem.module.css", "summary"),
  detail: requireClass(styles.detail, "steeringitem.module.css", "detail"),
  body: requireClass(styles.body, "steeringitem.module.css", "body"),
};

// classifySteering returns semantic lower-case labels (matching the legacy
// kind strings); the divider displays them in sentence case per the design
// system (a bare first-letter capitalize is enough - every label is already a
// lower-case phrase, e.g. "loop detection" -> "Loop detection").
function sentenceCase(label: string): string {
  return label.charAt(0).toUpperCase() + label.slice(1);
}

// The quiet collapsed-by-default steering divider (parity-m4 §8:
// appendSteeringDivider) - summary is the classified label plus an optional
// " · detail"; body is the verbatim classified text in a <pre> (never
// re-rendered as markdown).
function SteeringDivider({ label, detail, text }: { label: string; detail: string; text: string }) {
  return (
    <details className={CLASS.details} data-testid="steering-item">
      <summary className={CLASS.summary}>
        {sentenceCase(label)}
        {detail && <span className={CLASS.detail}> · {detail}</span>}
      </summary>
      <pre className={CLASS.body}>{text}</pre>
    </details>
  );
}

export const SteeringItem = memo(function SteeringItem({ item }: ItemRenderProps) {
  if (item.source === "user") return <UserMessageView item={item} />;
  if (!item.text) return null; // no text, no images path here - nothing to show
  const classification = classifySteering(item.text);
  const treatment = steeringTreatment(classification.kind);
  if (treatment === "suppress") return null; // the tasks panel owns this surface
  if (treatment === "card") {
    // A single steering turn can carry several notification blocks, each its own
    // card; any leftover non-block text keeps a plain divider (parity-m4 §8).
    return (
      <>
        {classification.notifications?.map((n) => (
          <NotificationCard key={n.rawText} notification={n} />
        ))}
        {classification.leftover && (
          <SteeringDivider label="steering injected" detail="" text={classification.leftover} />
        )}
      </>
    );
  }
  return (
    <SteeringDivider label={classification.label} detail={classification.detail} text={classification.cleanText} />
  );
}, ignoringTurn);

registerItemRenderer("steering", SteeringItem);
