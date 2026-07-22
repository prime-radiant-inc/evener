// The command registry: the 22-command palette (23 real entries; /fork is
// intentionally omitted - see below) ported from search.js:326-518. Every
// session mutation goes through Wave-5's threadsStore actions (each already
// Conflict-typed), navigation through shell/routing, /theme through
// prefsStore.setTheme (the hazard-#1 FIX: visible immediately, unlike the
// legacy's dead body-class toggle), /project through the rail's imperative
// reveal seam, /upgrade through the serf/upgrade wire method. Failures surface
// either as a blocked sentinel (inline .palette-error strip, palette stays
// open) for an idle-guarded action or a Conflict, or as a useToasts() toast
// for a fire-and-report action - never a silent swallow.

import { connectionStore } from "../../stores/connection";
import { prefsStore } from "../../stores/prefs";
import { threadsStore } from "../../stores/threads";
import type { ToastKind } from "../../widgets";
import { revealSessionInRail } from "../rail/railController";
import { navigate } from "../routing";
import { workspaceStore } from "../workspace";
import { blocked } from "./blocked";
import { commandScore } from "./commandScore";
import {
  focusedModel,
  hasActiveTurn,
  isSessionBusy,
  isSessionEnded,
  type OnPage,
  type PaletteContext,
} from "./paletteContext";
import { readRecentCommandIds } from "./recentCommands";

export type CommandScope = "global" | "session" | "ended-ok";

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
  keywords: string[];
  scope: CommandScope;
  // stayOpen commands (/search, /help) never close and never record recency.
  stayOpen?: boolean;
  args?: CommandArgs;
  run?(ctx: PaletteRunContext): CommandResult;
}

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

// clickTrigger ports the legacy /tasks and /status "synthesize a click on the
// chrome trigger" behavior (search.js:511-514), no-op-safe when the trigger
// is absent - exactly as the legacy `if (btn) btn.click()` was.
function clickTrigger(selector: string): void {
  const el = document.querySelector<HTMLElement>(selector);
  if (el) el.click();
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
  return client.request("serf/upgrade", { requested: "" }).then(
    (resp) => {
      ctx.toasts.push("success", `Serf upgraded to ${resp.channel || "current channel"}`);
      if (resp.restartMessage) ctx.toasts.push("info", resp.restartMessage);
    },
    (err) => {
      ctx.toasts.push("error", "Upgrade failed");
      throw err;
    },
  );
}

