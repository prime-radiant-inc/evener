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
  // Mirrors the Spawn effort selector's own option set (Spawn.tsx's
  // effortOptions): the ladder levels plus an unconditional explicit "none"
  // entry. A fresh form whose ladder excludes "none" still offers it, and the
  // backend accepts it — so slash pre-start validation must too, or
  // "/reasoning-effort none" fail-closes a value the selector happily sends.
  return [
    ...effortOptionLevels(levels, current).map((l) => ({ id: l, label: effortLabel(l, levels) })),
    ...(!levels.includes("none") ? [{ id: "none", label: effortLabel("none", levels) }] : []),
  ];
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

    // Narrowed once for the closures below: after the free-arg return, args
    // is enum-kind, but closures don't inherit narrowing.
    const enumArgs = command.args;
    const trimmed = argsText.trim();

    // Shared enum-arg runner: resolve the trimmed value against items, toast
    // the blocked message on no match, run the matched item. Only the
    // empty-value policy differs per command (model fail-opens to default,
    // reasoning-effort fail-closes), so callers pass it in.
    async function runEnumItem(items: CommandArgsEnumItem[], emptyMessage: string | null): Promise<BuiltinRunOutcome> {
      const needle = trimmed.toLowerCase();
      const item = items.find((it) => it.id.toLowerCase() === needle || it.label.toLowerCase() === needle);
      if (!item) {
        const message = trimmed ? `/${id}: unknown value "${trimmed}"` : (emptyMessage ?? `/${id} needs a value`);
        if (id === "model") {
          if (!toasted) toasts.push("error", message);
          return { ok: false, message };
        }
        const b = blocked(message);
        if (!toasted) toasts.push("error", b.message);
        return { ok: false, message: b.message };
      }
      const result = await enumArgs.run(wrappedCtx, item);
      if (isBlocked(result)) {
        const msg = (result as { message: string }).message;
        if (!toasted) toasts.push("error", msg);
        return { ok: false, message: msg };
      }
      return { ok: true };
    }

    async function enumItems(): Promise<CommandArgsEnumItem[]> {
      try {
        return await enumArgs.source(wrappedCtx);
      } catch {
        return [];
      }
    }

    if (id === "model") {
      if (trimmed === "") {
        return { ok: true };
      }
      // Resolve against the command's own source. In the pre-session caller
      // (Task 6) unknown values are validated before start via
      // resolveSpawnModelItems(modelCatalog); this post-start path validates
      // again against the live source. When the source is unavailable (null
      // catalog, tests) the unknown still fail-closes.
      const items = await enumItems();
      // If the source returned nothing (e.g. in tests where listModels is not
      // stubbed), fall back to an empty pre-session catalog for the fail-closed
      // blocked message rather than throwing.
      return runEnumItem(items.length === 0 ? resolveSpawnModelItems(null) : items, null);
    }

    if (id === "reasoning-effort") {
      return runEnumItem(await enumItems(), `/${id} needs a value`);
    }

    // Unreachable: spawnBuiltinCommands() only ever yields the goal (free-arg,
    // returned above), model, and reasoning-effort entries, so every id is
    // handled. A defensive unknown-id refusal rather than a silent success.
    const message = `/${id}: unknown value "${trimmed}"`;
    if (!toasted) toasts.push("error", message);
    return { ok: false, message };
  } catch (err) {
    const message = friendlyErrorMessage(err);
    if (!toasted) toasts.push("error", message);
    return { ok: false, message };
  }
}
