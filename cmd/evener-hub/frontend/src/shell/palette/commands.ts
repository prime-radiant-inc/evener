// The command registry: the 22-command palette (23 real entries; /fork is
// intentionally omitted - see below) ported from search.js:326-518. Every
// session mutation goes through Wave-5's threadsStore actions (each already
// Conflict-typed), navigation through shell/routing, /theme through
// prefsStore.setTheme (the hazard-#1 FIX: visible immediately, unlike the
// legacy's dead body-class toggle), /project through the rail's imperative
// reveal seam, /upgrade through the evener/upgrade wire method. Failures surface
// either as a blocked sentinel (inline .palette-error strip, palette stays
// open) for an idle-guarded action or a Conflict, or as a useToasts() toast
// for a fire-and-report action - never a silent swallow.

import type { ThreadModel } from "../../protocol/model";
import type { CommandDescriptor, ThreadCapabilities } from "../../protocol/types.gen";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { selectNeedsYouRows } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";
import { prefsStore } from "../../stores/prefs";
import { threadsStore } from "../../stores/threads";
import type { ToastKind } from "../../widgets";
import { modelListToCatalog } from "../../widgets/modelCatalog/catalogClient";
import { needsYouRefs, nextNeedsYouRef, openNeedsYouSession } from "../rail/needsYouCycle";
import { revealSessionInRail } from "../rail/railController";
import { navigate } from "../routing";
import { workspaceStore } from "../workspace";
import { blocked } from "./blocked";
import { commandScore } from "./commandScore";
import { focusedModel, hasActiveTurn, type OnPage, type PaletteContext } from "./paletteContext";
import { readRecentCommandIds } from "./recentCommands";

// A command is either global (no session needed) or session-scoped (a session
// pane must be focused). There is deliberately no third "works on an ended
// session too" scope: whether a session command can run is the hub's per-
// action capability flag, which it publishes for cold exited threads as
// readily as for live ones - see commandsInScope.
//
// CommandScope doubles as the 2026-08-14 surface split (decisions.md): "the
// palette is where you go; the composer is where you act on this session".
// A "session" command - every mutation or read that acts on the focused
// session, built-in (goal, model, effort, status, compact, steer, queue,
// clear, compact, interrupt, shutdown, aside, drain-as-steer, copy-id,
// tasks, project) or plugin-catalog - now executes ONLY from the session's
// own composer (panes/session/composer/builtinCommand.ts's interception,
// SlashCompletionMenu's merged catalog): the palette delists it and offers a
// one-row handoff instead (CommandPalette.tsx's buildView). A "global"
// command (new session, spawn, settings, theme, dashboard, search, help,
// upgrade, next-needs-you) needs no session and stays palette-native, exactly
// as before. commandSurface below is the one place that names this mapping,
// so a reader never has to infer "session scope" and "composer surface" are
// the same fact from two different modules independently.
export type CommandScope = "global" | "session";

export type CommandSurface = "session" | "app-global";

// commandSurface: which UI surface owns running this command. Derived
// straight from `scope`, not a second independently-set field - the two
// have been the same fact since the 2026-08-14 decision (see CommandScope's
// own doc comment above), and a parallel field would only invite the two to
// drift. sessionBuiltinCommands (composer's own resolved list) and the
// palette's delisting/handoff logic (CommandPalette.tsx) both read this
// instead of comparing against the literal string "session" themselves.
export function commandSurface(command: Pick<Command, "scope">): CommandSurface {
  return command.scope === "session" ? "session" : "app-global";
}

export interface CommandArgsEnumItem {
  id: string;
  label: string;
  hint?: string;
}

// A command run handler may return: undefined (fire-and-forget, close on
// success), a blocked sentinel (stay open, show the message), a value (close
// on success), or a Promise of any of those (a rejection surfaces as the
// error strip). Deliberately `unknown`, mirroring the legacy's heterogeneous
// run() returns - handleCommandResult in CommandPalette.tsx sorts them out.
export type CommandResult = unknown;

export interface PaletteUi {
  // /search: clear the input back to empty search mode and refocus.
  clearToSearch(): void;
  // /help: render the keyboard-shortcuts panel.
  showHelp(): void;
}

