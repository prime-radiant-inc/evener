import { KeyHint, type ShortcutListItem } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./welcome.module.css";

const CLASS = {
  chordHints: requireClass(styles.chordHints, "welcome.module.css", "chordHints"),
  chordHint: requireClass(styles.chordHint, "welcome.module.css", "chordHint"),
  chordHintLabel: requireClass(styles.chordHintLabel, "welcome.module.css", "chordHintLabel"),
};

/**
 * The cold-start chord hints: a centred row of keycaps that wraps, one chord
 * and its description per item. ShortcutList is the reference table for a
 * full shortcut listing (two aligned columns); this is the aside under a cold
 * pane's one action, where three short hints read as a single line. Local
 * rather than a widget because only the two cold-start panes (Welcome, New
 * session) carry it. The same description-list semantics as ShortcutList -
 * each chord is a term, its label the definition - and every key is drawn by
 * KeyHint.
 */
export function ChordHints({ items }: { items: ShortcutListItem[] }) {
  return (
    <dl className={CLASS.chordHints}>
      {items.map((item, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: fixed caller list, position is identity
        <div key={index} className={CLASS.chordHint}>
          <dt>
            <KeyHint keys={item.keys} />
          </dt>
          <dd className={CLASS.chordHintLabel}>{item.label}</dd>
        </div>
      ))}
    </dl>
  );
}
