// The command palette overlay body (T3 fills T1's stub). Mounted once by
// AppShell beside <ToastRegion/>, it subscribes to the shared paletteStore and
// renders the three-mode search/command surface over Wave-5's action layer.
// The overlay chrome (scrim, focus trap, Escape, close button) is the shared
// Dialog widget; this component fills its body with the mode state machine,
// the keyboard-navigable results list, the inline error strip, and the help
// panel - all ported from search.js, adapted to React state instead of
// imperative innerHTML.
import { type KeyboardEvent, useEffect, useMemo, useRef, useState } from "react";
import { WireError } from "../../protocol/errors";
import { type CadenceState, Chip, Dialog, StatusDot, useToasts } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { navigate } from "../routing";
import { isBlocked } from "./blocked";
import styles from "./commandpalette.module.css";
import {
  type Command,
  type CommandArgsEnumItem,
  filterCommands,
  type PaletteRunContext,
  type PaletteUi,
  rememberableId,
  type ScopedCommand,
} from "./commands";
import { computeMode } from "./mode";
import { buildPaletteContext, focusedModel } from "./paletteContext";
import { closePalette, usePaletteStore } from "./paletteController";
import { rememberCommand } from "./recentCommands";
import {
  fetchSearch,
  findInSessionMatches,
  type HighlightPart,
  highlightParts,
  type InSessionMatch,
  type SearchResponse,
  type SearchResult,
} from "./search";

const CLASS = {
  palette: requireClass(styles.palette, "commandpalette.module.css", "palette"),
  pill: requireClass(styles.pill, "commandpalette.module.css", "pill"),
  pillBack: requireClass(styles.pillBack, "commandpalette.module.css", "pillBack"),
  input: requireClass(styles.input, "commandpalette.module.css", "input"),
  error: requireClass(styles.error, "commandpalette.module.css", "error"),
  results: requireClass(styles.results, "commandpalette.module.css", "results"),
  sectionHeader: requireClass(styles.sectionHeader, "commandpalette.module.css", "sectionHeader"),
  row: requireClass(styles.row, "commandpalette.module.css", "row"),
  rowUnavailable: requireClass(styles.rowUnavailable, "commandpalette.module.css", "rowUnavailable"),
  unavailable: requireClass(styles.unavailable, "commandpalette.module.css", "unavailable"),
  glyph: requireClass(styles.glyph, "commandpalette.module.css", "glyph"),
  title: requireClass(styles.title, "commandpalette.module.css", "title"),
  cmdId: requireClass(styles.cmdId, "commandpalette.module.css", "cmdId"),
  hint: requireClass(styles.hint, "commandpalette.module.css", "hint"),
  project: requireClass(styles.project, "commandpalette.module.css", "project"),
  age: requireClass(styles.age, "commandpalette.module.css", "age"),
  snippet: requireClass(styles.snippet, "commandpalette.module.css", "snippet"),
  mark: requireClass(styles.mark, "commandpalette.module.css", "mark"),
  helpRow: requireClass(styles.helpRow, "commandpalette.module.css", "helpRow"),
  helpKeys: requireClass(styles.helpKeys, "commandpalette.module.css", "helpKeys"),
  helpDesc: requireClass(styles.helpDesc, "commandpalette.module.css", "helpDesc"),
  empty: requireClass(styles.empty, "commandpalette.module.css", "empty"),
  emptyTitle: requireClass(styles.emptyTitle, "commandpalette.module.css", "emptyTitle"),
  emptyBody: requireClass(styles.emptyBody, "commandpalette.module.css", "emptyBody"),
};

const SEARCH_PLACEHOLDER = "search live + past sessions";
const SEARCH_DEBOUNCE_MS = 150;

// The 7 fixed help rows (§2.8, search.js:697-705).
const HELP_ROWS: Array<[string, string]> = [
  ["⌘K / Ctrl-K", "open the palette from anywhere"],
  ["/", "at the start of an empty message textarea — opens command mode"],
  ["↑ ↓", "navigate the list"],
  ["↵", "run the highlighted command (or open a search result)"],
  ["⌘↵", "open a search result in a new tab"],
  ["⇧↵", "jump to a turn in the current session"],
  ["Esc", "close the palette (or back out of args mode)"],
];