export interface PaletteRunContext extends PaletteContext {
  toasts: { push(kind: ToastKind, text: string): void };
  ui: PaletteUi;
}

export interface CommandArgsFree {
  kind: "free";
  placeholder: string;
  run(ctx: PaletteRunContext, text: string): CommandResult;
}

export interface CommandArgsEnum {
  kind: "enum";
  placeholder: string;
  source(ctx: PaletteRunContext): CommandArgsEnumItem[] | Promise<CommandArgsEnumItem[]>;
  run(ctx: PaletteRunContext, item: CommandArgsEnumItem): CommandResult;
}

export type CommandArgs = CommandArgsFree | CommandArgsEnum;

export interface Command {
  id: string;
  title: string;
  hint: string;
  description?: string;
  source?: string;
  pluginName?: string;
  keywords: string[];
  scope: CommandScope;
  // The ThreadCapabilities flag the hub publishes for this command's action,
  // and the sole authority on whether it can run against the focused session.
  // Absent means the hub gates the action on nothing either (/reasoning-effort
  // has no capability field - see app_rpc.go's "No capability gate" note - and
  // the read-only /copy-id, /tasks, /status, /project touch no session state
  // at all), so the command is always offered once a session is focused.
  capability?: keyof ThreadCapabilities;
  // stayOpen commands (/search, /help) never close and never record recency.
  stayOpen?: boolean;
  args?: CommandArgs;
  slashCommandInvocation?: string;
  run?(ctx: PaletteRunContext): CommandResult;
}

// A registry Command resolved against the focused session. unavailableReason
// is set when the wire's capability flag for this command is false: the row
// still renders (disabled, carrying the reason) rather than vanishing, so a
// keyboard user's motor pattern survives and a missing command is never
// indistinguishable from one they misremembered.
export interface ScopedCommand extends Command {
  unavailableReason?: string;
}

// The one reason text. It says "right now" rather than naming a cause because
// a boolean is all the wire gives us, and it is temporal for a live session
// (mid-turn /clear) as much as it is for a cold or foreign-source one.
export const UNAVAILABLE_REASON = "not available right now";

// splitModelId reconstructs a ModelDescriptor {provider, model} from the
// "provider/model" id the /model enum item carries. A provider never contains
// "/", so splitting on the FIRST "/" preserves a model id that itself has
// slashes. threadsStore.setModel takes the two halves separately (unlike the
// legacy REST shim, which took the joined string).
export function splitModelId(id: string): { provider: string; model: string } {
  const slash = id.indexOf("/");
  if (slash < 0) return { provider: id, model: "" };
  return { provider: id.slice(0, slash), model: id.slice(slash + 1) };
}

// /tasks and /status used to branch on isMobileViewport(): desktop toggled the
// workspace pane, mobile synthesized a click on the session chrome's trigger
// button (search.js:511-514's `if (btn) btn.click()`). The unified SessionMenu
// now owns Details/Tasks/Activity at every width, so those triggers never
// render and the mobile path was a guaranteed no-op. Like the rail adapter,
// both commands now toggle the workspace pane on ALL viewports.
function toggleSessionPane(ctx: PaletteRunContext, type: "sessionTasks" | "sessionDetails"): void {
  if (ctx.sessionRef) workspaceStore.getState().togglePane(type, { ref: ctx.sessionRef });
}

// copyToClipboard prefers the async Clipboard API, falling back to a hidden-
// textarea execCommand("copy") for non-secure contexts (search.js:524-548).
// Rejects only if both paths fail.
export function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text).catch(() => execCopyFallback(text));
  }
  return execCopyFallback(text);
}

function execCopyFallback(text: string): Promise<void> {
  return new Promise((resolve, reject) => {
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "absolute";
      ta.style.left = "-9999px";
      document.body.appendChild(ta);
      ta.select();
      const ok = typeof document.execCommand === "function" && document.execCommand("copy");
      document.body.removeChild(ta);
      if (ok) resolve();
      else reject(new Error("execCommand copy returned false"));
    } catch (err) {
      reject(err instanceof Error ? err : new Error(String(err)));
    }
  });
}

