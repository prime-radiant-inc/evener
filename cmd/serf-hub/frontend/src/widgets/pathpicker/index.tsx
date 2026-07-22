import { type ChangeEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { Button } from "../button";
import { IconButton } from "../iconbutton";
import inputStyles from "../input/input.module.css";
import { requireClass } from "../internal/requireClass";
import styles from "./pathpicker.module.css";

export interface PathPickerProps {
  id?: string;
  value: string;
  /** Fires on every keystroke (normal controlled-input reflect), and once
   * more on Accept (committing whatever directory the popup is currently
   * browsing). Browsing into a suggestion row never calls this - see this
   * widget's own doc comment for the full browse-vs-commit contract. */
  onChange: (value: string) => void;
  /** Returns the full paths of `path`'s direct children (not bare
   * basenames - each returned string is itself a valid `path` to pass back
   * in, so a clicked row can become the next `listChildren` call with no
   * reconstruction). A rejection is treated as "couldn't load this
   * folder", not surfaced as a thrown error - this widget has no RPC
   * knowledge of its own, and a caller's lister failing (permissions, a
   * transient network blip) shouldn't crash it. */
  listChildren: (path: string) => Promise<string[]>;
  placeholder?: string;
  disabled?: boolean;
  /** Accessible name for the dedicated Browse button. Default "Browse". */
  browseLabel?: string;
}

const CLASS = {
  root: requireClass(styles.root, "pathpicker.module.css", "root"),
  field: requireClass(styles.field, "pathpicker.module.css", "field"),
  textInput: requireClass(styles.textInput, "pathpicker.module.css", "textInput"),
  popup: requireClass(styles.popup, "pathpicker.module.css", "popup"),
  popupPath: requireClass(styles.popupPath, "pathpicker.module.css", "popupPath"),
  status: requireClass(styles.status, "pathpicker.module.css", "status"),
  entries: requireClass(styles.entries, "pathpicker.module.css", "entries"),
  entry: requireClass(styles.entry, "pathpicker.module.css", "entry"),
  footer: requireClass(styles.footer, "pathpicker.module.css", "footer"),
};

function FolderIcon() {
  return (
    <svg viewBox="0 0 14 12" width="12" height="10" aria-hidden="true">
      <path
        d="M1 2h4l1.3 1.6H13a1 1 0 0 1 1 1V10a1 1 0 0 1-1 1H1a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1Z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinejoin="round"
      />
    </svg>
  );
}

// Splits a typed value into the directory it's inside plus whatever prefix
// of the final path component has been typed so far - "browsing by the
// final path component of what's typed" (this widget's own floor-doc
// contract). No trailing slash is trimmed off `dir` beyond the split
// itself: "/opt/plugins/" -> {dir:"/opt/plugins", prefix:""}, matching
// dirname/basename's usual behavior for a path that already names a
// directory.
function splitTyped(value: string): { dir: string; prefix: string } {
  const idx = value.lastIndexOf("/");
  if (idx === -1) return { dir: "", prefix: value };
  return { dir: idx === 0 ? "/" : value.slice(0, idx), prefix: value.slice(idx + 1) };
}

function basename(path: string): string {
  const idx = path.lastIndexOf("/");
  return idx === -1 ? path : path.slice(idx + 1);
}

/**
 * A directory input with an assisted-browse popup, per the legacy dir-
 * picker contract (`test-settings-dir-picker.js` / `assets/settings-
 * pickers.js`): the text field behaves exactly like a plain input on every
 * keystroke (never a native `datalist`), and separately, typing or the
 * dedicated Browse button opens a popup listing directory children via the
 * caller-supplied `listChildren` (this widget stays wire-free - no RPC of
 * its own). Two distinct browse-entry algorithms, matching the legacy
 * split:
 *  - Browse button: lists children of the CURRENT FULL value, unfiltered
 *    (`.chip-picker-dir` opened from the button, "listing children of its
 *    sibling text input's current value").
 *  - Typing: lists children of the typed value's DIRECTORY, client-side
 *    filtered by the typed PREFIX (the final path component) - re-fetched
 *    only when the directory portion itself changes, not per keystroke.
 *
 * Clicking a suggestion row BROWSES into it (re-lists its own children,
 * keeps the popup open) without ever touching `value`/`onChange` - only
 * clicking "Use this folder" (or Enter, as a keyboard equivalent) commits
 * the CURRENTLY BROWSED directory via exactly one `onChange` call and
 * closes the popup. Escape, an outside click, or Cancel close it without
 * committing.
 */
export function PathPicker({
  id,
  value,
  onChange,
  listChildren,
  placeholder,
  disabled = false,
  browseLabel = "Browse",
}: PathPickerProps) {
  const [open, setOpen] = useState(false);
  const [browsePath, setBrowsePath] = useState("");
  const [prefixFilter, setPrefixFilter] = useState("");
  const [entries, setEntries] = useState<string[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const rootRef = useRef<HTMLFieldSetElement>(null);

  // Re-fetches whenever the browsed directory changes while the popup is
  // open. A fetch superseded by a newer browsePath before it resolves is
  // simply discarded (the `active` guard) - matching the same "a stale
  // response never overwrites a fresher one" shape as Combobox's own
  // options effect, without needing an AbortController the caller's own
  // listChildren isn't guaranteed to honor anyway.
  useEffect(() => {
    if (!open) return;
    let active = true;
    setEntries(null);
    setLoadFailed(false);
    listChildren(browsePath).then(
      (result) => {
        if (active) setEntries(result);
      },
      () => {
        if (active) {
          setEntries([]);
          setLoadFailed(true);
        }
      },
    );
    return () => {
      active = false;
    };
  }, [open, browsePath, listChildren]);

  useEffect(() => {
    if (!open) return undefined;
    function onDocumentMouseDown(event: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", onDocumentMouseDown);
    return () => document.removeEventListener("mousedown", onDocumentMouseDown);
  }, [open]);

  function openViaButton() {
    setBrowsePath(value);
    setPrefixFilter("");
    setOpen(true);
  }

  function openViaTyping(typedValue: string) {
    const { dir, prefix } = splitTyped(typedValue);
    setBrowsePath(dir);
    setPrefixFilter(prefix);
    setOpen(true);
  }

  function handleInputChange(event: ChangeEvent<HTMLInputElement>) {
    const newValue = event.target.value;
    onChange(newValue);
    if (newValue.trim() === "") {
      setOpen(false);
      return;
    }
    const { dir, prefix } = splitTyped(newValue);
    setPrefixFilter(prefix);
    if (!open || dir !== browsePath) setBrowsePath(dir);
    setOpen(true);
  }

  function accept() {
    onChange(browsePath);
    setOpen(false);
  }

  function browseInto(entry: string) {
    setBrowsePath(entry);
    setPrefixFilter("");
  }

  function handleInputKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case "ArrowDown":
        if (!open) {
          event.preventDefault();
          openViaTyping(value);
        }
        break;
      case "Enter":
        if (open) {
          event.preventDefault();
          accept();
        }
        break;
      default:
        break;
    }
  }

  // Escape closes regardless of which descendant currently has focus (the
  // input, the Browse button, or a row inside the open popup) - unlike
  // ArrowDown/Enter above, which are specifically the text field's own
  // shortcuts per the floor-doc contract, dismissal is a property of the
  // whole widget, mirroring Menu's own popup-level Escape handling.
  function handleRootKeyDown(event: KeyboardEvent<HTMLFieldSetElement>) {
    if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
    }
  }

  const filteredEntries = (entries ?? []).filter((entry) =>
    basename(entry).toLowerCase().startsWith(prefixFilter.toLowerCase()),
  );

  return (
    // <fieldset> (not a plain div): this legitimately groups the text
    // field, its Browse button, and the popup into one field-level unit -
    // Biome's own role="group" guidance points at the native element with
    // that implicit role rather than a div+role escape hatch. The native
    // widget role is also what makes a delegated onKeyDown here (catching
    // Escape bubbled from whichever descendant - input, Browse button, or
    // a popup row - currently has focus, the same delegation Menu's own
    // role="menu" <ul> uses) legitimate rather than a handler on an
    // arbitrary static element. Default browser fieldset chrome (border,
    // margin, padding) is reset in pathpicker.module.css.
    <fieldset ref={rootRef} className={CLASS.root} onKeyDown={handleRootKeyDown}>
      <div className={CLASS.field}>
        <input
          id={id}
          className={`${inputStyles.input} ${CLASS.textInput}`}
          value={value}
          onChange={handleInputChange}
          onKeyDown={handleInputKeyDown}
          placeholder={placeholder}
          disabled={disabled}
        />
        <IconButton
          label={browseLabel}
          icon={<FolderIcon />}
          variant="quiet"
          size="sm"
          onClick={openViaButton}
          disabled={disabled}
        />
      </div>
      {open && (
        <div className={CLASS.popup}>
          <p className={CLASS.popupPath}>{browsePath === "" ? "/" : browsePath}</p>
          {entries === null ? (
            <p className={CLASS.status}>Loading…</p>
          ) : loadFailed ? (
            <p className={CLASS.status}>Couldn't load this folder.</p>
          ) : filteredEntries.length === 0 ? (
            <p className={CLASS.status}>No subdirectories.</p>
          ) : (
            <ul className={CLASS.entries}>
              {filteredEntries.map((entry) => (
                <li key={entry}>
                  <button type="button" className={CLASS.entry} onClick={() => browseInto(entry)}>
                    {basename(entry)}
                  </button>
                </li>
              ))}
            </ul>
          )}
          <div className={CLASS.footer}>
            <Button variant="quiet" size="sm" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button variant="primary" size="sm" onClick={accept}>
              Use this folder
            </Button>
          </div>
        </div>
      )}
    </fieldset>
  );
}
