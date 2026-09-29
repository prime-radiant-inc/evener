// The steering item renderer. The human-note kind routes first (it rides the
// user-sourced rail but renders the labeled divider), then source ("user" ->
// UserMessageView), then kind-routed:
//
//   - source === "user" (parity issue #24, appwire_projection.go:588 /
//     apptranscript.go:225-228): steering the human typed themselves is
//     indistinguishable from a normal prompt and reuses UserMessageView -
//     except the human-note kind above, which labels the whiteboard update.
//   - daemon-originated steering: routes on item.steeringKind (the wire's
//     events.SteeringKind*, named at the injection site) rather than
//     guessing a kind from the message's prose. current-task/task-list are
//     suppressed - the tasks panel + task-update card already own that
//     surface (parity-m4 §8:209-217). A notification card renders per
//     <job-notification>/observer-callback block (contracts §17); that
//     routing stays content-driven, since structured markup can't
//     false-positive the way a prose pattern could, so it still fires for a
//     steer projected before the wire carried a kind. Everything else keeps
//     the collapsible divider, labeled by the shared steeringLabel - an
//     unrecognized or absent kind reads a bare "System steered" rather than
//     inventing a label from a raw slug.
//
// The label table and the suppression (@evener/appwire-client steeringLabels)
// are the phone's too. The table is exhaustive over the generated SteeringKind
// union, so adding a kind in Go and regenerating fails the build until it is
// given a label.
//
// Daemon-sourced steering images are never rendered as thumbnails - only ever as
// a placeholder baked into the text server-side (apptranscript.go's
// ImagePlaceholder) - so, unlike UserMessageView, there is no images branch.

import {
  isSuppressedSteeringKind,
  steeringLabel,
  steeringNotificationFragments,
  stripSystemReminder,
} from "@evener/appwire-client";
import { memo, useMemo } from "react";
import { Chevron, SteeringGlyph } from "../../../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { itemScopeKey } from "../tools/subagentModuleStore";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { NotificationCard } from "./NotificationCard";
import styles from "./steeringitem.module.css";
import { UserMessageView } from "./UserMessageItem";

const CLASS = {
  details: requireClass(styles.details, "steeringitem.module.css", "details"),
  summary: requireClass(styles.summary, "steeringitem.module.css", "summary"),
  railIcon: requireClass(styles.railIcon, "steeringitem.module.css", "railIcon"),
  label: requireClass(styles.label, "steeringitem.module.css", "label"),
  chevron: requireClass(styles.chevron, "steeringitem.module.css", "chevron"),
  body: requireClass(styles.body, "steeringitem.module.css", "body"),
};

// The quiet collapsed-by-default steering divider (parity-m4 §8:
// appendSteeringDivider) - summary is the glyph, the kind label (or the bare
// fallback), and a trailing chevron; body is the verbatim steered text in a
// <pre> (never re-rendered as markdown). Open/closed state lives in the
// shared disclosureStore keyed by session ref plus item id, so an expanded
// divider survives a remount without colliding with another session's item.
// Collapsed by default.
function SteeringDivider({
  id,
  label,
  text,
  sessionRef,
}: {
  id: string;
  label: string;
  text: string;
  sessionRef?: string;
}) {
  const disclosureKey = itemScopeKey(sessionRef, id);
  const open = isDisclosureOpen(disclosureKey, false);
  return (
    <details className={CLASS.details} data-testid="steering-item" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled to keep the store the single source of truth (see ToolCallItem.tsx) */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(disclosureKey, false);
        }}
      >
        {/* The diamond rails with the thought/tool kind icons (Jesse's
            unification call): a run row with an empty rail reads as
            "weirdly indented". The slot span carries the rail geometry
            (--speaker-avatar-size wide, 50% opacity); the summary itself
            takes the gutter pull above the breakpoint, same container-pull
            mechanism as thinkblock's. */}
        <span className={CLASS.railIcon} data-testid="steering-rail-icon" aria-hidden="true">
          <SteeringGlyph />
        </span>
        <span className={CLASS.label}>{label}</span>
        <span
          className={CLASS.chevron}
          aria-hidden="true"
          data-open={open ? "true" : "false"}
          data-testid="steering-chevron"
        >
          <Chevron />
        </span>
      </summary>
      <pre className={CLASS.body}>{text}</pre>
    </details>
  );
}