function upgrade(ctx: PaletteRunContext): CommandResult {
  const client = connectionStore.getState().client;
  if (!client) return blocked("upgrade failed: not connected");
  return client.request("evener/upgrade", { requested: "" }).then(
    (resp) => {
      ctx.toasts.push("success", `Evener upgraded to ${resp.channel || "current channel"}`);
      if (resp.restartMessage) ctx.toasts.push("info", resp.restartMessage);
    },
    (err) => {
      ctx.toasts.push("error", "Upgrade failed");
      throw err;
    },
  );
}

// buildCommands rebuilds the registry fresh on every call (search.js:326:
// "The command registry is rebuilt fresh on every call"). All 24 entries: 9
// global (8 from search.js plus the UX-fix "Go to next session needing
// you"), 11 session mutations, 4 read-only session commands. /fork is
// intentionally OMITTED (floor §2.5, search.js:497-499) - it needs an edited
// message the palette has no UI to collect; the transcript row's own edit
// affordance is the entry point.
export function buildCommands(): Command[] {
  return [
    // --- global ---
    {
      id: "new",
      title: "New session",
      hint: "blank session form",
      keywords: [],
      scope: "global",
      run: () => {
        navigate("/new");
      },
    },
    {
      id: "spawn",
      title: "Start with prompt",
      hint: "new session, prefilled",
      keywords: ["start", "spawn"],
      scope: "global",
      args: {
        kind: "free",
        placeholder: "prompt to start…",
        run: (_ctx, text) => {
          navigate(`/new?prompt=${encodeURIComponent(text || "")}`);
        },
      },
    },
    {
      id: "settings",
      title: "Open settings",
      hint: "",
      keywords: ["prefs"],
      scope: "global",
      run: () => {
        navigate("/settings");
      },
    },
    {
      id: "theme",
      title: "Switch theme",
      hint: "dark/light",
      keywords: [],
      scope: "global",
      args: {
        kind: "enum",
        placeholder: "choose a theme…",
        source: () => [
          { id: "dark", label: "Dark" },
          { id: "light", label: "Light" },
        ],
        // The hazard-#1 FIX (parity-m6-surfaces.md §4.4): setTheme sets
        // data-theme immediately and persists, so the palette theme switch is
        // visible now, not silently deferred to the next full page load the
        // way the legacy's dead body-class toggle was.
        run: (_ctx, item) => {
          prefsStore.getState().setTheme(item.id === "light" ? "light" : "dark");
        },
      },
    },
    {
      id: "dashboard",
      title: "Go to dashboard",
      hint: "",
      keywords: ["home"],
      scope: "global",
      run: () => {
        navigate("/");
      },
    },
    {
      id: "search",
      title: "Search sessions",
      hint: "clear / and search",
      keywords: ["find"],
      scope: "global",
      stayOpen: true,
      run: (ctx) => {
        ctx.ui.clearToSearch();
      },
    },
    {
      id: "help",
      title: "Show keyboard shortcuts",
      hint: "TUI parity reference",
      keywords: ["?", "keys", "shortcuts", "hotkey"],
      scope: "global",
      stayOpen: true,
      run: (ctx) => {
        ctx.ui.showHelp();
      },
    },
    {
      id: "upgrade",
      title: "Upgrade Evener",
      hint: "current channel",
      keywords: ["update", "snapshot", "release"],
      scope: "global",
      run: (ctx) => upgrade(ctx),
    },
    // UX fix: cycles the needs-you sessions in tree order, wrapping from
    // whichever session is currently focused (Mod+J's own palette-visible
    // counterpart - see AppShell.tsx's chord and needsYouCycle.ts, which
    // both this and it share).
    {
      id: "next-needs-you",
      title: "Go to next session needing you",
      hint: "cycle needs-you sessions",
      keywords: ["needs you", "attention", "next"],
      scope: "global",
      run: (ctx) => {
        const next = nextNeedsYouRef(needsYouRefs(selectNeedsYouRows(navigationStore.getState())), ctx.sessionRef);
        if (next !== null) openNeedsYouSession(next);
      },
    },

    // --- session: mutations, each carrying the capability that authorizes it ---
    {
      id: "compact",
      title: "Compact transcript",
      hint: "free up token space",
      keywords: ["compress"],
      scope: "session",
      capability: "compact",
      run: (ctx) => (ctx.sessionRef ? threadsStore.getState().compact(ctx.sessionRef) : undefined),
    },
    {
      id: "interrupt",
      title: "Interrupt agent",
      hint: "cancel in-flight turn",
      keywords: ["cancel", "stop"],
      scope: "session",
      capability: "interrupt",
      // No hasActiveTurn gate, for the same reason /model has no busy gate
      // below: only the daemon knows. turn/interrupt names no turn (appwire v3)
      // and its precondition is the session's own quiescence, so answering "is
      // a turn in flight" here can only refuse a Stop the daemon would have
      // taken. activeTurnId is missing in states the wire really reaches -- a
      // session holding queued work reports active with no turn running (kata
      // vewa/5gdv).
      run: (ctx) => {
        if (!ctx.sessionRef) return blocked("interrupt failed: no session");
        return threadsStore.getState().interrupt(ctx.sessionRef);
      },
    },
    {
      id: "clear",
      title: "Clear context",
      hint: "start fresh in this session",
      keywords: [],
      scope: "session",
      capability: "clear",
      run: (ctx) => (ctx.sessionRef ? threadsStore.getState().clearThread(ctx.sessionRef) : undefined),
    },
    {
      id: "aside",
      title: "Aside: fork to side thread",
      hint: "side question, same permissions",
      keywords: ["fork", "side"],
      scope: "session",
      capability: "forkFromTurn",
      run: (ctx) => {
        if (!ctx.sessionRef) return undefined;
        return threadsStore
          .getState()
          .forkFromTurn(ctx.sessionRef, { aside: true })
          .then((resp) => {
            workspaceStore.getState().openPane("session", { ref: resp.thread.evener.ref });
          });
      },
    },
    {
      id: "shutdown",
      title: "Shut down daemon",
      hint: "ends this session",
      keywords: ["kill"],
      scope: "session",
      capability: "shutdown",
      run: (ctx) => {
        if (!ctx.sessionRef) return undefined;
        return threadsStore
          .getState()
          .shutdown(ctx.sessionRef)
          .then(
            () => {
              ctx.toasts.push("success", "Session shut down");
            },
            (err) => {
              ctx.toasts.push("error", "Shutdown failed");
              throw err;
            },
          );
      },
    },
    {
      id: "model",
      title: "Switch model",
      hint: "",
      keywords: [],
      scope: "session",
      capability: "changeModel",
      args: {
        kind: "enum",
        placeholder: "choose a model…",
        // The launchable SET and metadata come from the session-cached
        // model/list response, which is the same catalog ModelSwitch uses.
        source: async () => {
          const catalog = modelListToCatalog(await threadsStore.getState().listModels());
          return catalog.models.map((m) => ({
            id: `${m.provider}/${m.model}`,
            label: m.displayName,
            hint: m.provider,
          }));
        },
        // No client-side turn-in-flight guard: only the daemon knows, and it
        // answers. It resumes a cold session behind the call and retries
        // (app_model.go's setThreadModelWithResume), and refuses a genuine
        // mid-turn switch with a Conflict (server/appwire_runtime.go's
        // handleAppThreadModelSet), which surfaces below as the error toast
        // plus the palette's own error strip. Guessing here refused switches
        // the hub would have accepted - the same mistake ModelSwitch.tsx made.
        run: (ctx, item) => {
          if (!ctx.sessionRef) return undefined;
          const { provider, model: modelId } = splitModelId(item.id);
          return threadsStore
            .getState()
            .setModel(ctx.sessionRef, provider, modelId)
            .then(
              () => {
                ctx.toasts.push("success", `Model: ${item.id}`);
              },
              (err) => {
                ctx.toasts.push("error", "Model change failed");
                throw err;
              },
            );
        },
      },
    },
    {
      id: "reasoning-effort",
      title: "Set reasoning effort",
      hint: "",
      keywords: ["effort", "reasoning", "thinking"],
      scope: "session",
      args: {
        kind: "enum",
        placeholder: "choose effort…",
        // Snapshot-based (the focused model's own reasoningEffortLevels /
        // supportsReasoning), not a separate catalog request - the live
        // surface shouldn't need it (floor §2.5). A non-reasoning model yields ZERO options, not
        // just "(default)". "none" is omitted from a non-empty ladder: it
        // normalizes to "" (same as default), so it isn't a distinct option.
        source: (ctx) => {
          const model = focusedModel(ctx.sessionRef);
          const levels = model?.supportsReasoning ? model.reasoningEffortLevels.filter((l) => l !== "none") : [];
          if (!levels.length) return [];
          return [{ id: "", label: "(default)" }, ...levels.map((l) => ({ id: l, label: l }))];
        },
        run: (ctx, item) => {
          if (!ctx.sessionRef) return undefined;
          const eff = item.id || "";
          return threadsStore
            .getState()
            .setReasoningEffort(ctx.sessionRef, eff)
            .then(
              () => {
                ctx.toasts.push("success", `Effort: ${eff || "default"}`);
              },
              (err) => {
                ctx.toasts.push("error", "Effort change failed");
                throw err;
              },
            );
        },
      },
    },
    {
      id: "steer",
      title: "Steer model",
      hint: "inject mid-turn",
      keywords: [],
      scope: "session",
      capability: "steer",
      args: {
        kind: "free",
        placeholder: "steer text…",
        run: (ctx, text) => {
          const model = focusedModel(ctx.sessionRef);
          if (!ctx.sessionRef || !model || !hasActiveTurn(model)) return blocked("steer failed: no active turn");
          return threadsStore.getState().steer(ctx.sessionRef, text);
        },
      },
    },
    {
      id: "queue",
      title: "Queue message",
      hint: "process after active turn",
      keywords: ["enqueue"],
      scope: "session",
      capability: "queue",
      args: {
        kind: "free",
        placeholder: "queue text…",
        run: (ctx, text) => {
          const model = focusedModel(ctx.sessionRef);
          if (!ctx.sessionRef || !model || !hasActiveTurn(model)) return blocked("queue failed: no active turn");
          return threadsStore.getState().queue(ctx.sessionRef, text);
        },
      },
    },
    {
      id: "goal",
      title: "Set session goal",
      hint: "agent pursues until done",
      keywords: ["objective", "pursue"],
      scope: "session",
      capability: "goal",
      args: {
        kind: "free",
        placeholder: "objective… (empty to clear)",
        run: async (ctx, text) => {
          if (!ctx.sessionRef) return undefined;
          const ref = ctx.sessionRef;
          const objective = (text || "").trim();
          return threadsStore.getState().setGoal(ref, objective);
        },
      },
    },
    {
      id: "drain-as-steer",
      title: "Drain queue as steering",
      hint: "force-steer combined action",
      keywords: ["force-steer", "drain"],
      scope: "session",
      capability: "steer",
      run: (ctx) => {
        const model = focusedModel(ctx.sessionRef);
        if (!ctx.sessionRef || !model || !hasActiveTurn(model)) return blocked("drain failed: no active turn");
        return threadsStore.getState().drainAsSteer(ctx.sessionRef, "");
      },
    },

    // --- session: read-only, no capability to gate on ---
    {
      id: "copy-id",
      title: "Copy session ID",
      hint: "clipboard",
      keywords: ["clipboard"],
      scope: "session",
      run: (ctx) => {
        if (!ctx.sessionRef) return;
        copyToClipboard(ctx.sessionRef).catch(() => ctx.toasts.push("error", "Couldn't copy session id"));
      },
    },
    {
      id: "tasks",
      title: "Toggle tasks panel",
      hint: "",
      keywords: [],
      scope: "session",
      run: (ctx) => toggleSessionPane(ctx, "sessionTasks"),
    },
    {
      id: "status",
      title: "Toggle session details",
      hint: "",
      keywords: ["details", "info"],
      scope: "session",
      run: (ctx) => toggleSessionPane(ctx, "sessionDetails"),
    },
    {
      id: "project",
      title: "Reveal session's project in sidebar",
      hint: "scroll sidebar",
      keywords: ["folder"],
      scope: "session",
      run: (ctx) => {
        if (ctx.sessionRef) revealSessionInRail(ctx.sessionRef);
      },
    },
  ];
}

