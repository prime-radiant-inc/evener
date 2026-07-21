// The userMessage item renderer: quiet, demoted treatment (design-system.md
// mockup #3 - "the user knows what they said," so their own prompt never
// out-shouts the agent's prose). Never streams (a user message always
// arrives settled - see internal/appprojector's EventUserInput, which emits
// straight to item/completed with no item/started leg), so unlike
// agentMessage/reasoning there is no live/settled branch here at all.

import { memo } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { ImageGallery } from "../flow/ImageGallery";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import styles from "./usermessageitem.module.css";

const CLASS = {
  message: requireClass(styles.message, "usermessageitem.module.css", "message"),
  tag: requireClass(styles.tag, "usermessageitem.module.css", "tag"),
  text: requireClass(styles.text, "usermessageitem.module.css", "text"),
};

// UserMessageView is the shared presentational core, exported standalone so
// SteeringItem.tsx can reuse it verbatim for user-sourced steering (parity
// issue #24: a steer the human typed themselves renders exactly like a
// normal prompt, never the collapsible divider). item.images renders as
// real gallery thumbnails (ImageGallery already no-ops on an empty/absent
// array, so no conditional wrapper is needed here).
export function UserMessageView({ item }: { item: ItemModel }) {
  return (
    <div className={CLASS.message} data-testid="user-message-item">
      <span className={CLASS.tag}>You</span>
      <ImageGallery images={item.images} />
      <div className={CLASS.text}>{item.text}</div>
    </div>
  );
}

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (or even `live` - a user message
// never streams, see the file-top comment), so a fresh turn object on every
// streaming delta targeting a DIFFERENT item must not re-render an
// already-settled user message.
export const UserMessageItem = memo(function UserMessageItem({ item }: ItemRenderProps) {
  return <UserMessageView item={item} />;
}, ignoringTurn);

registerItemRenderer("userMessage", UserMessageItem);
