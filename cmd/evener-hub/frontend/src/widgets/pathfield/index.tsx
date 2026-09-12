// Directory fields open the shared DirectoryPicker and commit explicitly.
// File/output-file fields use a completion popover and retain literal-path entry.
// Callers inject filesystem operations; widgets never reach into stores or RPC.
import { type JSX, type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";
import { friendlyErrorMessage } from "../../protocol/errors";
import { Chevron } from "../chevron";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor imports ../button directly).
import { DirectoryPicker, type DirectoryPickerProps } from "../directorypicker";
import { requireClass } from "../internal/requireClass";
import { Popover } from "../popover";
import styles from "./pathfield.module.css";
import { basename, buildPathRows, childrenPrefix, type PathPickableRow, parentOf, pickableRows } from "./pathRows";

const CLASS = {
  trigger: requireClass(styles.trigger, "pathfield.module.css", "trigger"),
  directoryTrigger: requireClass(styles.directoryTrigger, "pathfield.module.css", "directoryTrigger"),
  directoryValue: requireClass(styles.directoryValue, "pathfield.module.css", "directoryValue"),
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
// How long the "you are here" header stays marked just-picked after a
// directory click (94yg) - long enough to register as a beat of feedback,
// short enough that it reads as a pulse rather than a persistent state.
const JUST_PICKED_MS = 700;

/** What the field names, which decides both whether files are listed and what
 * a row click means. `outputFile` behaves exactly like `file`: the file may
 * not exist yet, so typing is expected and existing files are pickable
 * references. */
export type PathFieldKind = "dir" | "file" | "outputFile";

interface PathFieldBaseProps {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  /** Injected - the widget stays wire-free. `includeFiles` is derived from
   * `kind`, never passed by the caller. A rejection degrades to an empty list:
   * this widget has no RPC knowledge, and a permissions failure or a transient
   * blip must not crash a form. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  /** Recent project directories. Only the spawn working-directory field passes
   * this; a skills-directory field has no meaningful "recents". A rejection
   * (an older hub without the RPC) degrades silently to no Recent group. */
  listRecents?: () => Promise<string[]>;
  /** The directory to browse when `value` is empty, instead of letting the hub
   * default to $HOME (spec 3.4). Only the spawn working-directory field has a
   * meaningful one - the last directory a session was launched in - so it is
   * optional everywhere else. */
  fallbackDir?: string;
  /** Fired once when the panel closes, with the field's final value. The spawn
   * working-directory field uses this to stamp its last-used-directory global
   * (spec 3.7) rather than writing it on every browse step. */
  onPanelClose?: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Names the closed trigger for assistive tech. Without it the trigger's
   * whole accessible name is the path it holds, which doesn't say WHICH field
   * it is when several sit on one page (LaunchConfigForm renders three pathList
   * add rows in one group). An `aria-label` REPLACES the button's text content
   * as its name, so this label is composed with the displayed path rather than
   * used alone: "<ariaLabel>: <path or placeholder> — browse". Callers pass the
   * field's own name ("Skill directories"), not a full sentence. */
  ariaLabel?: string;
}

/** Directory browsing always uses the shared validated picker. */
export interface DirectoryActions {
  validatePath: DirectoryPickerProps["validatePath"];
  createDirectory: DirectoryPickerProps["createDirectory"];
}

export type PathFieldProps = PathFieldBaseProps &
  (
    | { kind: "file" | "outputFile"; directory?: DirectoryActions }
    | { kind?: PathFieldKind; directory: DirectoryActions }
  );

/** A directory naming itself without its trailing slash - but "/" names the
 * filesystem root, which is a real directory and NOT the empty string. Those
 * two must stay distinct all the way down: "" means "no value, let the hub
 * default to $HOME", so collapsing "/" into it opens home instead of root and
 * labels the panel "Home". */
function withoutTrailingSlash(dir: string): string {
  const stripped = dir.replace(/\/+$/, "");
  return stripped === "" && dir !== "" ? "/" : stripped;
}

/** File browsing opens on the file's parent. An empty value browses
 * `fallbackDir` when the caller has one, otherwise "" which the hub resolves
 * to $HOME. */
function openingDir(value: string, fallbackDir?: string): string {
  const trimmed = value.trim();
  if (trimmed === "") return withoutTrailingSlash((fallbackDir ?? "").trim());
  return trimmed.includes("/") ? parentOf(trimmed) : "";
}

/** The directory part of a path being typed: a trailing slash means the typed
 * text already names the directory, otherwise the last component is a partial
 * name being filtered. */
function typedDir(text: string): string {
  if (text.endsWith("/")) return withoutTrailingSlash(text);
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

interface FileFieldPanelProps {
  kind: "file" | "outputFile";
  value: string;
  /** Every keystroke and every browse step - the field is a plain controlled
   * input and its value tracks the browse position. */
  onChange: (value: string) => void;
  /** A pick that ends the interaction (a file, a recent, or the typed literal
   * on Enter): the enclosing PathField closes on this. */
  onCommit: (value: string) => void;
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  listRecents?: () => Promise<string[]>;
  /** The directory to browse when `value` is empty - see PathFieldProps. */
  fallbackDir?: string;
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
 * The listbox is ALWAYS shown while the panel is open (aria-expanded stays
 * true): the panel itself is the popup.
 *
 * Dismissal is the enclosing Popover's job (Escape bubbles to its panel
 * handler, outside-click is its document listener), which is why there is no
 * Escape handler and no Cancel button here.
 */
function FileFieldPanel({
  kind,
  value,
  onChange,
  onCommit,
  complete,
  listRecents,
  fallbackDir,
}: FileFieldPanelProps): JSX.Element {
  // null means "the user hasn't typed yet": the input SHOWS the current value
  // (selected, so the first keystroke replaces it) while the list stays
  // unfiltered. Once typing starts the typed text is both the input's value
  // and the filter - including when it's cleared back to "".
  const [typed, setTyped] = useState<string | null>(null);
  const [currentDir, setCurrentDir] = useState(() => openingDir(value, fallbackDir));
  const [entries, setEntries] = useState<string[] | null>(null);
  // Set only when the LATEST request rejected - distinct from entries: []
  // meaning a genuinely empty directory. See buildPathRows's own doc comment
  // for why the two must never share a status line ("Nothing here." hides a
  // real failure from the user - the bug this state exists to fix).
  const [listError, setListError] = useState<string | null>(null);
  const [recents, setRecents] = useState<string[]>([]);
  // Recents are dropped permanently by the first keystroke, for this panel's
  // lifetime: once the user is typing a path, a recents list is noise.
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
  // entries.
  const reqIdRef = useRef(0);
  // The filter the entries in hand were fetched WITH. The hub hides dotfiles
  // unless the filter itself starts with a dot, so the listing's contents
  // depend on more than the directory - see handleType.
  const requestedFilterRef = useRef("");
  const listboxId = useId();
  // 94yg: a directory click both picks the value and descends into it (spec
  // 3.4 - no commit button, no Cancel), and the only thing that visibly
  // changes is this quiet "you are here" header - easy to miss, which is
  // exactly what the kata's two participants both hit. justPicked marks the
  // header for a beat right after a click registers the pick, then clears
  // itself - an in-the-moment confirmation, not a second control, so the
  // no-commit-button decision stands unchanged.
  const [justPicked, setJustPicked] = useState(false);
  const justPickedTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const text = typed ?? value;
  const filter = typed === null ? "" : typedFilter(typed);
  const visibleEntries = useMemo(() => {
    if (entries === null || filter === "") return entries;
    const wanted = filter.toLowerCase();
    return entries.filter((entry) => basename(entry).toLowerCase().startsWith(wanted));
  }, [entries, filter]);
  const rows = useMemo(
    () => buildPathRows({ kind, currentDir, entries: visibleEntries, value, recents, showRecents, listError }),
    [kind, currentDir, visibleEntries, value, recents, showRecents, listError],
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
    setListError(null);
    complete(prefix, true).then(
      (result) => {
        if (reqId === reqIdRef.current) setEntries(result);
      },
      // A rejection still degrades to an empty listing (there is no other
      // row shape for it, and an unreadable prefix legitimately looks the
      // same from here), but it must not read as "Nothing here." - that
      // hides a real failure (a closed client, a permissions error) behind
      // the same words an actually-empty directory gets. listError carries
      // the distinction into buildPathRows, which renders it instead.
      (err) => {
        if (reqId === reqIdRef.current) {
          setEntries([]);
          setListError(friendlyErrorMessage(err));
        }
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
    runCompletion(childrenPrefix(openingDir(value, fallbackDir)));
    listRecents?.().then(
      (result) => setRecents(result),
      () => setRecents([]),
    );
    return () => {
      clearTimeout(debounceRef.current);
      clearTimeout(justPickedTimeoutRef.current);
    };
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
    // Mark the header just-picked for a beat (94yg) - the pick already
    // registered via onChange above; this only makes that moment visible.
    setJustPicked(true);
    clearTimeout(justPickedTimeoutRef.current);
    justPickedTimeoutRef.current = setTimeout(() => setJustPicked(false), JUST_PICKED_MS);
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
    // decision) server-side.
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
            // The just-picked pulse only ever applies to the current-directory
            // header (isPath), the row a browseInto click actually changed -
            // never the Recent group's own static "Recent projects" title.
            return (
              <li
                key={row.key}
                role="presentation"
                className={`${CLASS.groupRow} ${isPath ? CLASS.groupPath : CLASS.groupLabel}`}
                data-just-picked={isPath && justPicked ? "true" : undefined}
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
 * opens the shared directory dialog or the file completion popover. There is no separate
 * folder/Browse button - the whole field is the affordance.
 */
export function PathField({
  id,
  value,
  onChange,
  kind = "dir",
  complete,
  listRecents,
  fallbackDir,
  onPanelClose,
  placeholder,
  disabled = false,
  ariaLabel,
  directory,
}: PathFieldProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const shownValue = value === "" ? (placeholder ?? "(default)") : value;

  // Restore the field trigger on every exit. Directory cancellation reports
  // the committed value; file browsing retains its live-value contract.
  function closePanel(finalValue: string): void {
    setOpen(false);
    triggerRef.current?.focus();
    onPanelClose?.(finalValue);
  }

  function commit(path: string): void {
    onChange(path);
    closePanel(path);
  }

  const trigger = (
    <button
      ref={triggerRef}
      id={id}
      type="button"
      className={`${CLASS.trigger} ${kind === "dir" ? CLASS.directoryTrigger : ""}`}
      disabled={disabled}
      aria-haspopup={kind === "dir" ? "dialog" : undefined}
      aria-expanded={kind === "dir" ? open : undefined}
      // The label has to carry the value too: aria-label replaces the
      // button's own text, so naming it "Skill directories" alone would
      // hide the path it currently holds.
      aria-label={ariaLabel === undefined ? undefined : `${ariaLabel}: ${shownValue} — browse`}
      onClick={() => (open ? closePanel(value) : setOpen(true))}
    >
      {/* Plain text, not a Chip: the trigger already draws the control's
              own border, and a bordered chip inside it reads as a double
              border. */}
      <span
        className={`${CLASS.triggerValue} ${kind === "dir" ? CLASS.directoryValue : ""} ${value === "" ? CLASS.triggerDefault : ""}`}
      >
        {shownValue}
      </span>
      <span className={CLASS.chevron} aria-hidden="true">
        <Chevron direction="down" />
      </span>{" "}
      {/* That separating space is load-bearing: the accessible name is this
              button's children concatenated, and each child's own text is
              trimmed first, so a space INSIDE either span would be dropped and
              the name would run together as "/home/jesse— browse". A
              whitespace-only text node between them survives that trim and
              renders nothing of its own (an all-whitespace anonymous flex item
              is not laid out). */}
      <span className={CLASS.srOnly}>— browse</span>
    </button>
  );
  if (kind === "dir") {
    if (!directory) throw new Error("Directory fields require validation and creation actions");
    return (
      <>
        {trigger}
        {open && (
          <DirectoryPicker
            key={value}
            value={value}
            fallbackDir={fallbackDir}
            complete={complete}
            listRecents={listRecents}
            validatePath={directory.validatePath}
            createDirectory={directory.createDirectory}
            onPick={commit}
            onClose={() => closePanel(value)}
          />
        )}
      </>
    );
  }

  return (
    <Popover
      open={open}
      // Escape and an outside click both dismiss with whatever browsing left
      // in the field - there is no Cancel to undo it (spec 3.4).
      onClose={() => closePanel(value)}
      // The panel's own list scrolls, and a page scroll behind it must not
      // dismiss it mid-interaction.
      closeOnScroll={false}
      // The panel's input owns focus and its own text selection - see
      // closePanel for why FocusScope must not manage focus here.
      autoFocus={false}
      // The trigger is a form control: it fills its field slot so it lines up
      // with the Input/Select siblings beside it.
      stretchTrigger
      trigger={trigger}
    >
      <div className={CLASS.popoverPanel}>
        <FileFieldPanel
          kind={kind}
          value={value}
          onChange={onChange}
          onCommit={commit}
          complete={complete}
          listRecents={listRecents}
          fallbackDir={fallbackDir}
        />
      </div>
    </Popover>
  );
}
