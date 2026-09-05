import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { friendlyErrorMessage } from "../../protocol/errors";
import { Button } from "../button";
import { OverlayPanel } from "../dialog/OverlayPanel";
import { requireClass } from "../internal/requireClass";
import { basename, childrenPrefix, parentOf } from "../pathfield/pathRows";
import styles from "./directorypicker.module.css";

export interface DirectoryPickerProps {
  value: string;
  fallbackDir?: string;
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  listRecents?: () => Promise<string[]>;
  validatePath: (path: string, kind: string) => Promise<{ valid: boolean; path?: string; error?: string }>;
  createDirectory: (path: string) => Promise<void>;
  onPick: (path: string) => void;
  onClose: () => void;
}

const CLASS = {
  panel: requireClass(styles.panel, "directorypicker.module.css", "panel"),
  body: requireClass(styles.body, "directorypicker.module.css", "body"),
  browser: requireClass(styles.browser, "directorypicker.module.css", "browser"),
  recents: requireClass(styles.recents, "directorypicker.module.css", "recents"),
  browse: requireClass(styles.browse, "directorypicker.module.css", "browse"),
  navigation: requireClass(styles.navigation, "directorypicker.module.css", "navigation"),
  crumbs: requireClass(styles.crumbs, "directorypicker.module.css", "crumbs"),
  pathForm: requireClass(styles.pathForm, "directorypicker.module.css", "pathForm"),
  input: requireClass(styles.input, "directorypicker.module.css", "input"),
  listHeading: requireClass(styles.listHeading, "directorypicker.module.css", "listHeading"),
  folders: requireClass(styles.folders, "directorypicker.module.css", "folders"),
  row: requireClass(styles.row, "directorypicker.module.css", "row"),
  path: requireClass(styles.path, "directorypicker.module.css", "path"),
  footer: requireClass(styles.footer, "directorypicker.module.css", "footer"),
  destination: requireClass(styles.destination, "directorypicker.module.css", "destination"),
  actions: requireClass(styles.actions, "directorypicker.module.css", "actions"),
  create: requireClass(styles.create, "directorypicker.module.css", "create"),
  status: requireClass(styles.status, "directorypicker.module.css", "status"),
};

export function DirectoryIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M2 3h4l2 2h6v8H2V3Z" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round" />
    </svg>
  );
}

/** The selected directory is committed once. Navigation, typing and creation
 * stay local so cancelled browsing cannot reload the caller's configuration. */