// buildCommands rebuilds the registry fresh on every call (search.js:326:
// "The command registry is rebuilt fresh on every call"). All 23 entries: 8
// global, 11 session (live-only), 4 session-info (ended-ok). /fork is
// intentionally OMITTED (floor §2.5, search.js:497-499) - it needs an edited
// message the palette has no UI to collect; the transcript row's own edit
// affordance is the entry point.
export function buildCommands(): Command[] {
  return [
    // --- global ---
    {
      id: "new",
      title: "New session",
      hint: "blank spawn page",
      keywords: [],
      scope: "global",
      run: () => {
        navigate("/new");
      },
    },
    {
      id: "spawn",
      title: "Spawn with prompt",
      hint: "new session, prefilled",
      keywords: ["start"],
      scope: "global",
      args: {
        kind: "free",
        placeholder: "prompt to spawn…",
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
      keywords: ["?", "keys", "shortcuts"],
      scope: "global",
      stayOpen: true,
      run: (ctx) => {
        ctx.ui.showHelp();
      },
    },
    {
      id: "upgrade",
      title: "Upgrade Serf",
      hint: "current channel",
      keywords: ["update", "snapshot", "release"],
      scope: "global",
      run: (ctx) => upgrade(ctx),
    },

    // --- session (live only) ---
    {
      id: "compact",
      title: "Compact transcript",
      hint: "free up token space",
      keywords: ["compress"],
      scope: "session",
      run: (ctx) => (ctx.sessionRef ? threadsStore.getState().compact(ctx.sessionRef) : undefined),
    },
    {
      id: "interrupt",
      title: "Interrupt agent",
      hint: "cancel in-flight turn",
      keywords: ["cancel", "stop"],
      scope: "session",
      run: (ctx) => {
        const model = focusedModel(ctx.sessionRef);
        if (!ctx.sessionRef || !model || !hasActiveTurn(model)) return blocked("interrupt failed: no active turn");
        return threadsStore.getState().interrupt(ctx.sessionRef);
      },
    },
    {
      id: "clear",
      title: "Clear context",
      hint: "start fresh in this session",
      keywords: [],
      scope: "session",
      run: (ctx) => (ctx.sessionRef ? threadsStore.getState().clearThread(ctx.sessionRef) : undefined),
    },
    {
      id: "aside",
      title: "Aside: fork to side thread",
      hint: "side question, same permissions",
      keywords: ["fork", "side"],
      scope: "session",
      run: (ctx) => {
        if (!ctx.sessionRef) return undefined;
        return threadsStore
          .getState()
          .forkFromTurn(ctx.sessionRef, { aside: true })
          .then((resp) => {
            workspaceStore.getState().openPane("session", { ref: resp.thread.serf.ref });
          });
      },
    },
    {
      id: "shutdown",
      title: "Shut down daemon",
      hint: "ends this session",
      keywords: ["kill"],
      scope: "session",
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
      args: {
        kind: "enum",
        placeholder: "choose a model…",
        // Interim source: appwire model/list (ModelDescriptor is {provider,
        // model} only). The rich REST /api/models catalog - display names,
        // capability badges, grouping, pricing - is Jesse-decided Wave 8, not
        // W6. Lets the enum's rejection path surface a load failure rather
        // than swallowing it to an empty list.
        source: async () => {
          const resp = await threadsStore.getState().listModels();
          return resp.data.map((m) => ({ id: `${m.provider}/${m.model}`, label: m.model, hint: m.provider }));
        },
        run: (ctx, item) => {
          if (!ctx.sessionRef) return undefined;
          const model = focusedModel(ctx.sessionRef);
          if (model && isSessionBusy(model)) return blocked("model change failed: turn in progress");
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
        // supportsReasoning), NOT /api/models - the live surface shouldn't
        // need it (floor §2.5). A non-reasoning model yields ZERO options, not
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
      args: {
        kind: "free",
        placeholder: "objective… (empty to clear)",
        run: (ctx, text) =>
          ctx.sessionRef ? threadsStore.getState().setGoal(ctx.sessionRef, (text || "").trim()) : undefined,
      },
    },
    {
      id: "drain-as-steer",
      title: "Drain queue as steering",
      hint: "force-steer combined action",
      keywords: ["force-steer", "drain"],
      scope: "session",
      run: (ctx) => {
        const model = focusedModel(ctx.sessionRef);
        if (!ctx.sessionRef || !model || !hasActiveTurn(model)) return blocked("drain failed: no active turn");
        return threadsStore.getState().drainAsSteer(ctx.sessionRef, "");
      },
    },

    // --- session info (live or ended) ---
    {
      id: "copy-id",
      title: "Copy session ID",
      hint: "clipboard",
      keywords: ["clipboard"],
      scope: "ended-ok",
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
      scope: "ended-ok",
      run: () => clickTrigger("[data-tasks-trigger]"),
    },
    {
      id: "status",
      title: "Toggle session details",
      hint: "",
      keywords: ["details", "info"],
      scope: "ended-ok",
      run: () => clickTrigger("[data-details-trigger]"),
    },
    {
      id: "project",
      title: "Reveal session's project in sidebar",
      hint: "scroll sidebar",
      keywords: ["folder"],
      scope: "ended-ok",
      run: (ctx) => {
        if (ctx.sessionRef) revealSessionInRail(ctx.sessionRef);
      },
    },
  ];
}

// commandsInScope filters the registry by the current context's scope
// (search.js:581-588): global always; ended-ok whenever a session pane is
// focused (live OR ended); bare session only for a LIVE focused session.
export function commandsInScope(ctx: PaletteContext): Command[] {
  const model = focusedModel(ctx.sessionRef);
  const ended = model ? isSessionEnded(model) : false;
  return buildCommands().filter((c) => {
    if (c.scope === "global") return true;
    if (c.scope === "ended-ok") return ctx.sessionRef !== null;
    return ctx.sessionRef !== null && !ended;
  });
}

export interface FilteredCommands {
  recent: Command[];
  commands: Command[];
}

// filterCommands is renderCommands' data half (search.js:637-651): with an
// EMPTY filter, a Recent section (from localStorage, excluded from the main
// list to avoid duplication); with a NON-empty filter, commandScore ranking
// (descending, registry order as a stable tiebreak, negatives excluded) and
// no Recent section.
export function filterCommands(ctx: PaletteContext, rawFilter: string): FilteredCommands {
  const q = rawFilter.replace(/^\//, "").toLowerCase().trim();
  const scoped = commandsInScope(ctx);
  const recent = q
    ? []
    : readRecentCommandIds()
        .map((id) => scoped.find((c) => c.id === id))
        .filter((c): c is Command => c !== undefined);
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
