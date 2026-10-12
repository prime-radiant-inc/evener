import { InlineNotice } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./diagnostics.module.css";

const CLASS = {
  list: requireClass(styles.list, "diagnostics.module.css", "list"),
};

/** A settings section's load warnings (the credentials registry's and the
 * launch config's diagnostics): a flat unordered list with no stable
 * per-entry identity of its own. Renders nothing when there are none. */
export function Diagnostics({ diagnostics }: { diagnostics: string[] }) {
  if (diagnostics.length === 0) return null;
  return (
    <InlineNotice title="Warnings" data-testid="diagnostics">
      <ul className={CLASS.list}>
        {diagnostics.map((d, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: diagnostics are a flat, unordered warning list with no stable identity of their own (the same text can legitimately repeat)
          <li key={index}>{d}</li>
        ))}
      </ul>
    </InlineNotice>
  );
}