export function DirectoryPicker({
  value,
  fallbackDir,
  complete,
  listRecents,
  validatePath,
  createDirectory,
  onPick,
  onClose,
}: DirectoryPickerProps) {
  const initialPath = value || fallbackDir || "~";
  const [current, setCurrent] = useState(initialPath);
  const [typed, setTyped] = useState(initialPath);
  const [entries, setEntries] = useState<string[] | null>(null);
  const [validated, setValidated] = useState(false);
  const [recents, setRecents] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [folderName, setFolderName] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef(0);
  const mounted = useRef(true);
  const pathInput = useRef<HTMLInputElement>(null);
  const pathFocused = useRef(false);
  const goButton = useRef<HTMLButtonElement>(null);
  const nameInput = useRef<HTMLInputElement>(null);
  const wasCreating = useRef(false);
  const pathId = useId();
  const nameId = useId();

  async function browse(path: string) {
    // Rows disappear during navigation. Keep focus on a persistent control
    // without opening the phone keyboard when browsing by touch.
    if (document.activeElement !== pathInput.current) goButton.current?.focus();
    const id = ++request.current;
    setValidated(false);
    setEntries(null);
    setError(null);
    setCreating(false);
    try {
      const result = await validatePath(path.trim(), "dir");
      if (!mounted.current || request.current !== id) return;
      // Selection requires an existing directory; New folder explicitly creates
      // a child before it becomes a selectable destination.
      if (!result.valid) {
        setError(result.error || "This path is not a directory.");
        return;
      }
      const canonical = result.path || path.trim();
      setCurrent(canonical);
      setTyped(canonical);
      setValidated(true);
      const children = await complete(childrenPrefix(canonical), false);
      if (mounted.current && request.current === id) setEntries(children);
    } catch (err) {
      if (mounted.current && request.current === id) setError(friendlyErrorMessage(err));
    }
  }

  // Callers key this draft by the committed directory so external route changes
  // discard stale browsing. Late responses cannot revive a closed
  // picker or replace a directory selected by a newer navigation.
  // biome-ignore lint/correctness/useExhaustiveDependencies: opening snapshot; callbacks belong to this picker lifetime
  useEffect(() => {
    mounted.current = true;
    void browse(initialPath);
    listRecents?.().then(
      (paths) => {
        if (mounted.current) setRecents([...new Set(paths)]);
      },
      () => {},
    );
    return () => {
      mounted.current = false;
      request.current++;
    };
  }, []);

  useEffect(() => {
    if (creating) {
      wasCreating.current = true;
      nameInput.current?.focus();
    } else if (!busy && wasCreating.current) {
      // Creation removes the focused form while Go is disabled. Restore only
      // after that persistent control has become enabled in the rendered DOM.
      wasCreating.current = false;
      goButton.current?.focus();
    }
  }, [creating, busy]);

  const ready = validated && typed === current && !busy;
  // Paths belong to the Linux/macOS hub, regardless of the browser OS.
  // Match the shared path helpers and supported hub builds in .goreleaser.yml.
  const segments = current.split("/").filter(Boolean);
  const crumbs = segments.map((label, index) => ({
    label,
    path: `${current.startsWith("/") ? "/" : ""}${segments.slice(0, index + 1).join("/")}`,
  }));

  async function create(event: FormEvent) {
    event.preventDefault();
    event.stopPropagation();
    if (!ready) return;
    const name = folderName.trim();
    if (!name || name === "." || name === ".." || name.includes("/") || name.includes("\0")) {
      setError("Enter a folder name without slashes.");
      return;
    }
    const path = `${childrenPrefix(current)}${name}`;
    if (entries?.includes(path)) {
      setError("A folder with this name already exists.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await createDirectory(path);
      if (!mounted.current) return;
      // Creation is complete; browsing owns validation and listing independently.
      void browse(path);
    } catch (err) {
      if (mounted.current) setError(friendlyErrorMessage(err));
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  return createPortal(
    <OverlayPanel
      open
      onClose={onClose}
      title="Choose directory"
      panelClassName={CLASS.panel}
      bodyClassName={CLASS.body}
      footer={
        <div className={CLASS.footer}>
          <div className={CLASS.destination}>
            <span>Use this directory</span>
            <strong>{basename(current) || "/"}</strong>
            <span className={CLASS.path}>{current}</span>
          </div>
          <div className={CLASS.actions}>
            <Button variant="quiet" onClick={onClose}>
              Cancel
            </Button>
            <Button disabled={!ready || creating} onClick={() => onPick(current)}>
              Use this folder
            </Button>
          </div>
        </div>
      }
    >
      <div className={CLASS.browser}>
        <aside className={CLASS.recents} aria-label="Recent directories">
          {listRecents && <h3>Recent</h3>}
          {recents.map((path) => (
            <button
              className={CLASS.row}
              type="button"
              key={path}
              disabled={busy}
              aria-label={`Open recent ${path}`}
              onClick={() => void browse(path)}
            >
              <strong>{basename(path) || "/"}</strong>
              <span className={CLASS.path}>{parentOf(path)}</span>
            </button>
          ))}
          {listRecents && recents.length === 0 && <p className={CLASS.status}>No recent directories.</p>}
          <h3>Locations</h3>
          <Button variant="quiet" disabled={busy} onClick={() => void browse("~")}>
            Home
          </Button>
        </aside>
        <div className={CLASS.browse}>
          <div className={CLASS.navigation}>
            <Button
              variant="secondary"
              aria-label="Parent directory"
              disabled={busy || current === "/"}
              onClick={() => void browse(parentOf(current))}
            >
              ↑
            </Button>
            <nav className={CLASS.crumbs} aria-label="Directory breadcrumbs">
              <Button variant="quiet" disabled={busy} onClick={() => void browse("/")}>
                /
              </Button>
              {crumbs.length > 3 && (
                <details>
                  <summary aria-label="Earlier directories">…</summary>
                  {crumbs.slice(0, -3).map((crumb) => (
                    <Button key={crumb.path} variant="quiet" disabled={busy} onClick={() => void browse(crumb.path)}>
                      {crumb.label}
                    </Button>
                  ))}
                </details>
              )}
              {crumbs.slice(-3).map((crumb) => (
                <Button variant="quiet" key={crumb.path} disabled={busy} onClick={() => void browse(crumb.path)}>
                  {crumb.label}
                </Button>
              ))}
            </nav>
          </div>
          <form
            className={CLASS.pathForm}
            onSubmit={(event) => {
              event.preventDefault();
              event.stopPropagation();
              if (!busy) void browse(typed);
            }}
          >
            <input
              id={pathId}
              ref={pathInput}
              className={CLASS.input}
              aria-label="Path"
              value={typed}
              spellCheck={false}
              autoComplete="off"
              disabled={busy}
              onFocus={(event) => {
                if (!pathFocused.current) {
                  pathFocused.current = true;
                  event.currentTarget.select();
                }
              }}
              onChange={(event) => {
                request.current++;
                setTyped(event.target.value);
                setError(null);
              }}
            />
            <Button ref={goButton} variant="secondary" type="submit" disabled={busy || !typed.trim()}>
              Go
            </Button>
          </form>
          <div className={CLASS.listHeading}>
            <span>Folders</span>
            <Button
              variant="quiet"
              size="sm"
              disabled={!ready}
              onClick={() => {
                setCreating(true);
                setFolderName("");
                setError(null);
              }}
            >
              New folder
            </Button>
          </div>
          {creating && (
            <form className={CLASS.create} onSubmit={(event) => void create(event)}>
              <label htmlFor={nameId}>Folder name</label>
              <input
                id={nameId}
                ref={nameInput}
                className={CLASS.input}
                value={folderName}
                disabled={busy}
                onChange={(event) => setFolderName(event.target.value)}
                autoComplete="off"
              />
              <div className={CLASS.actions}>
                <Button
                  variant="quiet"
                  disabled={busy}
                  onClick={() => {
                    setCreating(false);
                    setError(null);
                  }}
                >
                  Cancel new folder
                </Button>
                <Button type="submit" disabled={busy}>
                  {busy ? "Creating…" : "Create folder"}
                </Button>
              </div>
            </form>
          )}
          {error && (
            <p className={CLASS.status} role="alert">
              {error}
            </p>
          )}
          <section className={CLASS.folders} aria-label="Folders" aria-busy={entries === null && error === null}>
            {entries === null ? (
              !error && (
                <p className={CLASS.status} role="status">
                  Loading directories…
                </p>
              )
            ) : entries.length === 0 ? (
              <p className={CLASS.status} role="status">
                No subfolders to display.
              </p>
            ) : (
              entries.map((path) => (
                <button
                  type="button"
                  className={CLASS.row}
                  disabled={busy}
                  aria-label={`Open ${path}`}
                  key={path}
                  onClick={() => void browse(path)}
                >
                  <DirectoryIcon />
                  <span>{basename(path)}</span>
                  <span aria-hidden="true">›</span>
                </button>
              ))
            )}
          </section>
        </div>
      </div>
    </OverlayPanel>,
    document.body,
  );
}
