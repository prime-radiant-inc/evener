import { friendlyErrorMessage } from "../../protocol/errors";
import { blocked, isBlocked } from "../../shell/palette/blocked";
import {
  buildCommands,
  type CommandArgsEnumItem,
  type PaletteRunContext,
  type ScopedCommand,
} from "../../shell/palette/commands";
import { effortLabel, effortOptionLevels } from "../../shell/reasoningEffort";
import type { ToastKind } from "../../widgets";
import type { ModelCatalog } from "../../widgets/modelCatalog";

// The pre-session allowlist: the only session builtins the spawn box offers.
// Turn-gated commands (steer, queue, interrupt, drain-as-steer) have no turn
// yet; the session-only remainder (compact, clear, aside, shutdown, copy-id,
// tasks, status, project) each needs an existing session to act on. These
// three configure the session being started, so they make sense before one
// exists.
export const PRE_SESSION_BUILTIN_IDS = ["goal", "model", "reasoning-effort"] as const;

export type BuiltinRunOutcome = { ok: true } | { ok: false; message: string };

export function spawnBuiltinCommands(): ScopedCommand[] {
  const byId = new Map(
    buildCommands()
      .filter((c) => c.scope === "session")
      .map((c) => [c.id, c]),
  );
  // Allowlist order, not registry order: mergeSlashCommands renders builtins
  // in this array's order, and the menu should read goal, model,
  // reasoning-effort regardless of where the registry lists them.
  return PRE_SESSION_BUILTIN_IDS.map((id) => byId.get(id)).filter((c): c is ScopedCommand => c !== undefined);
}

export function resolveSpawnModelItems(catalog: ModelCatalog | null): CommandArgsEnumItem[] {
  if (!catalog) return [];
  return catalog.models.map((m) => ({
    id: `${m.provider}/${m.model}`,
    label: m.displayName || `${m.provider}/${m.model}`,
    hint: m.provider,
  }));
}

export function resolveSpawnEffortItems(levels: string[], current: string): CommandArgsEnumItem[] {
  return effortOptionLevels(levels, current).map((l) => ({ id: l, label: effortLabel(l, levels) }));
}

export async function runSpawnBuiltinAfterStart(
  id: string,
  argsText: string,
  ref: string,
  toasts: { push(kind: ToastKind, text: string): void },
): Promise<BuiltinRunOutcome> {
  const command = spawnBuiltinCommands().find((c) => c.id === id);
  if (!command) {
    const message = `/${id}: unknown value "${argsText.trim()}"`;
    toasts.push("error", message);
    return { ok: false, message };
  }

  const ctx: PaletteRunContext = {
    sessionRef: ref,
    onPage: "spawn",
    toasts,
    ui: { clearToSearch: () => {}, showHelp: () => {} },
  };

  let toasted = false;
  const wrappedCtx: PaletteRunContext = {
    ...ctx,
    toasts: {
      push: (kind, text) => {
        toasted = true;
        toasts.push(kind, text);
      },
    },
  };

  if (command.unavailableReason) {
    const message = `/${command.id} is ${command.unavailableReason}`;
    toasts.push("error", message);
    return { ok: false, message };
  }

  try {
    if (!command.args) {
      const result = await command.run?.(wrappedCtx);
      if (isBlocked(result)) {
        const msg = (result as { message: string }).message;
        if (!toasted) toasts.push("error", msg);
        return { ok: false, message: msg };
      }
      return { ok: true };
    }

    if (command.args.kind === "free") {
      const result = await command.args.run(wrappedCtx, argsText);
      if (isBlocked(result)) {
        const msg = (result as { message: string }).message;
        if (!toasted) toasts.push("error", msg);
        return { ok: false, message: msg };
      }
      return { ok: true };
    }

    const trimmed = argsText.trim();

    if (id === "model") {
      if (trimmed === "") {
        return { ok: true };
      }
      // Resolve against the command's own source. In the pre-session caller
      // (Task 6) unknown values are validated before start via
      // resolveSpawnModelItems(modelCatalog); this post-start path validates
      // again against the live source. When the source is unavailable (null
      // catalog, tests) the unknown still fail-closes.
      let items: CommandArgsEnumItem[] = [];
      try {
        items = await command.args.source(wrappedCtx);
      } catch {
        items = [];
      }
      // If the source returned nothing (e.g. in tests where listModels is not
      // stubbed), fall back to an empty pre-session catalog for the fail-closed
      // blocked message rather than throwing.
      if (items.length === 0) {
        items = resolveSpawnModelItems(null);
      }
      const needle = trimmed.toLowerCase();
      const item = items.find((it) => it.id.toLowerCase() === needle || it.label.toLowerCase() === needle);
      if (!item) {
        const message = `/${id}: unknown value "${trimmed}"`;
        if (!toasted) toasts.push("error", message);
        return { ok: false, message };
      }
      const result = await command.args.run(wrappedCtx, item);
      if (isBlocked(result)) {
        const msg = (result as { message: string }).message;
        if (!toasted) toasts.push("error", msg);
        return { ok: false, message: msg };
      }
      return { ok: true };
    }

    if (id === "reasoning-effort") {
      let items: CommandArgsEnumItem[] = [];
      try {
        items = await command.args.source(wrappedCtx);
      } catch {
        items = [];
      }
      const needle = trimmed.toLowerCase();
      const item = items.find((it) => it.id.toLowerCase() === needle || it.label.toLowerCase() === needle);
      if (!item) {
        const message = trimmed ? `/${id}: unknown value "${trimmed}"` : `/${id} needs a value`;
        const b = blocked(message);
        if (!toasted) toasts.push("error", b.message);
        return { ok: false, message: b.message };
      }
      const result = await command.args.run(wrappedCtx, item);
      if (isBlocked(result)) {
        const msg = (result as { message: string }).message;
        if (!toasted) toasts.push("error", msg);
        return { ok: false, message: msg };
      }
      return { ok: true };
    }

    const items = await command.args.source(wrappedCtx);
    const needle = trimmed.toLowerCase();
    const item = items.find((it) => it.id.toLowerCase() === needle || it.label.toLowerCase() === needle);
    if (!item) {
      const message = trimmed ? `/${id}: unknown value "${trimmed}"` : `/${id} needs a value`;
      const b = blocked(message);
      if (!toasted) toasts.push("error", b.message);
      return { ok: false, message: b.message };
    }
    const result = await command.args.run(wrappedCtx, item);
    if (isBlocked(result)) {
      const msg = (result as { message: string }).message;
      if (!toasted) toasts.push("error", msg);
      return { ok: false, message: msg };
    }
    return { ok: true };
  } catch (err) {
    const message = friendlyErrorMessage(err);
    if (!toasted) toasts.push("error", message);
    return { ok: false, message };
  }
}
