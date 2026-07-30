// The userMessage item renderer: the slack-lean speaker treatment
// (docs/web-ui/specs/2026-07-29-transcript-slack-lean-messages.md, decisions
// 1, 2, 5). Speaker identity is a one-line header - avatar tile, then "You"
// at body size, then the message's clock time at caption - replacing the old
// stacked caption eyebrow, which was too faint to scan exchange boundaries
// by. The whole message is one flex row (avatar + content column), so the
// header and the text share the column the TurnBlock gutter aligns
// agent-side items to; there is deliberately no breakpoint here - the avatar
// stays inline at every width and TurnBlock owns the gutter media query.
// Never streams (a user message always arrives settled - see
// internal/appprojector's EventUserInput, which emits straight to
// item/completed with no item/started leg), so unlike agentMessage/reasoning
// there is no live/settled branch here at all.

import { memo, type ReactNode, useState } from "react";
import { sessionActionError } from "../../../../protocol/errors";
import type { ItemModel } from "../../../../protocol/model";
import { workspaceStore } from "../../../../shell/workspace";
import { threadsStore } from "../../../../stores/threads";
import { IconButton, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
// Direct widget path, NOT the widgets barrel: the barrel is controller-owned
// and does not re-export SpeakerAvatar yet.
import { SpeakerAvatar } from "../../../../widgets/speakeravatar";
import { writeDraft } from "../../composer/draft";
import { ImageGallery } from "../flow/ImageGallery";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import { formatClockTime } from "./format";
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
  // No placeholder when the wire carries no startedAt: a header with no time
  // shows no time rather than a guess (formatClockTime returns undefined for
  // a missing or unparseable timestamp).
  const time = formatClockTime(item.startedAt);
  return (
    <div
      className={CLASS.message}
      data-testid="user-message-item"
      data-opens-exchange={opensExchange ? "true" : undefined}
    >
      <span className={CLASS.avatar}>
        <SpeakerAvatar speaker="user" />
      </span>
      <div className={CLASS.content}>
        <div className={CLASS.header}>
          <span className={CLASS.name}>You</span>
          {time !== undefined && <span className={CLASS.time}>{time}</span>}
          {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
        </div>
        <div className={CLASS.body} data-testid="user-bubble">
          <div className={CLASS.text}>{item.text}</div>
          <ImageGallery images={item.images} />
        </div>
      </div>
    </div>
  );
}

export const UserMessageItem = memo(function UserMessageItem({ item, turn, sessionRef }: ItemRenderProps) {
  const actions = sessionRef ? <ForkFromHereButton sessionRef={sessionRef} turnId={turn.id} /> : undefined;
  return <UserMessageView item={item} actions={actions} />;
}, ignoringTurn);

registerItemRenderer("userMessage", UserMessageItem);
