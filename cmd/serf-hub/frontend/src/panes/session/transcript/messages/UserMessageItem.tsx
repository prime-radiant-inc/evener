// The userMessage item renderer: quiet, demoted treatment (design-system.md
// mockup #3 - "the user knows what they said," so their own prompt never
// out-shouts the agent's prose). Never streams (a user message always
// arrives settled - see internal/appprojector's EventUserInput, which emits
// straight to item/completed with no item/started leg), so unlike
// agentMessage/reasoning there is no live/settled branch here at all.

import { memo, type ReactNode, useState } from "react";
import { sessionActionError } from "../../../../protocol/errors";
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
  eyebrow: requireClass(styles.eyebrow, "usermessageitem.module.css", "eyebrow"),
  body: requireClass(styles.body, "usermessageitem.module.css", "body"),
  actions: requireClass(styles.actions, "usermessageitem.module.css", "actions"),
  text: requireClass(styles.text, "usermessageitem.module.css", "text"),
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
  turnId: string;
}

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
      toasts.push("error", sessionActionError("Couldn't fork from here", err));
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

export function UserMessageView({
  item,
  actions,
  opensExchange = true,
}: {
  item: ItemModel;
  actions?: ReactNode;
  opensExchange?: boolean;
}) {
  return (
    <div
      className={CLASS.message}
      data-testid="user-message-item"
      data-opens-exchange={opensExchange ? "true" : undefined}
    >
      <div className={CLASS.header}>
        <span className={CLASS.eyebrow}>You</span>
        {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
      </div>
      <div className={CLASS.body}>
        <ImageGallery images={item.images} />
        <div className={CLASS.text}>{item.text}</div>
      </div>
    </div>
  );
}

export const UserMessageItem = memo(function UserMessageItem({ item, turn, sessionRef }: ItemRenderProps) {
  const actions = sessionRef ? <ForkFromHereButton sessionRef={sessionRef} turnId={turn.id} /> : undefined;
  return <UserMessageView item={item} actions={actions} />;
}, ignoringTurn);

registerItemRenderer("userMessage", UserMessageItem);