// rememberableId is the id a successful ARGLESS command records in recents -
// empty (never recorded) for a stayOpen command (/search, /help), the command
// id otherwise (search.js:829).
export function rememberableId(command: Command): string {
  return command.stayOpen ? "" : command.id;
}

// commandsInScope resolves the registry against the focused session: global
// commands always, session commands whenever a session pane is focused, each
// carrying the hub's own verdict on whether it can run.
//
// Session LIVENESS is not a gate here, and that is the point (katas cjzc,
// zshh). The palette used to hide every session command for an ended session,
// which took /model /goal /clear /compact /aside away from exactly the
// sessions the hub advertises them for: pastEntryThread (app_threadread.go)
// publishes ChangeModel/Send/Compact/Clear/Shutdown/Goal/Rename for a cold
// exited thread and the handlers resume it behind the call, while deliberately
// leaving Steer/Interrupt/Queue false because those need a turn no cold
// session has. Reading those flags gives the user the same answer the hub
// would, and spares them having to know whether a session is "running".
//
// An unhydrated model (no snapshot in the store yet) leaves everything
// enabled: unknown is not the same as refused, and the hub still gets the
// final word on the call itself.
export function commandsInScope(ctx: PaletteContext, catalog = useCommandCatalog.getState().commands): ScopedCommand[] {
  const model = focusedModel(ctx.sessionRef);
  const catalogEntries = ctx.sessionRef === null ? [] : catalogCommands(catalog);
  return [...buildCommands(), ...catalogEntries]
    .filter((c) => c.scope === "global" || ctx.sessionRef !== null)
    .map((c) => scopeCommand(c, model));
}

