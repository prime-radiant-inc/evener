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

/** table is the reference listing: two aligned columns, chords against their
 * descriptions. inline is the aside under a cold pane's one action: a centred
 * row of chord-and-description pairs at caption size that wraps between
 * pairs, never inside one. */
export type ShortcutListLayout = "table" | "inline";

export interface ShortcutListProps {
  items: ShortcutListItem[];
  layout?: ShortcutListLayout;
}

const BASE_CLASS = {
  item: requireClass(styles.item, "shortcutlist.module.css", "item"),
  keys: requireClass(styles.keys, "shortcutlist.module.css", "keys"),
  label: requireClass(styles.label, "shortcutlist.module.css", "label"),
};

const LAYOUT_CLASS: Record<ShortcutListLayout, string> = {
  table: requireClass(styles.table, "shortcutlist.module.css", "table"),
  inline: requireClass(styles.inline, "shortcutlist.module.css", "inline"),
};

/** A list of keyboard shortcuts: each chord (a KeyHint) as the term, the
 * action it performs as its definition, in either layout. */
export function ShortcutList({ items, layout = "table" }: ShortcutListProps) {
  return (
    <dl className={LAYOUT_CLASS[layout]} data-layout={layout}>
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
