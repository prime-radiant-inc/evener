// The working-directory chip (floor §1.6): a text field plus an assisted-browse
// popup that lists recent projects (serf/projects/recent, first listing only)
// and live directory completions (serf/paths/complete, debounced 150ms). Wire-
// free like PathPicker - the parent injects listRecents/complete closures over
// the appwire client. Distinct from the generic PathPicker widget because the
// spawn dir-picker adds recents (accept-on-click), a `..` parent row, a
// use-current button, and the recents-drop-after-first-browse rule.
import { type ChangeEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { Button, IconButton, Input } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { Popover } from "../../widgets/popover";
import styles from "./dirField.module.css";
import { getGlobalLastWorkingDir, setGlobalLastWorkingDir } from "./spawnDefaults";

const COMPLETE_DEBOUNCE_MS = 150;

const CLASS = {
  root: requireClass(styles.root, "dirField.module.css", "root"),
  field: requireClass(styles.field, "dirField.module.css", "field"),
  panel: requireClass(styles.panel, "dirField.module.css", "panel"),
  section: requireClass(styles.section, "dirField.module.css", "section"),
  sectionLabel: requireClass(styles.sectionLabel, "dirField.module.css", "sectionLabel"),
  entries: requireClass(styles.entries, "dirField.module.css", "entries"),
  entry: requireClass(styles.entry, "dirField.module.css", "entry"),
  entryName: requireClass(styles.entryName, "dirField.module.css", "entryName"),
  entryPath: requireClass(styles.entryPath, "dirField.module.css", "entryPath"),
  status: requireClass(styles.status, "dirField.module.css", "status"),
  footer: requireClass(styles.footer, "dirField.module.css", "footer"),
  visuallyHidden: requireClass(styles.visuallyHidden, "dirField.module.css", "visuallyHidden"),
};

export interface DirFieldProps {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  /** serf/projects/recent -> recent project full paths. A rejection (older hub
   * without the RPC) degrades silently to "no recent section" (floor §1.6). */
  listRecents: () => Promise<string[]>;
  /** serf/paths/complete -> full-path directory children/completions for a
   * prefix. A trailing "/" lists children; otherwise it filters by basename. */
  complete: (prefix: string) => Promise<string[]>;
  placeholder?: string;
}

function basename(path: string): string {
  const trimmed = path.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return idx === -1 ? trimmed : trimmed.slice(idx + 1);
}

function parentOf(dir: string): string {
  const trimmed = dir.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  if (idx <= 0) return "/";
  return trimmed.slice(0, idx);
}

// The prefix that lists a directory's own children (trailing slash), matching
// completePaths' listDir branch. An empty dir lists the home directory.
function childrenPrefix(dir: string): string {
  if (dir === "") return "";
  return dir.endsWith("/") ? dir : `${dir}/`;
}

export function DirField({ id, value, onChange, listRecents, complete, placeholder }: DirFieldProps) {
  const [open, setOpen] = useState(false);
  const [browseValue, setBrowseValue] = useState("");
  const [currentDir, setCurrentDir] = useState("");
  const [entries, setEntries] = useState<string[] | null>(null);
  const [recents, setRecents] = useState<string[]>([]);
  const [showRecents, setShowRecents] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // Monotonic request id: a completion response older than the latest request
  // is dropped, so an out-of-order resolution never overwrites fresher entries
  // (floor §1.6, dir-picker.js:274-275,281).
  const reqIdRef = useRef(0);

  useEffect(() => () => clearTimeout(debounceRef.current), []);

  function runCompletion(prefix: string): void {
    reqIdRef.current += 1;
    const reqId = reqIdRef.current;
    setEntries(null);
    complete(prefix).then(
      (result) => {
        if (reqId === reqIdRef.current) setEntries(result);
      },
      () => {
        if (reqId === reqIdRef.current) setEntries([]);
      },
    );
  }

  function openViaButton(): void {
    const seed = value.trim() !== "" ? value : getGlobalLastWorkingDir();
    setBrowseValue(seed);
    setCurrentDir(seed);
    setShowRecents(true);
    setOpen(true);
    listRecents().then(
      (result) => setRecents(result),
      () => setRecents([]),
    );
    runCompletion(childrenPrefix(seed));
  }

  function handleType(event: ChangeEvent<HTMLInputElement>): void {
    const typed = event.target.value;
    onChange(typed);
    setBrowseValue(typed);
    setCurrentDir(typed.endsWith("/") ? typed.replace(/\/+$/, "") : parentOf(typed));
    // Any typing drops the recent-projects section for the rest of this
    // picker's lifetime (floor §1.6, dir-picker.js:230-252).
    setShowRecents(false);
    setOpen(true);
    clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => runCompletion(typed), COMPLETE_DEBOUNCE_MS);
  }

  function accept(path: string): void {
    const committed = path.trim();
    onChange(committed);
    setGlobalLastWorkingDir(committed);
    setOpen(false);
  }

  function browseInto(dir: string): void {
    setBrowseValue(dir);
    setCurrentDir(dir);
    setShowRecents(false);
    runCompletion(childrenPrefix(dir));
  }

  // Delegated at the fieldset (the Input widget forwards neither onKeyDown nor
  // aria-label): Escape closes from anywhere; Enter commits the typed literal
  // path, but ONLY from a text input (floor §1.6, dir-picker.js:311-319) - Enter
  // on a popup row/button must keep its native button activation.
  function handleRootKeyDown(event: KeyboardEvent<HTMLFieldSetElement>): void {
    if (!open) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      return;
    }
    if (event.key === "Enter" && (event.target as HTMLElement).tagName === "INPUT") {
      event.preventDefault();
      accept(browseValue);
    }
  }

  const showParent = currentDir !== "" && currentDir !== "/";

  return (
    // <fieldset> groups the field + its browse popover as one control, the same
    // native-role rationale PathPicker documents; chrome reset in the module.
    <fieldset className={CLASS.root} onKeyDown={handleRootKeyDown}>
      <Popover
        open={open}
        onClose={() => setOpen(false)}
        autoFocus={false}
        trigger={
          <div className={CLASS.field}>
            <Input id={id} value={value} onChange={handleType} placeholder={placeholder} />
            <IconButton
              label="Browse working directory"
              icon={<FolderIcon />}
              variant="quiet"
              size="sm"
              onClick={openViaButton}
            />
          </div>
        }
      >
        <div className={CLASS.panel}>
          {showRecents && recents.length > 0 && (
            <div className={CLASS.section}>
              <p className={CLASS.sectionLabel}>Recent projects</p>
              <ul className={CLASS.entries}>
                {recents.map((path) => (
                  <li key={path}>
                    <button type="button" className={CLASS.entry} onClick={() => accept(path)}>
                      <span className={CLASS.entryName}>{basename(path)}</span>
                      <span className={CLASS.entryPath}>{path}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
          <ul className={CLASS.entries}>
            {showParent && (
              <li>
                <button type="button" className={CLASS.entry} onClick={() => browseInto(parentOf(currentDir))}>
                  <span className={CLASS.entryName}>../</span>
                </button>
              </li>
            )}
            {entries === null ? (
              <li className={CLASS.status}>Loading…</li>
            ) : entries.length === 0 && !(showRecents && recents.length > 0) ? (
              <li className={CLASS.status}>No directories here.</li>
            ) : (
              entries.map((path) => (
                <li key={path}>
                  <button type="button" className={CLASS.entry} onClick={() => browseInto(path)}>
                    <span className={CLASS.entryName}>{basename(path)}</span>
                  </button>
                </li>
              ))
            )}
          </ul>
          <div className={CLASS.footer}>
            <Button variant="quiet" size="sm" onClick={() => setOpen(false)}>
              Cancel
            </Button>
          </div>
        </div>
      </Popover>
    </fieldset>
  );
}

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
