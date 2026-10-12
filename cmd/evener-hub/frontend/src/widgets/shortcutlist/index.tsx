import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import { KeyHint } from "../keyhint";
import styles from "./shortcutlist.module.css";

export interface ShortcutListItem {
  /** One chord, in KeyHint's key names ("Mod" renders ⌘ or Ctrl). */
  keys: string[];
  /** What the chord does. */
  label: ReactNode;
}

export interface ShortcutListProps {
  items: ShortcutListItem[];
}

const BASE_CLASS = {
  list: requireClass(styles.list, "shortcutlist.module.css", "list"),
  item: requireClass(styles.item, "shortcutlist.module.css", "item"),
  keys: requireClass(styles.keys, "shortcutlist.module.css", "keys"),
  label: requireClass(styles.label, "shortcutlist.module.css", "label"),
};

/** A reference list of keyboard shortcuts: each chord (a KeyHint) as the
 * term, the action it performs as its definition. */
export function ShortcutList({ items }: ShortcutListProps) {
  return (
    <dl className={BASE_CLASS.list}>
      {items.map((item, i) => (
        // The same chord legitimately appears twice with different meanings
        // (context-dependent bindings), and a label may be rich content, so
        // position is the only stable identity in a caller's fixed list.
        // biome-ignore lint/suspicious/noArrayIndexKey: fixed caller list, position is identity - see above
        <div className={BASE_CLASS.item} key={i}>
          <dt className={BASE_CLASS.keys}>
            <KeyHint keys={item.keys} />
          </dt>
          <dd className={BASE_CLASS.label}>{item.label}</dd>
        </div>
      ))}
    </dl>
  );
}