// sessionBuiltinCommands: the composer's own view of the registry - every
// BUILT-IN (never plugin-catalog: those carry slashCommandInvocation, which
// is what distinguishes a catalogCommands() entry from a buildCommands() one
// here) session-scoped command, resolved against the focused session the
// exact same way the palette used to resolve its whole list -
// unavailableReason included. Composer.tsx's inline slash menu (merged with
// the plugin catalog separately - slashCompletion.ts's mergeSlashCommands)
// and its Enter/submit interception (builtinCommand.ts's
// matchBuiltinInvocation) both read this SAME resolved list, so which
// commands the composer offers and which it will actually run for a typed
// "/name" can never drift apart.
export function sessionBuiltinCommands(ctx: PaletteContext): ScopedCommand[] {
  return commandsInScope(ctx).filter((c) => c.scope === "session" && c.slashCommandInvocation === undefined);
}

// sessionScopedHandoffMatch: does the palette filter's FIRST token (the
// command name the user is mid-typing, args ignored) prefix-match a
// session-scoped command's name - built-in id, or a catalog entry's own
// name? Unlike commandsInScope's own catalog gating, this does NOT require a
// focused session: the point of the handoff row this drives
// (CommandPalette.tsx's buildView) is to explain the one-place-to-run-this
// rule even to a user with nothing focused yet (design point 3: "No focused
// session -> the handoff row explains that instead"), so the check has to
// work with no session in play. An empty query never matches - there is
// nothing to hand off yet, and the palette's own empty-query view (Recent +
// global Commands, or the needs-you list) should render undisturbed.
export function sessionScopedHandoffMatch(rawFilter: string, catalog: CommandDescriptor[]): boolean {
  const q = rawFilter.replace(/^\//, "").toLowerCase().trim();
  const firstToken = q.split(/\s+/)[0] ?? "";
  if (!firstToken) return false;
  const builtinIds = buildCommands()
    .filter((c) => c.scope === "session")
    .map((c) => c.id);
  const catalogNames = catalog.map((c) => c.name.toLowerCase());
  return [...builtinIds, ...catalogNames].some((name) => name.startsWith(firstToken));
}

// slashCommandInvocation is the one place that decides what a user actually
// types to invoke a catalog command: a plugin-sourced command with a known
// pluginName needs the qualified "/plugin:name" form (unqualified "/name"
// only resolves the FIRST plugin registering that name - see app_rpc.go's
// own dispatch), everything else (user commands, and plugin commands
// without a pluginName, e.g. a stub catalog entry in a test) is unambiguous
// as bare "/name". Shared verbatim by catalogCommands below (what the
// palette's activateCommand inserts, via the stored field on Command) and
// composer/Composer.tsx's commitSlashCompletion (the inline "/" menu's
// insert) - a single source of truth for the qualification rule, so the two
// insertion paths can never drift back out of sync the way they did before
// this fix (the inline menu inserted bare "/name" even for plugin
// commands, which the hub dispatch cannot resolve for anything but the
// FIRST-registered plugin using that name).
export function slashCommandInvocation(command: Pick<CommandDescriptor, "name" | "source" | "pluginName">): string {
  return command.source === "plugin" && command.pluginName
    ? `/${command.pluginName}:${command.name}`
    : `/${command.name}`;
}

// The command catalog is global, but plugin commands are only valid in a
// session that loaded their plugin. Keep this filter at the palette boundary:
// the store remains the complete catalog for other consumers, and the
// no-session state deliberately keeps its global view.
export function visibleCatalogCommands(
  commands: CommandDescriptor[],
  activePluginNames: ReadonlySet<string> | null | undefined,
): CommandDescriptor[] {
  if (activePluginNames === undefined) return commands;
  return commands.filter(
    (command) =>
      command.source !== "plugin" ||
      (activePluginNames !== null && command.pluginName !== undefined && activePluginNames.has(command.pluginName)),
  );
}

function catalogCommands(catalog: CommandDescriptor[]): Command[] {
  return catalog.map((command) => ({
    id: command.name,
    title: `${command.name} [${command.source ?? "plugin"}]`,
    hint: command.description ?? "",
    description: command.description ?? "",
    source: command.source,
    pluginName: command.pluginName,
    keywords: [command.source ?? "plugin", command.pluginName ?? ""].filter(Boolean),
    scope: "session",
    slashCommandInvocation: slashCommandInvocation(command),
  }));
}

function scopeCommand(command: Command, model: ThreadModel | undefined): ScopedCommand {
  const capability = command.capability;
  if (!capability || !model || model.capabilities[capability]) return command;
  return { ...command, unavailableReason: UNAVAILABLE_REASON };
}

export interface FilteredCommands {
  recent: ScopedCommand[];
  commands: ScopedCommand[];
}

// appGlobalCommandsInScope: the palette's own browsable universe - every
// app-global command, unfiltered by any query. filterCommands (below) layers
// the query/ranking on top of exactly this; CommandPalette.tsx's own exact-id
// lookup (its enterPressed) uses it directly, unranked, so a typed command id
// resolves regardless of how commandScore would have ranked it against the
// REST of the query text.
export function appGlobalCommandsInScope(ctx: PaletteContext): ScopedCommand[] {
  return commandsInScope(ctx).filter((c) => c.scope === "global");
}

// filterCommands is renderCommands' data half (search.js:637-651): with an
// EMPTY filter, a Recent section (from localStorage, excluded from the main
// list to avoid duplication); with a NON-empty filter, commandScore ranking
// (descending, registry order as a stable tiebreak, negatives excluded) and
// no Recent section.
//
// app-global ONLY (2026-08-14 decision, commandSurface's own doc comment):
// every session-scoped command - built-in or plugin-catalog - is delisted
// here, whether or not a session is focused. The palette hands those off to
// the composer instead (CommandPalette.tsx's buildView, via
// sessionScopedHandoffMatch) rather than listing and running them itself.
export function filterCommands(ctx: PaletteContext, rawFilter: string): FilteredCommands {
  const q = rawFilter.replace(/^\//, "").toLowerCase().trim();
  const scoped = appGlobalCommandsInScope(ctx);
  const recent = q
    ? []
    : readRecentCommandIds()
        .map((id) => scoped.find((c) => c.id === id))
        .filter((c): c is ScopedCommand => c !== undefined);
  const recentIds = new Set(recent.map((c) => c.id));
  const commands = q
    ? scoped
        .map((command, idx) => ({ command, idx, score: commandScore(command, q) }))
        .filter((row) => row.score >= 0)
        .sort((a, b) => b.score - a.score || a.idx - b.idx)
        .map((row) => row.command)
    : scoped.filter((c) => !recentIds.has(c.id));
  return { recent, commands };
}

export type { OnPage };
