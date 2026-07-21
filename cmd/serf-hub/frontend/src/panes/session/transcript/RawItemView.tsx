// RawItemView is the item-renderer registry's fallback (types.ts's
// itemRendererFor): a plain "type + text" view for any ThreadItem.type with
// no dedicated renderer registered yet. It IS the "parent" StreamingText's
// own doc comment refers to: while the item is live and has pendingText
// chunks to stream, it renders StreamingText directly (imperative growth,
// proving the whole live pipeline end to end even before T2/T3 register
// nicer per-type views); once settled (or if there's nothing to stream), it
// shows the plain settled text field.

import { memo } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./rawitemview.module.css";
import { StreamingText } from "./StreamingText";
import { type ItemRenderProps, ignoringTurn } from "./types";

const CLASS = {
  item: requireClass(styles.item, "rawitemview.module.css", "item"),
  type: requireClass(styles.type, "rawitemview.module.css", "type"),
  text: requireClass(styles.text, "rawitemview.module.css", "text"),
};

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`/`live`, destructured
// below), so a fresh turn object on every streaming delta targeting a
// DIFFERENT item must not re-render an already-settled fallback row.
export const RawItemView = memo(function RawItemView({ item, live }: ItemRenderProps) {
  const chunks = item.pendingText;
  const streaming = live && chunks !== undefined && chunks.length > 0;
  return (
    <div className={CLASS.item} data-testid="raw-item" data-item-type={item.type}>
      <span className={CLASS.type}>{item.type}</span>
      {streaming ? <StreamingText chunks={chunks} /> : <span className={CLASS.text}>{item.text}</span>}
    </div>
  );
}, ignoringTurn);
