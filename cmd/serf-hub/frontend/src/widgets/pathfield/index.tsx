// The path field: one widget for every "enter a path" surface - a working
// directory, a system-prompt file, an ATIF export target. A control-shaped
// trigger opens a floating panel whose list is ALREADY expanded, and browsing
// writes the field as you go: a directory click both descends and becomes the
// value, so there is nothing to commit and no Cancel to undo.
//
// Wire-free by design, exactly like the PathPicker and DirField it replaces:
// the caller injects `complete` (serf/paths/complete) and, where recents mean
// something, `listRecents` (serf/projects/recent). includeFiles is derived
// from `kind` here, never passed in.
import { type JSX, type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor/pathpicker import ../button directly).
import { requireClass } from "../internal/requireClass";
import { Popover } from "../popover";
import styles from "./pathfield.module.css";
import { basename, buildPathRows, childrenPrefix, type PathPickableRow, parentOf, pickableRows } from "./pathRows";

const CLASS = {
  trigger: requireClass(styles.trigger, "pathfield.module.css", "trigger"),
  triggerValue: requireClass(styles.triggerValue, "pathfield.module.css", "triggerValue"),
  triggerDefault: requireClass(styles.triggerDefault, "pathfield.module.css", "triggerDefault"),
  chevron: requireClass(styles.chevron, "pathfield.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "pathfield.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "pathfield.module.css", "popoverPanel"),
  panel: requireClass(styles.panel, "pathfield.module.css", "panel"),
  input: requireClass(styles.input, "pathfield.module.css", "input"),
  list: requireClass(styles.list, "pathfield.module.css", "list"),
  groupRow: requireClass(styles.groupRow, "pathfield.module.css", "groupRow"),
  groupLabel: requireClass(styles.groupLabel, "pathfield.module.css", "groupLabel"),
  groupPath: requireClass(styles.groupPath, "pathfield.module.css", "groupPath"),
  row: requireClass(styles.row, "pathfield.module.css", "row"),
  rowActive: requireClass(styles.rowActive, "pathfield.module.css", "rowActive"),
  rowName: requireClass(styles.rowName, "pathfield.module.css", "rowName"),
  icon: requireClass(styles.icon, "pathfield.module.css", "icon"),
  check: requireClass(styles.check, "pathfield.module.css", "check"),
  meta: requireClass(styles.meta, "pathfield.module.css", "meta"),
  status: requireClass(styles.status, "pathfield.module.css", "status"),
};

const COMPLETE_DEBOUNCE_MS = 150;

/** What the field names, which decides both whether files are listed and what
 * a row click means. `outputFile` behaves exactly like `file`: the file may
 * not exist yet, so typing is expected and existing files are pickable
 * references. */
export type PathFieldKind = "dir" | "file" | "outputFile";

export interface PathFieldProps {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  /** Decides whether files are listed and what a row click means. Default "dir". */
  kind?: PathFieldKind;
  /** Injected - the widget stays wire-free. `includeFiles` is derived from
   * `kind`, never passed by the caller. A rejection degrades to an empty list:
   * this widget has no RPC knowledge, and a permissions failure or a transient
   * blip must not crash a form. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Recent project directories. Only the spawn working-directory field passes
   * this; a skills-directory field has no meaningful "recents". A rejection
   * (an older hub without the RPC) degrades silently to no Recent group. */
  listRecents?: () => Promise<string[]>;
  placeholder?: string;
  disabled?: boolean;
}

/** Files are listed for the two file kinds and never for a directory field,
 * which is what makes every row of a `dir` field's list a legal answer. */
function includesFiles(kind: PathFieldKind): boolean {
  return kind !== "dir";
}

/** The directory the panel opens on: a directory field browses the value
 * itself, a file field browses the file's parent. An empty value browses ""
 * which the hub resolves to $HOME. */
function openingDir(kind: PathFieldKind, value: string): string {
  const trimmed = value.trim();
  if (trimmed === "") return "";
  if (kind === "dir") return trimmed.replace(/\/+$/, "");
  return trimmed.includes("/") ? parentOf(trimmed) : "";
}

/** The directory part of a path being typed: a trailing slash means the typed
 * text already names the directory, otherwise the last component is a partial
 * name being filtered. */