export const SteeringItem = memo(function SteeringItem({ item, sessionRef }: ItemRenderProps) {
  // Parsed once per text: a live turn re-renders this row on every publish.
  const fragments = useMemo(() => steeringNotificationFragments(item.text ?? ""), [item.text]);
  // The human-note steer rides the user-sourced steering rail (it interrupts
  // via the client-mutation steer path) but carries the human-note kind, and
  // the kind selects the divider: it labels the human's whiteboard update
  // distinctly rather than rendering as an indistinguishable user bubble.
  // Every other user-sourced steer still renders as a user message below.
  if (item.steeringKind === "human-note") {
    if (!item.text) return null; // no text, no images path here - nothing to show
    return (
      <SteeringDivider id={item.id} label={steeringLabel("human-note")} text={item.text} sessionRef={sessionRef} />
    );
  }
  // opensExchange={false}: a steer the human typed lands MID-turn, interrupting
  // work already under way rather than starting a new exchange, so it renders
  // like a prompt without claiming the boundary a prompt marks.
  if (item.source === "user") return <UserMessageView item={item} opensExchange={false} />;
  if (!item.text) return null; // no text, no images path here - nothing to show
  if (isSuppressedSteeringKind(item.steeringKind)) return null;

  // Card routing stays content-driven: the trigger is <job-notification>
  // markup, which cannot false-positive, so a steer projected before the kind
  // field existed still renders its cards.
  const label = steeringLabel(item.steeringKind);
  if (fragments) {
    return (
      <>
        {fragments.map((fragment, index) => {
          // Per-fragment identity: transcript item id + order-stable parse
          // index (see below). A stable string the key rule cannot mistake
          // for a bare loop index.
          const fragmentKey = `${item.id}:${index}`;
          return fragment.kind === "notification" ? (
            // rawText is NOT a safe key: a generic notification the daemon
            // never gave a job_id (e.g. a watch-timeout retry, kata rail-nav
            // React invariant) can appear more than once in the same steer
            // with byte-identical text, and rawText was colliding on it -
            // "Encountered two children with the same key" in the console,
            // and downstream React reconciler corruption (an "Expected static
            // flag was missing" internal invariant) on the next update once a
            // streamed delta rebuilds this same item with another duplicate.
            // fragments is a fresh, order-stable parse of this one item's
            // text (steeringNotificationFragments above), never a
            // diffed/reordered list, so the fragment key above is a safe,
            // stable identity - same reasoning as ExcerptText's own index key
            // in NotificationCard.tsx.
            <NotificationCard
              key={fragmentKey}
              notification={fragment.notification}
              sessionRef={sessionRef}
              disclosureId={fragmentKey}
            />
          ) : (
            // Each interstitial text span gets its own divider, positioned
            // where it appeared in the original text (issue #48) rather than
            // every span merged into one divider rendered after all cards. A
            // per-fragment id keeps its collapsed/expanded state independent
            // of any other divider on the same item.
            <SteeringDivider
              // biome-ignore lint/suspicious/noArrayIndexKey: index is stable - see comment above
              key={index}
              id={`${item.id}:${index}`}
              label={label}
              text={fragment.text}
              sessionRef={sessionRef}
            />
          );
        })}
      </>
    );
  }

  return <SteeringDivider id={item.id} label={label} text={stripSystemReminder(item.text)} sessionRef={sessionRef} />;
}, ignoringTurn);

registerItemRenderer("steering", SteeringItem);