// Live-row status dot: the search API's normalized state (hubcore.
// NormalizeState) mapped onto the StatusDot widget's CadenceState. Pulsing
// alive/attention/danger dots read as "live" for active/awaiting/errored, the
// exact set the legacy pulsed (search.js:1007-1009); past rows are always
// "ended" (neutral).
function toCadenceState(state: string): CadenceState {
  switch (state) {
    case "active":
      return "working";
    case "awaiting":
    case "warning":
      return "needs-you";
    case "errored":
      return "failed";
    case "ended":
      return "ended";
    default:
      return "idle";
  }
}

// A single navigable row in the results list.
type PaletteItem =
  | { kind: "live"; result: SearchResult }
  | { kind: "past"; result: SearchResult }
  | { kind: "insession"; match: InSessionMatch }
  | { kind: "command"; command: ScopedCommand }
  | { kind: "arg"; item: CommandArgsEnumItem };

// One render entry: a section header or a navigable row carrying its flat
// index (which matches its position in the parallel `items` list, so
// keyboard nav and rendering can never drift apart).
type Entry = { type: "header"; label: string } | { type: "row"; item: PaletteItem; index: number };

interface ResultsView {
  entries: Entry[];
  items: PaletteItem[];
}

// commandErrorMessage extracts a rejected command Promise's message, falling
// back through the stringified error to the literal "command failed"
// (search.js:867-871).
//
// The hubLaunch prefix keeps a failed auto-resume from being read as a failed
// command. A session mutation against a cold session resumes it first
// (app_model.go's setThreadModelWithResume and its siblings); when that
// spawn fails, the hub returns the spawner's own raw text under
// serfErrorInfo "hubLaunch" (appwire.HubLaunchError), which on its own says
// nothing about which of the two steps died - and would send someone
// debugging /goal when the daemon simply would not start.
export function commandErrorMessage(err: unknown): string {
  if (err instanceof WireError && err.serfErrorInfo === "hubLaunch") {
    return `couldn't start this session: ${err.message || "launch failed"}`;
  }
  if (err instanceof Error && err.message) return err.message;
  const msg = String(err ?? "").trim();
  return msg || "command failed";
}

function Highlighted({ parts }: { parts: HighlightPart[] }) {
  return (
    <>
      {parts.map((part, i) =>
        part.mark ? (
          // biome-ignore lint/suspicious/noArrayIndexKey: parts are a fixed positional split of one string, order is their identity
          <mark key={i} className={CLASS.mark}>
            {part.text}
          </mark>
        ) : (
          // biome-ignore lint/suspicious/noArrayIndexKey: parts are a fixed positional split of one string, order is their identity
          <span key={i}>{part.text}</span>
        ),
      )}
    </>
  );
}

export function CommandPalette() {
  const open = usePaletteStore((s) => s.open);
  const query = usePaletteStore((s) => s.query);
  const openSeq = usePaletteStore((s) => s.openSeq);
  return (
    <Dialog open={open} onClose={closePalette} title="Command palette">
      {open ? <PaletteBody key={openSeq} initialQuery={query} /> : null}
    </Dialog>
  );
}

