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
import { registerItemRenderer, type ItemRenderProps } from "../types";
import { Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./warningitem.module.css";

const CLASS = {
  row: requireClass(styles.row, "warningitem.module.css", "row"),
  message: requireClass(styles.message, "warningitem.module.css", "message"),
  hint: requireClass(styles.hint, "warningitem.module.css", "hint"),
};

export function WarningItem({ item }: ItemRenderProps) {
  const title = item.warning?.title;
  const hint = item.warning?.hint;
  const message = item.text;
  if (!title && !message && !hint) return null; // nothing to show

  return (
    <div className={CLASS.row} data-testid="warning-item">
      <Chip tone="attention">{title || "Warning"}</Chip>
      {message !== "" && (
        <div className={CLASS.message} data-testid="warning-message">
          {message}
        </div>
      )}
      {!!hint && (
        <div className={CLASS.hint} data-testid="warning-hint">
          {hint}
        </div>
      )}
    </div>
  );
}

registerItemRenderer("warning", WarningItem);