function typedDir(text: string): string {
  if (text.endsWith("/")) return text.replace(/\/+$/, "");
  return text.includes("/") ? parentOf(text) : "";
}

/** The partial last component typed so far - the filter over the listing. */
function typedFilter(text: string): string {
  if (text.endsWith("/")) return "";
  const idx = text.lastIndexOf("/");
  return idx === -1 ? text : text.slice(idx + 1);
}

function rowDomId(listboxId: string, key: string): string {
  return `${listboxId}-${key}`;
}

function FolderIcon() {
  return (
    <svg viewBox="0 0 14 12" width="12" height="10" aria-hidden="true" className={CLASS.icon}>
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

function FileIcon() {
  return (
    <svg viewBox="0 0 12 14" width="10" height="12" aria-hidden="true" className={CLASS.icon}>
      <path
        d="M1.5 1h5L11 5v8a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V2a1 1 0 0 1 .5-1ZM6.5 1v4H11"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export interface PathFieldPanelProps {
  kind: PathFieldKind;
  value: string;
  /** Every keystroke and every browse step - the field is a plain controlled
   * input and its value tracks the browse position. */
  onChange: (value: string) => void;
  /** A pick that ends the interaction (a file, a recent, or the typed literal
   * on Enter): the enclosing PathField closes on this. */
  onCommit: (value: string) => void;
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  listRecents?: () => Promise<string[]>;
}

/**
 * The open panel's content only (the path input + its always-expanded list),
 * with no trigger or closed-state rendering of its own - extracted so a caller
 * with its own trigger affordance can reuse the browse behavior, and so the
 * behavior is testable without one.
 *
 * The ARIA 1.2 combobox-with-listbox pattern: role="combobox" on the input, a
 * role="listbox" sibling, aria-activedescendant tracking the highlighted row -
 * real DOM focus never leaves the input, so typing is never interrupted.
 * Unlike widgets/combobox the listbox is ALWAYS shown while the panel is open
 * (aria-expanded stays true): the panel itself is the popup.
 *
 * Dismissal is the enclosing Popover's job (Escape bubbles to its panel
 * handler, outside-click is its document listener), which is why there is no
 * Escape handler and no Cancel button here.
 */
export function PathFieldPanel({
  kind,
  value,
  onChange,
  onCommit,
  complete,
  listRecents,
}: PathFieldPanelProps): JSX.Element {
  // null means "the user hasn't typed yet": the input SHOWS the current value
  // (selected, so the first keystroke replaces it) while the list stays
  // unfiltered. Once typing starts the typed text is both the input's value
  // and the filter - including when it's cleared back to "".
  const [typed, setTyped] = useState<string | null>(null);
  const [currentDir, setCurrentDir] = useState(() => openingDir(kind, value));
  const [entries, setEntries] = useState<string[] | null>(null);
  const [recents, setRecents] = useState<string[]>([]);
  // Recents are dropped permanently by the first keystroke, for this panel's
  // lifetime - the same rule DirField had.
  const [showRecents, setShowRecents] = useState(listRecents !== undefined);
  // The highlighted row, by KEY rather than by list position: rows arrive
  // asynchronously (the listing, and the Recent group behind it), and an index
  // would silently slide onto a different row the moment anything was inserted
  // above it. undefined means nothing is highlighted, which is what lets Enter
  // commit the typed literal.
  const [activeKey, setActiveKey] = useState<string | undefined>(undefined);
  // Whether the highlight is the user's own doing. The current-file seeding
  // below is an opening courtesy, so it must never overwrite an arrow key.
  const highlightIsUserSetRef = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // Monotonic request id: a completion response older than the newest request
  // is dropped, so an out-of-order resolution never overwrites fresher
  // entries (PathPicker's `active` guard and DirField's reqId, kept).
  const reqIdRef = useRef(0);
  // The filter the entries in hand were fetched WITH. The hub hides dotfiles
  // unless the filter itself starts with a dot, so the listing's contents
  // depend on more than the directory - see handleType.
  const requestedFilterRef = useRef("");
  const listboxId = useId();

  const text = typed ?? value;
  const filter = typed === null ? "" : typedFilter(typed);
  const visibleEntries = useMemo(() => {
    if (entries === null || filter === "") return entries;
    const wanted = filter.toLowerCase();
    return entries.filter((entry) => basename(entry).toLowerCase().startsWith(wanted));
  }, [entries, filter]);
  const rows = useMemo(
    () => buildPathRows({ kind, currentDir, entries: visibleEntries, value, recents, showRecents }),
    [kind, currentDir, visibleEntries, value, recents, showRecents],
  );
  const picks = useMemo(() => pickableRows(rows), [rows]);
  // A key that no longer names a row (its listing was replaced) highlights
  // nothing, rather than being clamped onto whatever now sits at that position.
  const activeIndex = activeKey === undefined ? -1 : picks.findIndex((row) => row.key === activeKey);
  const activeRow = activeIndex >= 0 ? picks[activeIndex] : undefined;

  /** Move the highlight to a position in `picks`, and remember that the user is
   * the one driving it - which is what makes the seeding effect below back off
   * for the rest of this listing. */
  function moveHighlight(index: number): void {
    highlightIsUserSetRef.current = true;
    setActiveKey(picks[index]?.key);
  }

  /** Clear the highlight and hand control back to the seeding effect: a new
   * listing is on its way and its own current row should be highlighted. */
  function resetHighlight(): void {
    highlightIsUserSetRef.current = false;
    setActiveKey(undefined);
  }

  function runCompletion(prefix: string): void {
    requestedFilterRef.current = typedFilter(prefix);
    reqIdRef.current += 1;
    const reqId = reqIdRef.current;
    setEntries(null);
    complete(prefix, includesFiles(kind)).then(
      (result) => {
        if (reqId === reqIdRef.current) setEntries(result);
      },
      // A rejection is "couldn't read this directory", not an error to
      // surface: the hub itself returns an empty result for an unreadable
      // prefix, so the two are indistinguishable here by design.
      () => {
        if (reqId === reqIdRef.current) setEntries([]);
      },
    );
  }

  // Mount: take focus, select the pre-filled value so the first keystroke
  // replaces it wholesale, and start the opening listing. Mount-only - the
  // panel is created fresh on every open, and re-running this would fight a
  // user already typing.
  // biome-ignore lint/correctness/useExhaustiveDependencies: mount-only open sequence; the injected closures are captured for this panel's lifetime
  useEffect(() => {
    const input = inputRef.current;
    input?.focus();
    input?.select();
    runCompletion(childrenPrefix(openingDir(kind, value)));
    listRecents?.().then(
      (result) => setRecents(result),
      () => setRecents([]),
    );
    return () => clearTimeout(debounceRef.current);
  }, []);

  // SEED the highlight onto the current FILE's row when a listing arrives (a
  // directory row is never current - the group header is the you-are-here), so
  // a file field opens showing where you already are. Seeding only: once the
  // user has touched an arrow key this backs off entirely, because an async
  // arrival re-running it would discard the row they just moved to - and then
  // Enter would fall through to committing the typed literal and closing
  // instead of descending (spec 3.6).
  useEffect(() => {
    if (typed !== null || highlightIsUserSetRef.current) return;
    setActiveKey(picks.find((row) => (row.kind === "file" || row.kind === "dir") && row.current)?.key);
  }, [picks, typed]);

  // Keep the highlighted row visible inside the list's own scroll container.
  // scrollIntoView is called optionally: jsdom implements none at all.
  useEffect(() => {
    if (activeKey === undefined) return;
    document.getElementById(rowDomId(listboxId, activeKey))?.scrollIntoView?.({ block: "nearest" });
  }, [activeKey, listboxId]);

  /** Descend: the value tracks the browse position, so this writes the field
   * AND lists the directory's children. The panel stays open. */
  function browseInto(dir: string): void {
    onChange(dir);
    setCurrentDir(dir);
    setTyped(null);
    // A new listing is on its way: hand the highlight back to the seeding
    // effect so the new directory's own current row gets it.
    resetHighlight();
    clearTimeout(debounceRef.current);
    runCompletion(childrenPrefix(dir));
  }

  function pickRow(row: PathPickableRow): void {
    if (row.kind === "dir" || row.kind === "parent") {
      browseInto(row.path);
      return;
    }
    // A file has no children, and a recent is a finished answer.
    onCommit(row.path);
  }

  function handleType(next: string): void {
    setTyped(next);
    onChange(next);
    // Any typing drops the Recent group for the rest of this panel's life.
    setShowRecents(false);
    // Nothing is active while typing, which is what lets Enter commit the
    // typed literal (an outputFile naming a file that doesn't exist yet).
    resetHighlight();
    // A narrower last component usually filters the entries already in hand,
    // so only two things force a fresh listing: a different directory, or a
    // filter that crosses the leading-dot boundary. That second condition is
    // not cosmetic - the hub hides every dotted name unless the FILTER starts
    // with a dot, so the entries fetched for a dotless filter provably contain
    // no dotfile and no amount of client-side narrowing can surface one.
    //
    // The timer is cleared only when a new request replaces it - typing on
    // past a "/" into the next component happens inside one debounce window,
    // and cancelling unconditionally there would drop the listing the slash
    // just asked for.
    const dir = typedDir(next);
    const nextFilter = typedFilter(next);
    const dotnessChanged = nextFilter.startsWith(".") !== requestedFilterRef.current.startsWith(".");
    if (dir === currentDir && !dotnessChanged) return;
    setCurrentDir(dir);
    // Drop the old directory's children the INSTANT the header moves, not when
    // the debounced request finally fires: the header and the "../" row already
    // describe the new directory, and rendering the previous one's rows under
    // them for a whole debounce window makes every row a lie (clicking one
    // navigates somewhere the user never pointed at). "Loading…" for that
    // window is truthful.
    setEntries(null);
    clearTimeout(debounceRef.current);
    // With a filter present, the typed text itself IS the prefix: the hub
    // splits it into listDir + filter and does the matching (and the dotfile
    // decision) server-side, which is what DirField did too.
    const prefix = nextFilter === "" ? childrenPrefix(dir) : next;
    debounceRef.current = setTimeout(() => runCompletion(prefix), COMPLETE_DEBOUNCE_MS);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>): void {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (picks.length === 0) break;
        moveHighlight(Math.min(activeIndex + 1, picks.length - 1));
        break;
      case "ArrowUp":
        event.preventDefault();
        if (picks.length === 0) break;
        moveHighlight(Math.max(activeIndex - 1, 0));
        break;
      case "Home":
        if (picks.length === 0) break;
        event.preventDefault();
        moveHighlight(0);
        break;
      case "End":
        if (picks.length === 0) break;
        event.preventDefault();
        moveHighlight(picks.length - 1);
        break;
      case "Enter": {
        event.preventDefault();
        if (activeRow) {
          pickRow(activeRow);
          break;
        }
        // Nothing highlighted: the typed literal IS the answer, which is what
        // an outputFile field naming a file that doesn't exist yet needs.
        onCommit(text.trim());
        break;
      }
      default:
        break;
    }
  }

  return (
    <div className={CLASS.panel}>
      <input
        ref={inputRef}
        role="combobox"
        className={CLASS.input}
        value={text}
        onChange={(event) => handleType(event.target.value)}
        onKeyDown={handleKeyDown}
        aria-expanded
        aria-autocomplete="list"
        aria-controls={listboxId}
        // Named off the RESOLVED row, never off the stored key: a key whose row
        // is no longer listed must not be pointed at, since an
        // aria-activedescendant naming an absent element is an ARIA violation.
        aria-activedescendant={activeRow !== undefined ? rowDomId(listboxId, activeRow.key) : undefined}
        aria-label="Path"
      />
      {/* <ul role="listbox">/<li role="option"> is the WAI-ARIA APG
          combobox-with-listbox-popup pattern's own example markup
          (w3.org/WAI/ARIA/apg/patterns/combobox) - not an interactive role
          bolted onto an arbitrary static element, Biome's role-vs-element
          heuristic just doesn't special-case ul/li for it. */}
      <ul
        // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own listbox markup, see above
        role="listbox"
        id={listboxId}
        aria-label="Path"
        className={CLASS.list}
      >
        {rows.map((row) => {
          if (row.kind === "group") {
            // The current-directory header is a path, the Recent head is a
            // section title; they get different faces for that reason.
            const isPath = row.label.startsWith("/");
            return (
              <li
                key={row.key}
                role="presentation"
                className={`${CLASS.groupRow} ${isPath ? CLASS.groupPath : CLASS.groupLabel}`}
              >
                {row.label}
              </li>
            );
          }
          if (row.kind === "status") {
            return (
              <li key={row.key} role="presentation" className={CLASS.status}>
                {row.text}
              </li>
            );
          }
          const current = (row.kind === "file" || row.kind === "dir") && row.current;
          return (
            // Real focus never leaves the input (ARIA 1.2 activedescendant
            // pattern): aria-activedescendant above tracks the "virtual"
            // active option, and handleKeyDown's Enter case already performs
            // this same pick - so this <li> is deliberately not focusable and
            // needs no onKeyDown of its own.
            // biome-ignore lint/a11y/useFocusableInteractive: activedescendant pattern, real focus stays on the input, see above
            // biome-ignore lint/a11y/useKeyWithClickEvents: activedescendant pattern, Enter on the input already does this, see above
            <li
              key={row.key}
              id={rowDomId(listboxId, row.key)}
              // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ARIA APG listbox markup, see the ul above
              role="option"
              aria-selected={current}
              className={`${CLASS.row} ${row.key === activeKey ? CLASS.rowActive : ""}`}
              // Picking with the mouse must not blur the input (the ARIA 1.2
              // pattern keeps real focus there throughout).
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => pickRow(row)}
            >
              {row.kind === "file" ? <FileIcon /> : <FolderIcon />}
              <span className={CLASS.rowName}>{row.kind === "parent" ? "../" : row.name}</span>
              {current && (
                <span className={CLASS.check} aria-hidden="true">
                  ✓
                </span>
              )}
              {row.kind === "recent" && <span className={CLASS.meta}>{row.path}</span>}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/**
 * The closed state IS the trigger: a control-shaped button holding the current
 * path as plain monospace text plus a chevron, and clicking anywhere on it
 * opens the browse panel as a floating Popover (portaled, so it never reflows
 * the form and no scrollable ancestor can clip it). There is no separate
 * folder/Browse button - the whole field is the affordance.
 */
export function PathField({
  id,
  value,
  onChange,
  kind = "dir",
  complete,
  listRecents,
  placeholder,
  disabled = false,
}: PathFieldProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);

  // Popover's FocusScope is opted out of focus management entirely
  // (autoFocus={false}) so the panel's own input can hold focus and its
  // selection, which means returning focus to the trigger on close is this
  // component's job - otherwise focus falls to <body>.
  function closePanel(): void {
    setOpen(false);
    triggerRef.current?.focus();
  }

  // Committing deliberately does NOT refocus the trigger: it's a completed
  // choice, and yanking focus back would fight a keyboard user tabbing onward
  // through the form.
  function commit(path: string): void {
    setOpen(false);
    onChange(path);
  }

  return (
    <Popover
      open={open}
      onClose={closePanel}
      // The panel's own list scrolls, and a page scroll behind it must not
      // dismiss it mid-interaction.
      closeOnScroll={false}
      // The panel's input owns focus and its own text selection - see
      // closePanel for why FocusScope must not manage focus here.
      autoFocus={false}
      // The trigger is a form control: it fills its field slot so it lines up
      // with the Input/Select siblings beside it.
      stretchTrigger
      trigger={
        <button
          ref={triggerRef}
          id={id}
          type="button"
          className={CLASS.trigger}
          disabled={disabled}
          onClick={() => (open ? closePanel() : setOpen(true))}
        >
          {/* Plain text, not a Chip: the trigger already draws the control's
              own border, and a bordered chip inside it reads as a double
              border. */}
          <span className={`${CLASS.triggerValue} ${value === "" ? CLASS.triggerDefault : ""}`}>
            {value === "" ? (placeholder ?? "(default)") : value}
          </span>
          <span className={CLASS.chevron} aria-hidden="true">
            ▾
          </span>
          <span className={CLASS.srOnly}>— browse</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <PathFieldPanel
          kind={kind}
          value={value}
          onChange={onChange}
          onCommit={commit}
          complete={complete}
          listRecents={listRecents}
        />
      </div>
    </Popover>
  );
}
