import { type FormEvent, type ReactNode, useState } from "react";
import { Button } from "../button";
import { IconButton } from "../iconbutton";
import { Input } from "../input";
import { requireClass } from "../internal/requireClass";
import styles from "./collectioneditor.module.css";

export type CollectionAddResult = { ok: true } | { ok: false; error: string };

export interface CollectionEditorProps<T> {
  /** Accessible name for the row list (aria-label) and, wrapped around the
   * add field, its accessible name too - required, matching Meter/Switch's
   * own required accessible-name props. */
  label: string;
  items: readonly T[];
  getKey: (item: T) => string;
  renderItem: (item: T) => ReactNode;
  /** Accessible name for a given row's remove button, e.g. `Remove
   * ${item.path}` - a bare "Remove" would be ambiguous with N rows on the
   * page at once. */
  removeLabel: (item: T) => string;
  /** Fires immediately on click - no built-in confirm step. This wave's
   * binding constraint (every row removal confirms) is the CALLER's
   * responsibility: gate this handler behind a ConfirmDialog keyed on the
   * pending item, rather than mutating directly - see ConfirmDialog's own
   * doc comment for the pattern. Left un-opinionated here because the
   * confirm copy is different for every real use (marketplace vs.
   * instance vs. plain dir row). */
  onRemove: (item: T) => void;
  emptyMessage: string;
  /** Placeholder for the built-in plain-text add field. Unused (optional)
   * when `renderAddField` is given - the caller's own fields carry their
   * own placeholders then. */
  addPlaceholder?: string;
  addButtonLabel?: string;
  /** Replaces the built-in plain-text add field (and its submit button)
   * with the caller's own rendering - e.g. a PathField (dirListSetting.tsx)
   * or a structured key/value pair (envMap - collectionFields.tsx).
   * CollectionEditor still owns `draft`/`busy`/the submit `<form>`, so
   * `value`/`onChange` are that same draft state and Enter-to-submit keeps
   * working as long as the caller's own markup stays inside a real
   * `<input>` in the form; the caller's return value must include its own
   * submit trigger (a `type="submit"` button) since none is rendered by
   * default in this mode. Naming the field is the caller's own
   * responsibility too - the built-in visually-hidden `label` wrapper is
   * skipped in this mode (it only ever labels the single default input,
   * which no longer renders). */
  renderAddField?: (props: { value: string; onChange: (value: string) => void; disabled: boolean }) => ReactNode;
  /** Runs the caller's own validate-then-persist step against the
   * trimmed, non-empty add value (blank/whitespace-only input never
   * reaches this - the Add button stays disabled). Resolving `{ok:true}`
   * clears the add field; `{ok:false,error}` keeps it and shows `error`
   * inline instead. items is fully caller-owned (CollectionEditor holds no
   * copy of it) - a successful add is expected to be reflected back via a
   * new `items` array on the next render, the same way every other
   * controlled widget in this set works. */
  onAdd: (value: string) => Promise<CollectionAddResult> | CollectionAddResult;
}

const CLASS = {
  root: requireClass(styles.root, "collectioneditor.module.css", "root"),
  list: requireClass(styles.list, "collectioneditor.module.css", "list"),
  row: requireClass(styles.row, "collectioneditor.module.css", "row"),
  content: requireClass(styles.content, "collectioneditor.module.css", "content"),
  empty: requireClass(styles.empty, "collectioneditor.module.css", "empty"),
  addForm: requireClass(styles.addForm, "collectioneditor.module.css", "addForm"),
  addField: requireClass(styles.addField, "collectioneditor.module.css", "addField"),
  visuallyHidden: requireClass(styles.visuallyHidden, "collectioneditor.module.css", "visuallyHidden"),
  error: requireClass(styles.error, "collectioneditor.module.css", "error"),
};

function RemoveIcon() {
  return (
    <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
      <path d="M2 2 L10 10 M10 2 L2 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

/**
 * The generic add-row/remove-row list editor - the single most duplicated
 * shape in the legacy settings UI (directory lists, MCP config files,
 * inline MCP servers, ...). `items` is fully controlled by the caller;
 * this widget owns only its own add-field draft text, inline validation
 * error, and in-flight/busy state. Keyboard: the add field is a real
 * `<input>` inside a `<form>`, so Enter submits it exactly like clicking
 * Add (no custom key handling needed); every row's remove button is an
 * ordinary, individually tab-reachable button - no roving tabindex, since
 * arrow-key list navigation isn't part of this widget's contract (unlike
 * RadioGroup/Tree/Menu, nothing here cycles a fixed set of peer actions).
 */
export function CollectionEditor<T>({
  label,
  items,
  getKey,
  renderItem,
  removeLabel,
  onRemove,
  emptyMessage,
  addPlaceholder,
  addButtonLabel = "Add",
  onAdd,
  renderAddField,
}: CollectionEditorProps<T>) {
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const trimmed = draft.trim();

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (trimmed === "" || busy) return;
    setBusy(true);
    const result = await onAdd(trimmed);
    setBusy(false);
    if (result.ok) {
      setDraft("");
      setError(null);
    } else {
      setError(result.error);
    }
  }

  return (
    <div className={CLASS.root}>
      <ul aria-label={label} className={CLASS.list}>
        {items.length === 0 ? (
          <li className={CLASS.empty}>{emptyMessage}</li>
        ) : (
          items.map((item) => (
            <li key={getKey(item)} className={CLASS.row}>
              <div className={CLASS.content}>{renderItem(item)}</div>
              <IconButton
                label={removeLabel(item)}
                icon={<RemoveIcon />}
                variant="quiet"
                size="sm"
                onClick={() => onRemove(item)}
              />
            </li>
          ))
        )}
      </ul>
      <form className={CLASS.addForm} onSubmit={(event) => void handleSubmit(event)}>
        {renderAddField ? (
          renderAddField({ value: draft, onChange: setDraft, disabled: busy })
        ) : (
          <>
            <label className={CLASS.addField}>
              <span className={CLASS.visuallyHidden}>{label}</span>
              <Input
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                placeholder={addPlaceholder}
                disabled={busy}
              />
            </label>
            <Button type="submit" variant="quiet" disabled={trimmed === "" || busy}>
              {addButtonLabel}
            </Button>
          </>
        )}
      </form>
      {error !== null && (
        <p role="alert" className={CLASS.error}>
          {error}
        </p>
      )}
    </div>
  );
}
