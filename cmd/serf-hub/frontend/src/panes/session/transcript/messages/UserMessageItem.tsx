// The userMessage item renderer: quiet, demoted treatment (design-system.md
// mockup #3 - "the user knows what they said," so their own prompt never
// out-shouts the agent's prose). Never streams (a user message always
// arrives settled - see internal/appprojector's EventUserInput, which emits
// straight to item/completed with no item/started leg), so unlike
// agentMessage/reasoning there is no live/settled branch here at all.

import { memo, type ReactNode, useState } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { workspaceStore } from "../../../../shell/workspace";
import { threadsStore } from "../../../../stores/threads";
import { IconButton, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { writeDraft } from "../../composer/draft";
import { ImageGallery } from "../flow/ImageGallery";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import styles from "./usermessageitem.module.css";

const CLASS = {
  message: requireClass(styles.message, "usermessageitem.module.css", "message"),
  header: requireClass(styles.header, "usermessageitem.module.css", "header"),
  actions: requireClass(styles.actions, "usermessageitem.module.css", "actions"),
  tag: requireClass(styles.tag, "usermessageitem.module.css", "tag"),
  text: requireClass(styles.text, "usermessageitem.module.css", "text"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

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
  turnId: string;
}

// ForkFromHereButton (webui-ux-transcript C3, issue #42 "legacy per-message
// fork-into-composer"): calls the SAME thread/fork RPC as
// SessionActionsMenu.tsx's "Fork" menu action (stores/threads.ts's
// forkFromTurn), reached per-message instead of only from the session's
// last message. deferInput:true forks the child thread at this message's
// turn WITHOUT replaying it (appwire/types.go's ThreadForkParams.DeferInput
// doc comment): the wire hands the original text back as
// ThreadForkResponse.originalInput rather than auto-sending it, so
// writeDraft seeds the new session's composer with it via the same
// localStorage draft mechanism Composer.tsx already reads on its own mount
// (composer/draft.ts) - the forked session opens ready to edit/resend, never
// auto-run. openChildPane's own success path (SessionActionsMenu.tsx) is
// mirrored exactly: open the new ref as its own pane, no separate toast -
// the pane appearing IS the success affordance.
function ForkFromHereButton({ sessionRef, turnId }: ForkFromHereButtonProps) {
  const toasts = useToasts();
  const [busy, setBusy] = useState(false);

  async function handleFork() {
    setBusy(true);
    try {
      const resp = await threadsStore.getState().forkFromTurn(sessionRef, { sourceTurnId: turnId, deferInput: true });
      writeDraft(resp.thread.serf.ref, resp.originalInput ?? "");
      workspaceStore.getState().openPane("session", { ref: resp.thread.serf.ref });
    } catch (err) {
      toasts.push("error", `Couldn't fork from here: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <IconButton
      label="Fork from here"
      title="Fork from here"
      icon={<ForkGlyph />}
      variant="quiet"
      size="sm"
      disabled={busy}
      onClick={() => void handleFork()}
    />
  );
}

// UserMessageView is the shared presentational core, exported standalone so
// SteeringItem.tsx can reuse it verbatim for user-sourced steering (parity
// issue #24: a steer the human typed themselves renders exactly like a
// normal prompt, never the collapsible divider). `actions` is optional and
// omitted entirely by that reuse - only UserMessageItem below (the real
// "userMessage" registration) passes the per-message fork action, so a
// mid-turn steer never grows one of its own. item.images renders as real
// gallery thumbnails (ImageGallery already no-ops on an empty/absent array,
// so no conditional wrapper is needed here).
export function UserMessageView({ item, actions }: { item: ItemModel; actions?: ReactNode }) {
  return (
    <div className={CLASS.message} data-testid="user-message-item">
      <div className={CLASS.header}>
        <span className={CLASS.tag}>You</span>
        {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
      </div>
      <ImageGallery images={item.images} />
      <div className={CLASS.text}>{item.text}</div>
    </div>
  );
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): reads
// turn.id and sessionRef in addition to item/live, but both stay safe
// under this comparator even though it never compares `turn` by value -
// turn.id never changes for an existing turn (only the TurnModel object
// reference does, on an unrelated delta elsewhere in the same turn -
// reducer.ts's immutable-update discipline only replaces the item that
// actually changed), and sessionRef is constant for the whole pane's life
// (ItemRenderProps' own doc comment). So a fresh turn object on every
// streaming delta targeting a DIFFERENT item still can't change what this
// component would render, and it correctly skips re-rendering an
// already-settled user message.
export const UserMessageItem = memo(function UserMessageItem({ item, turn, sessionRef }: ItemRenderProps) {
  // sessionRef is undefined for the read-only "open beside" transcript pane
  // (panes/transcript/Transcript.tsx doesn't thread one through) - forking
  // needs a ref to call thread/fork with, so the action is withheld there
  // rather than reaching for one that doesn't exist.
  const actions = sessionRef ? <ForkFromHereButton sessionRef={sessionRef} turnId={turn.id} /> : undefined;
  return <UserMessageView item={item} actions={actions} />;
}, ignoringTurn);

registerItemRenderer("userMessage", UserMessageItem);
