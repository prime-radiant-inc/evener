// The warning item renderer (R2-B's type:"warning" items, see protocol/
// reducer.ts's own "warning" case): a quiet inline banner - legacy parity
// §WARNING's WARNING event -> appendBanner("warning", ...), modernized per
// the design system. The attention hue comes entirely from Chip's own tone
// prop (chip is pre-allowlisted in token-contract.test.ts); this file's own
// CSS module stays tokens-only with no bare --attention reference of its
// own, mirroring sandboxEscalation.tsx's identical Chip-carries-the-hue
// pattern.
//
// warning.title leads (inside the tone chip, falling back to a generic
// "Warning" label when absent), item.text is the body, warning.hint sits
// quiet below - each piece renders only when present/non-empty, and the
// whole row renders nothing at all when there is truly nothing to show.

import { hasWarningText } from "@evener/appwire-client";
import { memo } from "react";
import { Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, ignoringTurn, registerItemRenderer } from "../types";
import styles from "./warningitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "warningitem.module.css", "row"),
  message: requireClass(styles.message, "warningitem.module.css", "message"),
  hint: requireClass(styles.hint, "warningitem.module.css", "hint"),
};

// Memoized ignoring `turn` identity (types.ts's ignoringTurn): this
// component never reads `turn` at all (only `item`, destructured below), so
// a fresh turn object on every streaming delta targeting a DIFFERENT item
// must not re-render an already-settled warning row.
export const WarningItem = memo(function WarningItem({ item }: ItemRenderProps) {
  // hasWarningText is the package's own "is this actually content" reading
  // (a non-blank string) — the same one the reducer's raw-frame fallback
  // decides against, so a blank-but-present title/hint/message never shows
  // an empty chip or row here while item.text falls back to the raw frame.
  // It also rejects a non-string runtime value outright, so a malformed
  // wire frame's title/hint can never reach React as a child.
  const title = hasWarningText(item.warning?.title) ? item.warning?.title : undefined;
  const hint = hasWarningText(item.warning?.hint) ? item.warning?.hint : undefined;
  const message = hasWarningText(item.text) ? item.text : "";
  if (!title && !message && !hint) return null; // nothing to show

  return (
    <div className={CLASS.row} data-testid="warning-item">
      <Chip tone="attention">{title || "Warning"}</Chip>
      {message !== "" && (
        <div className={CLASS.message} data-testid="warning-message">
          {message}
        </div>
      )}
      {hint !== undefined && (
        <div className={CLASS.hint} data-testid="warning-hint">
          {hint}
        </div>
      )}
    </div>
  );
}, ignoringTurn);

registerItemRenderer("warning", WarningItem);
