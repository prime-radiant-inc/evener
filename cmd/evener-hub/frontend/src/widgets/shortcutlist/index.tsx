import { chordDisplayKeys, type KeySequence, serializeChord } from "@evener/appwire-client";
import { Fragment, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import { KeyHint } from "../keyhint";
import styles from "./shortcutlist.module.css";

export interface ShortcutListItem {
  /** One chord, in KeyHint's key names ("Mod" renders ⌘ or Ctrl). */
  keys?: string[];
  /** Alternative bindings and multi-press sequences from the keybinding
   * registry: each entry is one binding's presses in order, so two entries
   * are two chords the action accepts. Takes precedence over `keys`. */
  sequences?: readonly KeySequence[];
  /** Rendered in the chord column when no sequence has a press - the
   * cheatsheet's unbound action. Omitted by callers with no such state. */
  unbound?: ReactNode;
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

export interface KeySequencesProps {
  /** One action's chords: each entry is one binding's presses in order. */
  sequences: readonly KeySequence[];
  /** Rendered between alternative sequences. */
  orLabel?: ReactNode;
}

const BASE_CLASS = {
  item: requireClass(styles.item, "shortcutlist.module.css", "item"),
  keys: requireClass(styles.keys, "shortcutlist.module.css", "keys"),
  sequences: requireClass(styles.sequences, "shortcutlist.module.css", "sequences"),
  or: requireClass(styles.or, "shortcutlist.module.css", "or"),
  unbound: requireClass(styles.unbound, "shortcutlist.module.css", "unbound"),
  label: requireClass(styles.label, "shortcutlist.module.css", "label"),
};

const LAYOUT_CLASS: Record<ShortcutListLayout, string> = {
  table: requireClass(styles.table, "shortcutlist.module.css", "table"),
  inline: requireClass(styles.inline, "shortcutlist.module.css", "inline"),
};

/** One action's bindings as keycaps: each sequence renders its presses in
 * order (a multi-press chord), and alternative sequences are separated by
 * `orLabel` ("or"; the hold hints use "/"). */
export function KeySequences({ sequences, orLabel = "or" }: KeySequencesProps) {
  return (
    <span className={BASE_CLASS.sequences}>
      {sequences.map((sequence, i) => (
        // A binding's serialized chord is its identity: two alternatives
        // cannot share one and stay distinct bindings in the same scope.
        <Fragment key={serializeChord(sequence)}>
          {i > 0 && <span className={BASE_CLASS.or}>{orLabel}</span>}
          {sequence.map((press, pressIndex) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: repeated presses in a multi-press sequence ("g g") are content-identical, position is identity, and the list is static per binding
            <KeyHint key={`${pressIndex}:${serializeChord([press])}`} keys={chordDisplayKeys(press)} />
          ))}
        </Fragment>
      ))}
    </span>
  );
}

function chordSlot(item: ShortcutListItem): ReactNode {
  const sequences = item.sequences?.filter((sequence) => sequence.length > 0) ?? [];
  if (sequences.length > 0) return <KeySequences sequences={sequences} />;
  if (item.keys !== undefined && item.keys.length > 0) return <KeyHint keys={item.keys} />;
  return item.unbound != null ? <span className={BASE_CLASS.unbound}>{item.unbound}</span> : null;
}

/** A list of keyboard shortcuts: each chord (a KeyHint) as the term, the
 * action it performs as its definition, in either layout. An item may carry
 * alternative and multi-press bindings instead of one chord. */
export function ShortcutList({ items, layout = "table" }: ShortcutListProps) {
  return (
    <dl className={LAYOUT_CLASS[layout]} data-layout={layout}>
      {items.map((item, i) => (
        // The same chord legitimately appears twice with different meanings
        // (context-dependent bindings), and a label may be rich content, so
        // position is the only stable identity in a caller's fixed list.
        // biome-ignore lint/suspicious/noArrayIndexKey: fixed caller list, position is identity - see above
        <div className={BASE_CLASS.item} key={i}>
          <dt className={BASE_CLASS.keys}>{chordSlot(item)}</dt>
          <dd className={BASE_CLASS.label}>{item.label}</dd>
        </div>
      ))}
    </dl>
  );
}
