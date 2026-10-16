// The userMessage item renderer: the slack-lean speaker treatment
// (docs/web-ui/specs/2026-07-29-transcript-slack-lean-messages.md, decisions
// 1, 2, 5). Speaker identity is a one-line header - avatar tile, then "You"
// at body size, then the message's relative time at caption - replacing the old
// stacked caption eyebrow, which was too faint to scan exchange boundaries
// by. The whole message is one flex row (avatar + content column), so the
// header and the text share the column the TurnBlock gutter aligns
// agent-side items to; there is deliberately no breakpoint here - the avatar
// stays inline at every width and TurnBlock owns the gutter media query.
// Never streams (a user message always arrives settled - see
// internal/appprojector's EventUserInput, which emits straight to
// item/completed with no item/started leg), so unlike agentMessage/reasoning
// there is no live/settled branch here at all.

import type { ItemModel } from "@evener/appwire-client";
import { sessionActionError } from "@evener/appwire-client";
import { memo, type ReactNode, useState } from "react";
import { workspaceStore } from "../../../../shell/workspace";
import { browserDraftKey, getBrowserDraftRepository } from "../../../../stores/browserDraftRepository";
import { threadsStore } from "../../../../stores/threads";
import { IconButton, SpeakerAvatar, type SpeakerAvatarSpeaker, Tooltip, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { EntityText } from "../EntityText";
import { ImageGallery } from "../flow/ImageGallery";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { MessageTimestamp } from "./MessageTimestamp";
import styles from "./usermessageitem.module.css";

const CLASS = {
  message: requireClass(styles.message, "usermessageitem.module.css", "message"),
  avatar: requireClass(styles.avatar, "usermessageitem.module.css", "avatar"),
  content: requireClass(styles.content, "usermessageitem.module.css", "content"),
  header: requireClass(styles.header, "usermessageitem.module.css", "header"),
  name: requireClass(styles.name, "usermessageitem.module.css", "name"),
  time: requireClass(styles.time, "usermessageitem.module.css", "time"),
  actions: requireClass(styles.actions, "usermessageitem.module.css", "actions"),
  body: requireClass(styles.body, "usermessageitem.module.css", "body"),
  text: requireClass(styles.text, "usermessageitem.module.css", "text"),
  provisional: requireClass(styles.provisional, "usermessageitem.module.css", "provisional"),
  railmark: requireClass(styles.railmark, "usermessageitem.module.css", "railmark"),
};

// A simple line-and-node "fork" glyph (straight lines only, no arcs) -
// chosen over a Unicode character (unlike QueueStrip.tsx's plain-glyph
// icons) because there's no broadly-supported code point that reads
// unambiguously as "branch/fork"; drawn with currentColor so it inherits
// IconButton's own variant color exactly like a text glyph would.
function ForkGlyph() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <line x1="4" y1="3" x2="4" y2="13" stroke="currentColor" strokeWidth="1.3" />
      <line x1="4" y1="8" x2="12" y2="8" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="4" cy="3" r="1.6" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="4" cy="13" r="1.6" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="12" cy="8" r="1.6" fill="none" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

export interface ForkFromHereButtonProps {
  sessionRef: string;
  // The transcriptKey of the message being forked from (transcriptindex.
  // ItemKey), which the hub resolves back to this entry - see
  // UserMessageItem's own comment for why a turn id cannot substitute.
  transcriptKey: string;
}

// A child the hub created must outlive the row it was pressed from before
// its seeding settles: transcript rows are virtualized, so scrolling a
// message away and back remounts its button with fresh state, and a press
// that had lost the child would repeat the fork RPC and create a duplicate.
// The retention lives here, keyed by the session and the message being forked
// from, written the moment the hub creates the child, and survives until a
// press completes (or conflicts on) that child's seeding.
const unseededForks = new Map<string, { ref: string; originalInput: string }>();
// One fork operation per (session, message) at a time, registered before the
// first await: a row that unmounts mid-press remounts with fresh state and
// nothing retained yet (the child exists only after the RPC returns), and its
// press must JOIN the operation already in flight - forking again would
// create a duplicate child session.
const forkOperations = new Map<string, Promise<void>>();

export function resetForkRetentionForTests(): void {
  unseededForks.clear();
  forkOperations.clear();
}

function ForkFromHereButton({ sessionRef, transcriptKey }: ForkFromHereButtonProps) {
  const toasts = useToasts();
  const [busy, setBusy] = useState(false);

  async function handleFork() {
    setBusy(true);
    const retentionKey = JSON.stringify([sessionRef, transcriptKey]);
    const inFlight = forkOperations.get(retentionKey);
    if (inFlight) {
      // A previous press - possibly from a row that has since unmounted - is
      // already forking or seeding this message. Join it instead of creating
      // another child; the operation reports its own outcome.
      try {
        await inFlight;
      } catch {
        // The originating press already reported its own failure.
      } finally {
        setBusy(false);
      }
      return;
    }
    const operation = performFork(retentionKey);
    forkOperations.set(retentionKey, operation);
    try {
      await operation;
    } catch {
      // performFork reports its own failures.
    } finally {
      forkOperations.delete(retentionKey);
      setBusy(false);
    }
  }

  async function performFork(retentionKey: string): Promise<void> {
    const retained = unseededForks.get(retentionKey);
    let ref = retained?.ref;
    let originalInput = retained?.originalInput ?? "";
    try {
      if (!ref) {
        const resp = await threadsStore
          .getState()
          .forkFromTurn(sessionRef, { sourceItemKey: transcriptKey, deferInput: true });
        ref = resp.thread.evener.ref;
        originalInput = resp.originalInput ?? "";
        // The child is retained the moment the hub creates it, before any
        // seeding outcome settles: the row can unmount while the seed is
        // still pending, and a remounted press with fresh state must
        // complete THIS child, not repeat the fork RPC into a duplicate
        // session. A concurrent second seed converges through the
        // conflict-compare path below - the same shape the uncertain-seed
        // retry takes.
        unseededForks.set(retentionKey, { ref, originalInput });
      }
      const seeded = await getBrowserDraftRepository().seedDraft(browserDraftKey(ref), {
        text: originalInput,
        skillNames: [],
      });
      if (seeded.state === "conflict") {
        // A conflict can be this fork's own uncertain seed: the transaction
        // committed while its completion event stalled, the watchdog settled
        // the press as unavailable, and the retained child's retry now finds
        // the row it itself wrote. Load it - when the child carries exactly
        // the message being forked, the seeding succeeded and the navigation
        // completes instead of stranding the child behind a failure toast.
        const existing = await getBrowserDraftRepository().load(browserDraftKey(ref));
        if (existing?.text === originalInput) {
          unseededForks.delete(retentionKey);
          workspaceStore.getState().openPane("session", { ref });
          return;
        }
        // The child already carries a draft another surface seeded; that draft
        // wins, and a retry would only re-conflict.
        unseededForks.delete(retentionKey);
        toasts.push(
          "error",
          sessionActionError("Couldn't fork from here", new Error("The forked session already has a draft")),
        );
        return;
      }
      if (seeded.state !== "committed") {
        // The child exists on the hub (retained since the moment the hub
        // created it); only its local seeding failed. A later press finishes
        // THIS child without a second fork RPC, and this press reports what
        // actually happened instead of claiming the fork failed.
        toasts.push(
          "error",
          "Forked the session, but couldn't save its message locally. Press Fork from here again to retry.",
        );
        return;
      }
      unseededForks.delete(retentionKey);
      workspaceStore.getState().openPane("session", { ref });
    } catch (err) {
      // The child the RPC already created stays retained through a local
      // fault: the next press retries only its seeding.
      toasts.push("error", sessionActionError("Couldn't fork from here", err));
    }
  }

  return (
    <Tooltip label="Fork from here">
      <IconButton
        label="Fork from here"
        icon={<ForkGlyph />}
        variant="quiet"
        size="sm"
        disabled={busy}
        onClick={() => void handleFork()}
      />
    </Tooltip>
  );
}

export function UserMessageView({
  item,
  actions,
  opensExchange = true,
  speaker = "user",
  name = "You",
  timeIso,
  entityText = false,
  provisional,
}: {
  item: ItemModel;
  actions?: ReactNode;
  opensExchange?: boolean;
  // speaker/name/timeIso generalize the view beyond the human user's own
  // messages (delegate_send renders the agent's outgoing message and the
  // delegate's reply through this same slack-lean structure). Defaults
  // preserve the exact rendering a real userMessage gets.
  speaker?: SpeakerAvatarSpeaker;
  name?: string;
  timeIso?: string;
  /** Reserved for real user messages; tool-output callers keep literal text. */
  entityText?: boolean;
  /** Held-steer ghost caption (steering-ghost spec §2): presence applies the provisional register and renders in the meta slot; absence renders exactly the delivered form. */
  provisional?: string;
}) {
  // Missing or invalid server timestamps stay absent rather than showing a guess.
  const time = Date.parse(timeIso ?? item.startedAt ?? "");
  return (
    <div
      className={provisional === undefined ? CLASS.message : `${CLASS.message} ${CLASS.provisional}`}
      data-testid="user-message-item"
      data-speaker={speaker}
      data-opens-exchange={opensExchange ? "true" : undefined}
      data-provisional={provisional !== undefined ? "true" : undefined}
    >
      <span className={CLASS.avatar}>
        <SpeakerAvatar speaker={speaker} />
      </span>
      <div className={CLASS.content}>
        <div className={CLASS.header}>
          <span className={CLASS.name}>{name}</span>
          {provisional !== undefined ? (
            <span className={CLASS.time}>{provisional}</span>
          ) : (
            Number.isFinite(time) && (
              <span className={CLASS.time}>
                <MessageTimestamp value={time} />
              </span>
            )
          )}
          {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
        </div>
        <div className={CLASS.body} data-testid="user-bubble">
          <div className={CLASS.text}>{entityText ? <EntityText text={item.text} /> : item.text}</div>
          {provisional !== undefined && <span className={CLASS.railmark} aria-hidden="true" />}
          <ImageGallery images={item.images} />
        </div>
      </div>
    </div>
  );
}

// The fork affordance is driven by the item's own transcriptKey, never by the
// enclosing turn's id. thread/fork reads sourceItemKey as an item key
// (cmd/evener-hub/app_threadlifecycle.go parseSourceItemKey ->
// agent.ForkSessionAtUserTurn) and resolves it to the entry that opened the
// item; a live turn id names a different entry than the message it belongs to
// past the first turn or two, because it is numbered off its minter's own
// counter - internal/appprojector's per-turn counter, or the daemon's
// per-mutation counter (turn_m<seq>) - not the transcript's entry ordinal.
//
// An item with no transcriptKey has no persisted transcript position, so it
// names no divergence point and the action is simply not offered - the same
// refusal this component already makes for a read-only pane with no
// sessionRef, and the same stance cmd/evener-tui/hub_browse.go's
// startForkDraft takes when TurnIndexFromID yields 0 ("fork requires
// persisted transcript turn identity"). Guessing an entry is the one outcome
// that must never happen.
export const UserMessageItem = memo(function UserMessageItem({ item, sessionRef }: ItemRenderProps) {
  const transcriptKey = item.transcriptKey;
  const actions =
    sessionRef && transcriptKey ? (
      <ForkFromHereButton sessionRef={sessionRef} transcriptKey={transcriptKey} />
    ) : undefined;
  return <UserMessageView item={item} actions={actions} entityText />;
}, ignoringTurn);

registerItemRenderer("userMessage", UserMessageItem);