function PaletteBody({ initialQuery }: { initialQuery: string }) {
  const toasts = useToasts();
  const inputRef = useRef<HTMLInputElement>(null);

  const [query, setQuery] = useState(initialQuery);
  const [selectedCommand, setSelectedCommand] = useState<Command | null>(null);
  const [preArgsFilter, setPreArgsFilter] = useState("/");
  const [error, setError] = useState<string | null>(null);
  const [activeIndex, setActiveIndex] = useState(0);
  const [showingHelp, setShowingHelp] = useState(false);
  const [searchResp, setSearchResp] = useState<SearchResponse | null>(null);
  const [searchFailed, setSearchFailed] = useState(false);
  const [enumStatus, setEnumStatus] = useState<"idle" | "loading" | "loaded" | "error">("idle");
  const [enumItems, setEnumItems] = useState<CommandArgsEnumItem[]>([]);
  const searchTokenRef = useRef(0);

  // The palette's context is fixed at open time - focus is trapped inside the
  // overlay, so the focused session can't change while it's open. Computing it
  // once on mount keeps a stable reference for the view memo below (a fresh
  // object each render would defeat that memo and reset the active row on
  // every keystroke). Command runs still read the live ThreadModel fresh via
  // focusedModel(), so turn-state guards stay current. (buildPaletteContext is
  // a stable module import, so the empty dep array needs no suppression.)
  const ctx = useMemo(() => buildPaletteContext(), []);
  const mode = computeMode({ query, hasSelectedCommand: selectedCommand !== null });

  const ui: PaletteUi = {
    clearToSearch: () => {
      setSelectedCommand(null);
      setShowingHelp(false);
      setQuery("");
      inputRef.current?.focus();
    },
    showHelp: () => {
      setShowingHelp(true);
      inputRef.current?.focus();
    },
  };

  function runContext(): PaletteRunContext {
    return { ...ctx, toasts, ui };
  }

  // Debounced search (§2.3): an empty query clears locally with no backend
  // call; otherwise a stale-drop token guards out-of-order responses.
  useEffect(() => {
    if (mode !== "search") return;
    const q = query.trim();
    if (!q) {
      setSearchResp(null);
      setSearchFailed(false);
      return;
    }
    const timer = setTimeout(() => {
      const token = ++searchTokenRef.current;
      fetchSearch(q).then(
        (resp) => {
          if (searchTokenRef.current === token) {
            setSearchResp(resp);
            setSearchFailed(false);
          }
        },
        () => {
          if (searchTokenRef.current === token) {
            setSearchResp(null);
            setSearchFailed(true);
          }
        },
      );
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [mode, query]);

  // Load an enum command's option source once on entering args mode (§2.6): a
  // thenable shows Loading… then resolves to options or a "couldn't load"
  // error; a plain array resolves synchronously.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runContext is recreated every render; the source loads once, keyed on the selected command's identity
  useEffect(() => {
    if (selectedCommand?.args?.kind !== "enum") {
      setEnumStatus("idle");
      setEnumItems([]);
      return;
    }
    let cancelled = false;
    let value: CommandArgsEnumItem[] | Promise<CommandArgsEnumItem[]>;
    try {
      value = selectedCommand.args.source(runContext());
    } catch {
      value = [];
    }
    if (typeof (value as Promise<CommandArgsEnumItem[]>).then === "function") {
      setEnumStatus("loading");
      setEnumItems([]);
      (value as Promise<CommandArgsEnumItem[]>).then(
        (list) => {
          if (!cancelled) {
            setEnumItems(list);
            setEnumStatus("loaded");
          }
        },
        () => {
          if (!cancelled) {
            setEnumItems([]);
            setEnumStatus("error");
          }
        },
      );
    } else {
      setEnumItems(value as CommandArgsEnumItem[]);
      setEnumStatus("loaded");
    }
    return () => {
      cancelled = true;
    };
  }, [selectedCommand]);

  // The results view (rows + parallel nav list), rebuilt only when its data
  // deps change - never on a bare activeIndex change, so arrow-key navigation
  // doesn't reset the selection (see the reset effect below).
  const view = useMemo<ResultsView>(
    () => buildView({ mode, query, ctx, searchResp, selectedCommand, enumItems, showingHelp }),
    [mode, query, ctx, searchResp, selectedCommand, enumItems, showingHelp],
  );

  // Reset the active row whenever the row list is rebuilt (§2.3/§2.4:
  // active = first row, or none when empty). Keyed on the view memo, which
  // only changes when the data deps do - never on a bare activeIndex change.
  useEffect(() => {
    setActiveIndex(view.items.length > 0 ? 0 : -1);
  }, [view]);

  function move(dir: number) {
    if (view.items.length === 0) return;
    setActiveIndex((prev) => (prev + dir + view.items.length) % view.items.length);
  }

  function enterArgsMode(command: Command) {
    setPreArgsFilter(query.startsWith("/") ? query : "/");
    setSelectedCommand(command);
    setQuery("");
    setShowingHelp(false);
    inputRef.current?.focus();
  }

  function exitArgsMode() {
    setSelectedCommand(null);
    setQuery(preArgsFilter || "/");
    inputRef.current?.focus();
  }

  function finishSuccess(opts: { closeOnSuccess: boolean; rememberId: string }) {
    if (opts.rememberId) rememberCommand(opts.rememberId);
    if (opts.closeOnSuccess) closePalette();
  }

  function handleCommandResult(result: unknown, opts: { closeOnSuccess: boolean; rememberId: string }) {
    if (isBlocked(result)) {
      setError(result.message);
      return;
    }
    if (result && typeof (result as Promise<unknown>).then === "function") {
      (result as Promise<unknown>).then(
        (value) => {
          if (isBlocked(value)) {
            setError(value.message);
            return;
          }
          finishSuccess(opts);
        },
        (err) => setError(commandErrorMessage(err)),
      );
      return;
    }
    finishSuccess(opts);
  }

  // activateCommand is the single entry point for "the user chose this row",
  // shared by Enter and click. An unavailable command stays selectable and
  // answers with its reason in the error strip rather than being unreachable:
  // a row you can see but cannot land on is as confusing as a missing one, and
  // silently running the NEXT command instead would be worse than either.
  function activateCommand(command: ScopedCommand) {
    if (command.unavailableReason) {
      setError(`/${command.id} is ${command.unavailableReason}`);
      return;
    }
    if (command.args) enterArgsMode(command);
    else runArgless(command);
  }

  function runArgless(command: Command) {
    let result: unknown;
    try {
      result = command.run?.(runContext());
    } catch {
      // A synchronous throw is swallowed before handling, mirroring the legacy
      // (search.js:828) - a command that throws still surfaces via its
      // rejected-Promise path if it has one.
    }
    handleCommandResult(result, { closeOnSuccess: !command.stayOpen, rememberId: rememberableId(command) });
  }

  function runWithArg(command: Command, arg: string | CommandArgsEnumItem) {
    if (!command.args) return;
    let result: unknown;
    try {
      result =
        command.args.kind === "enum"
          ? command.args.run(runContext(), arg as CommandArgsEnumItem)
          : command.args.run(runContext(), arg as string);
    } catch {
      // Same swallow as runArgless.
    }
    handleCommandResult(result, { closeOnSuccess: true, rememberId: "" });
  }

  function activateResult(item: PaletteItem, newTab: boolean) {
    if (item.kind === "insession") {
      // Best-effort: focus stays on the already-focused session pane; precise
      // scroll-to-hit in the virtualized transcript is beyond-parity (flagged
      // in the sweep).
      closePalette();
      return;
    }
    if (item.kind === "live" || item.kind === "past") {
      closePalette();
      // Open by the qualified ref when the hub provides one; fall back to the
      // bare id for old hubs that don't (I3). The session route resolves a
      // ref, not a bare session id.
      const url = `/s/${encodeURIComponent(item.result.ref || item.result.id)}`;
      if (newTab) window.open(url, "_blank");
      else navigate(url);
    }
  }

  function enterPressed() {
    const item = view.items[activeIndex];
    if (mode === "search") {
      if (item) activateResult(item, false);
      return;
    }
    if (mode === "command-filter") {
      if (item?.kind !== "command") return;
      activateCommand(item.command);
      return;
    }
    // command-args
    if (!selectedCommand?.args) return;
    if (selectedCommand.args.kind === "enum") {
      if (item?.kind !== "arg") return;
      runWithArg(selectedCommand, item.item);
    } else {
      runWithArg(selectedCommand, query);
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      move(1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      move(-1);
    } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      if (mode === "search") {
        e.preventDefault();
        const item = view.items[activeIndex];
        if (item) activateResult(item, true);
      }
    } else if (e.key === "Enter" && e.shiftKey) {
      if (mode === "search") {
        e.preventDefault();
        const item = view.items[activeIndex];
        if (item?.kind === "insession") activateResult(item, false);
      }
    } else if (e.key === "Enter") {
      e.preventDefault();
      enterPressed();
    } else if (e.key === "Escape" && selectedCommand) {
      // Esc from args mode backs OUT to command-filter, it does not close the
      // dialog (§2.1). stopPropagation keeps it from reaching OverlayPanel's
      // own Escape-to-close handler on the panel; every other Escape bubbles
      // there and closes.
      e.preventDefault();
      e.stopPropagation();
      exitArgsMode();
    }
  }

  const activeId = activeIndex >= 0 && activeIndex < view.items.length ? `palette-row-${activeIndex}` : undefined;
  const placeholder = selectedCommand?.args ? selectedCommand.args.placeholder : SEARCH_PLACEHOLDER;

  return (
    <div className={CLASS.palette}>
      {selectedCommand && (
        <div className={CLASS.pill}>
          <span>{selectedCommand.title}</span>
          <button type="button" className={CLASS.pillBack} aria-label="back to command list" onClick={exitArgsMode}>
            ×
          </button>
        </div>
      )}
      <input
        ref={inputRef}
        // biome-ignore lint/a11y/noAutofocus: a command palette's whole purpose is an immediately-typable input on open
        autoFocus
        className={CLASS.input}
        type="text"
        role="combobox"
        aria-label="Command palette"
        aria-expanded="true"
        aria-controls="palette-results"
        aria-activedescendant={activeId}
        placeholder={placeholder}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          // Typing anything leaves the help panel and resumes filtering (§2.8).
          setShowingHelp(false);
        }}
        onKeyDown={onKeyDown}
      />
      {error && (
        <div className={CLASS.error} role="alert">
          <Chip tone="danger">{error}</Chip>
        </div>
      )}
      <div className={CLASS.results} id="palette-results" role="listbox" aria-label="Palette results">
        {renderResults({
          mode,
          query,
          view,
          showingHelp,
          selectedCommand,
          enumStatus,
          searchFailed,
          activeIndex,
          onActivate: activateFromClick,
        })}
      </div>
    </div>
  );

  function activateFromClick(index: number) {
    setActiveIndex(index);
    const item = view.items[index];
    if (!item) return;
    if (mode === "search") {
      activateResult(item, false);
      return;
    }
    if (mode === "command-filter" && item.kind === "command") {
      activateCommand(item.command);
      return;
    }
    if (mode === "command-args" && item.kind === "arg" && selectedCommand) {
      runWithArg(selectedCommand, item.item);
    }
  }
}

// buildView computes the ordered rows for the current mode plus the parallel
// flat nav list. Section headers carry no index; rows carry the flat index
// matching their position in `items`.
function buildView(args: {
  mode: ReturnType<typeof computeMode>;
  query: string;
  ctx: ReturnType<typeof buildPaletteContext>;
  searchResp: SearchResponse | null;
  selectedCommand: Command | null;
  enumItems: CommandArgsEnumItem[];
  showingHelp: boolean;
}): ResultsView {
  const { mode, query, ctx, searchResp, selectedCommand, enumItems, showingHelp } = args;
  const entries: Entry[] = [];
  const items: PaletteItem[] = [];
  const push = (item: PaletteItem) => {
    entries.push({ type: "row", item, index: items.length });
    items.push(item);
  };

  // The help panel is inert (§2.8, search.js:695-696,713): no navigable rows
  // underneath, so ArrowDown/Enter can't fire a hidden registry command.
  // Typing clears showingHelp (input onChange), which returns a real list.
  if (showingHelp) return { entries, items };

  if (mode === "search") {
    const live = searchResp?.live ?? [];
    const past = searchResp?.past ?? [];
    const model = focusedModel(ctx.sessionRef);
    const inSession = model && query.trim() ? findInSessionMatches(model, query.trim()) : [];
    if (live.length) {
      entries.push({ type: "header", label: "Live" });
      for (const result of live) push({ kind: "live", result });
    }
    if (past.length) {
      entries.push({ type: "header", label: `Past · ${past.length}` });
      for (const result of past) push({ kind: "past", result });
    }
    if (inSession.length) {
      entries.push({ type: "header", label: `In session · ${inSession.length}` });
      for (const match of inSession) push({ kind: "insession", match });
    }
    return { entries, items };
  }

  if (mode === "command-filter") {
    const { recent, commands } = filterCommands(ctx, query);
    if (recent.length) {
      entries.push({ type: "header", label: "Recent" });
      for (const command of recent) push({ kind: "command", command });
    }
    entries.push({ type: "header", label: "Commands" });
    for (const command of commands) push({ kind: "command", command });
    return { entries, items };
  }

  // command-args (enum only produces rows; free-arg mode has none).
  if (selectedCommand?.args?.kind === "enum") {
    const q = query.toLowerCase().trim();
    const filtered = q
      ? enumItems.filter((it) => it.label.toLowerCase().includes(q) || it.id.toLowerCase().includes(q))
      : enumItems;
    for (const it of filtered) push({ kind: "arg", item: it });
  }
  return { entries, items };
}

function renderResults(args: {
  mode: ReturnType<typeof computeMode>;
  query: string;
  view: ResultsView;
  showingHelp: boolean;
  selectedCommand: Command | null;
  enumStatus: "idle" | "loading" | "loaded" | "error";
  searchFailed: boolean;
  activeIndex: number;
  onActivate: (index: number) => void;
}) {
  const { mode, query, view, showingHelp, selectedCommand, enumStatus, searchFailed, activeIndex, onActivate } = args;

  if (showingHelp) {
    return (
      <>
        <div className={CLASS.sectionHeader}>Keyboard shortcuts</div>
        {HELP_ROWS.map(([keys, desc]) => (
          <div className={CLASS.helpRow} key={keys}>
            <span className={CLASS.helpKeys}>{keys}</span>
            <span className={CLASS.helpDesc}>{desc}</span>
          </div>
        ))}
      </>
    );
  }

  if (view.items.length > 0) {
    return view.entries.map((entry) =>
      entry.type === "header" ? (
        <div className={CLASS.sectionHeader} key={`h-${entry.label}`}>
          {entry.label}
        </div>
      ) : (
        <Row
          key={`r-${entry.index}`}
          item={entry.item}
          index={entry.index}
          query={query}
          active={entry.index === activeIndex}
          onActivate={onActivate}
        />
      ),
    );
  }

  // No rows: the mode-specific placeholder.
  if (mode === "search") {
    if (searchFailed) return <Empty title="Search failed" />;
    if (!query.trim()) return null;
    return <Empty title="No matches" body="Nothing in live, past, or this session." />;
  }
  if (mode === "command-filter") {
    return <Empty title="No commands match" body="Try a different keyword or open a session first." />;
  }
  // command-args
  if (selectedCommand?.args?.kind === "enum") {
    if (enumStatus === "loading") return <Empty body="Loading…" />;
    if (enumStatus === "error")
      return <Empty title="Couldn't load options" body="Something went wrong. Close and reopen to retry." />;
    return <Empty title="No matches" />;
  }
  // free-arg hint
  return <Empty body={query.trim() ? `press ↵ to run with: ${query}` : "type a value and press ↵"} />;
}

function Empty({ title, body }: { title?: string; body?: string }) {
  return (
    <div className={CLASS.empty}>
      {title && <p className={CLASS.emptyTitle}>{title}</p>}
      {body && <p className={CLASS.emptyBody}>{body}</p>}
    </div>
  );
}

function Row({
  item,
  index,
  query,
  active,
  onActivate,
}: {
  item: PaletteItem;
  index: number;
  query: string;
  active: boolean;
  onActivate: (index: number) => void;
}) {
  // aria-disabled, never the disabled attribute: the row must stay focusable
  // and activatable so choosing it explains itself (activateCommand), instead
  // of swallowing the keystroke.
  const unavailable = item.kind === "command" && item.command.unavailableReason !== undefined;
  return (
    <button
      type="button"
      id={`palette-row-${index}`}
      className={unavailable ? `${CLASS.row} ${CLASS.rowUnavailable}` : CLASS.row}
      role="option"
      aria-selected={active}
      aria-disabled={unavailable || undefined}
      onClick={() => onActivate(index)}
    >
      <RowContent item={item} query={query} />
    </button>
  );
}

function RowContent({ item, query }: { item: PaletteItem; query: string }) {
  if (item.kind === "command") {
    return (
      <>
        <span className={CLASS.glyph} aria-hidden="true">
          /
        </span>
        <span className={CLASS.title}>{item.command.title}</span>
        <span className={CLASS.cmdId}>/{item.command.id}</span>
        {/* The reason takes the hint's place rather than sitting beside it:
            on a row the user cannot run, why is the only thing worth the
            width. */}
        {item.command.unavailableReason ? (
          <span className={CLASS.unavailable}>{item.command.unavailableReason}</span>
        ) : (
          item.command.hint && <span className={CLASS.hint}>{item.command.hint}</span>
        )}
      </>
    );
  }
  if (item.kind === "arg") {
    return (
      <>
        <span className={CLASS.title}>{item.item.label || item.item.id}</span>
        {item.item.hint && <span className={CLASS.hint}>{item.item.hint}</span>}
      </>
    );
  }
  if (item.kind === "insession") {
    return (
      <>
        <span className={CLASS.glyph} aria-hidden="true">
          ↳
        </span>
        <span className={CLASS.snippet}>
          <Highlighted parts={item.match.snippet} />
        </span>
        <span className={CLASS.age}>turn {item.match.turn}</span>
      </>
    );
  }
  // live / past: the title and project get <mark> around the query match
  // (§2.3, search.js:994-1013).
  return (
    <>
      <StatusDot state={toCadenceState(item.result.state)} />
      <span className={CLASS.title}>
        <Highlighted parts={highlightParts(item.result.title, query)} />
      </span>
      {item.result.project && (
        <span className={CLASS.project}>
          <Highlighted parts={highlightParts(item.result.project, query)} />
        </span>
      )}
      {item.result.age && <span className={CLASS.age}>{item.result.age}</span>}
    </>
  );
}
