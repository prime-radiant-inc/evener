// The userMessage item renderer: quiet, demoted treatment (design-system.md
// mockup #3 - "the user knows what they said," so their own prompt never
// out-shouts the agent's prose). Never streams (a user message always
// arrives settled - see internal/appprojector's EventUserInput, which emits
// straight to item/completed with no item/started leg), so unlike
// agentMessage/reasoning there is no live/settled branch here at all.
import { registerItemRenderer, type ItemRenderProps } from "../types";
import type { ItemModel } from "../../../../protocol/model";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { imagePlaceholder } from "./format";
import styles from "./usermessageitem.module.css";

const CLASS = {
  message: requireClass(styles.message, "usermessageitem.module.css", "message"),
  tag: requireClass(styles.tag, "usermessageitem.module.css", "tag"),
  text: requireClass(styles.text, "usermessageitem.module.css", "text"),
  images: requireClass(styles.images, "usermessageitem.module.css", "images"),
};

// UserMessageView is the shared presentational core, exported standalone so
// SteeringItem.tsx can reuse it verbatim for user-sourced steering (parity
// issue #24: a steer the human typed themselves renders exactly like a
// normal prompt, never the collapsible divider). Real image thumbnails are
// T4's job (transcript/flow/** media work) - `data-testid="user-message-
// image-placeholder"` marks the honest count-line stand-in this wave ships
// instead, so that later work has a stable hook to replace it at.
export function UserMessageView({ item }: { item: ItemModel }) {
  const imageCount = item.images?.length ?? 0;
  return (
    <div className={CLASS.message} data-testid="user-message-item">
      <span className={CLASS.tag}>You</span>
      {imageCount > 0 && (
        <div className={CLASS.images} data-testid="user-message-image-placeholder">
          {imagePlaceholder(imageCount)}
        </div>
      )}
      <div className={CLASS.text}>{item.text}</div>
    </div>
  );
}

export function UserMessageItem({ item }: ItemRenderProps) {
  return <UserMessageView item={item} />;
}

registerItemRenderer("userMessage", UserMessageItem);
