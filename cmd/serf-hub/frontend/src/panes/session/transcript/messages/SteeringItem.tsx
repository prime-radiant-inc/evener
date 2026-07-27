// The steering item renderer. Source-discriminated first, then kind-routed:
//
//   - source === "user" (parity issue #24, appwire_projection.go:588 /
//     apptranscript.go:225-228): steering the human typed themselves is
//     indistinguishable from a normal prompt and reuses UserMessageView.
//   - daemon-originated steering: routes on item.steeringKind (the wire's
//     events.SteeringKind*, named at the injection site) rather than
//     guessing a kind from the message's prose. current-task/task-list are
//     suppressed - the tasks panel + task-update card already own that
//     surface (parity-m4 §8:209-217). A notification card renders per
//     <job-notification>/observer-callback block (contracts §17); that
//     routing stays content-driven, since structured markup can't
//     false-positive the way a prose pattern could, so it still fires for a
//     steer projected before the wire carried a kind. Everything else keeps
//     the collapsible divider, labeled from KIND_LABELS - an unrecognized or
//     absent kind renders unlabelled rather than inventing a label from a
//     raw slug.
//
// Daemon-sourced steering images are never rendered as thumbnails - only ever as
// a placeholder baked into the text server-side (apptranscript.go's
// ImagePlaceholder) - so, unlike UserMessageView, there is no images branch.
import { memo } from "react";
import type { SteeringKind } from "../../../../protocol/types.gen";
import { SteeringGlyph } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { NotificationCard } from "./NotificationCard";
import { parseSteeringNotifications } from "./steeringClassify";
import styles from "./steeringitem.module.css";
import { UserMessageView } from "./UserMessageItem";

const CLASS = {
  details: requireClass(styles.details, "steeringitem.module.css", "details"),
  summary: requireClass(styles.summary, "steeringitem.module.css", "summary"),
  label: requireClass(styles.label, "steeringitem.module.css", "label"),
  chevron: requireClass(styles.chevron, "steeringitem.module.css", "chevron"),
  body: requireClass(styles.body, "steeringitem.module.css", "body"),
};

const STEERED = "System steered";

// Labels for the wire's steering kinds (events.SteeringKind* on the Go side,
// generated onto SteeringKind in protocol/types.gen.ts). current-task and
// task-list are suppressed (the tasks panel owns them - SUPPRESSED below) and
// notification routes to a card, so those three carry no label. Every OTHER
// kind the daemon can emit must have one: this Record is exhaustive over the
// generated union, so adding a kind in Go and regenerating fails the build
// here until it is given a label. That is the point - the frontend's idea of
// what the daemon sends cannot drift from what it sends.
type LabelledKind = Exclude<SteeringKind, "current-task" | "task-list" | "notification">;

const KIND_LABELS: Record<LabelledKind, string> = {
  interrupted: "Interrupted",
  "agent-message": "Message sent",
  "hook-context": "Hook context",
  "precompact-hook": "Pre-compact hook",
  "compact-nudge": "Compaction nudge",
  "image-description": "Image description",
  "no-tool-calls": "No tool calls",
  "loop-detected": "Loop detection",
  "tasks-done": "Tasks done",
  "task-nudge": "Task nudge",
  "task-inactive": "Task list idle",
  "note-handoff": "Note to self",
  "goal-objective": "Goal objective",
  "transcript-pointer": "Transcript pointer",
};

// item.steeringKind is a plain string | undefined on the wire (a running
// frontend can meet a kind newer than its own build, or none at all), never
// the generated SteeringKind union itself, so the lookup stays tolerant of a
// miss instead of an indexed access that would silently type as string: an
// unrecognized kind renders unlabelled rather than inventing a label from a
// raw slug.
function labelFor(kind: string): string | undefined {
  return Object.hasOwn(KIND_LABELS, kind) ? KIND_LABELS[kind as LabelledKind] : undefined;
}

// The tasks panel and the task-update card already own these surfaces
// (parity-m4 §8:209-217), so they render nothing inline. Typed against the
// generated union so a typo here (unlike a typo in KIND_LABELS' keys, which
// TypeScript already rejects since it targets an exact Record) fails too.
const SUPPRESSED: ReadonlySet<SteeringKind> = new Set(["current-task", "task-list"]);

// The quiet collapsed-by-default steering divider (parity-m4 §8:
// appendSteeringDivider) - summary is the glyph, the kind label (or the bare
// fallback), and a trailing chevron; body is the verbatim steered text in a
// <pre> (never re-rendered as markdown). Open/closed state lives in the
// shared disclosureStore keyed by `id` (yt2q), so an expanded divider
// survives the VirtualList/dockview remount that would reset a native
// uncontrolled <details>. Collapsed by default.
function SteeringDivider({ id, label, text }: { id: string; label: string; text: string }) {
  const open = isDisclosureOpen(id, false);
  return (
    <details className={CLASS.details} data-testid="steering-item" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(id, false);
        }}
      >
        <SteeringGlyph />
        <span className={CLASS.label}>{label}</span>
        <span
          className={CLASS.chevron}
          aria-hidden="true"
          data-open={open ? "true" : "false"}
          data-testid="steering-chevron"
        >
          ▸
        </span>
      </summary>
      <pre className={CLASS.body}>{text}</pre>
    </details>
  );
}

export const SteeringItem = memo(function SteeringItem({ item }: ItemRenderProps) {
  // opensExchange={false}: a steer the human typed lands MID-turn, interrupting
  // work already under way rather than starting a new exchange, so it renders
  // like a prompt without claiming the boundary a prompt marks.
  if (item.source === "user") return <UserMessageView item={item} opensExchange={false} />;
  if (!item.text) return null; // no text, no images path here - nothing to show
  const kind = item.steeringKind ?? "";
  if ((SUPPRESSED as ReadonlySet<string>).has(kind)) return null;

  // Card routing stays content-driven: the trigger is <job-notification>
  // markup, which cannot false-positive, so a steer projected before the kind
  // field existed still renders its cards.
  const label = labelFor(kind);
  const { notifications, leftover } = parseSteeringNotifications(item.text);
  if (notifications.length > 0) {
    return (
      <>
        {notifications.map((n) => (
          <NotificationCard key={n.rawText} notification={n} />
        ))}
        {leftover && <SteeringDivider id={item.id} label={label ? `${STEERED}: ${label}` : STEERED} text={leftover} />}
      </>
    );
  }

  return <SteeringDivider id={item.id} label={label ? `${STEERED}: ${label}` : STEERED} text={leftover} />;
}, ignoringTurn);

registerItemRenderer("steering", SteeringItem);
