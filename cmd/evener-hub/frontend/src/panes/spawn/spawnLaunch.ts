import { findBuiltinArgument } from "@evener/appwire-client";
import { splitModelId } from "../../shell/palette/commands";
import type { ModelCatalog, ModelCatalogEntry } from "../../widgets";
import { resolveSpawnEffortItems, resolveSpawnModelItems } from "./spawnSlashMenu";

// Fallback effort ladder for a model whose own ladder the hub does not
// enumerate. The select's real ladder comes from the selected model's catalog
// entry (reasoningEffortLevels/supportsReasoning, served by model/list);
// "(default)" + an explicit "none" ride every ladder.
export const FALLBACK_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];
// Shared empty-ladder constant so the derived value keeps a stable identity
// across renders (the stale-effort effect keys off it).
export const NO_EFFORT_LEVELS: string[] = [];

// The effort levels a catalog entry authorizes: the model's own named ladder
// when it has one, an EMPTY list when the catalog says the model cannot
// reason at all, and null when the hub can't say (enrichment failed, or the
// entry names neither levels nor a reasoning capability). The caller
// substitutes FALLBACK_EFFORT_LEVELS for null, so a missing catalog never
// empties or disables the field.
export function catalogEffortLevels(entry: ModelCatalogEntry | undefined): string[] | null {
  if (entry === undefined) return null;
  if (entry.reasoningEffortLevels !== undefined && entry.reasoningEffortLevels.length > 0) {
    return entry.reasoningEffortLevels;
  }
  if (entry.supportsReasoning === false) return NO_EFFORT_LEVELS;
  return null;
}

export interface SpawnLaunchScalars {
  modelProvider?: string;
  model?: string;
  reasoningEffort?: string;
}

/** Why a pre-session builtin was refused before any session exists. */
export interface SpawnBuiltinBlock {
  kind: "block";
  commandId: string;
  reason: "needs-value" | "unknown-value";
  /** The trimmed value the user typed; empty for "needs-value". */
  value: string;
}

/**
 * What a matched pre-session builtin does to the launch: nothing (an ordinary
 * start), a refusal the caller reports before starting, or launch scalars that
 * ride thread/start and win over the form's chips.
 */
export type SpawnBuiltinLaunch =
  | { kind: "none" }
  | SpawnBuiltinBlock
  | { kind: "scalars"; scalars: SpawnLaunchScalars };

export interface SpawnBuiltinLaunchInput {
  match: { command: { id: string }; argsText: string };
  remoteLaunch: boolean;
  /** The catalog stamped for the current scope, or null when none committed for it. */
  scopedModelCatalog: ModelCatalog | null;
  /** True only when a catalog stamped for ANOTHER scope or loader is on hand. */
  modelCatalogScopeMismatch: boolean;
  /** The model whose effort ladder the form is showing. */
  effortModel: string;
  reasoningEffort: string;
}

function block(commandId: string, reason: SpawnBuiltinBlock["reason"], value: string): SpawnBuiltinBlock {
  return { kind: "block", commandId, reason, value };
}

/** The text a caller toasts for a refused builtin. */
export function spawnBuiltinBlockMessage(refusal: SpawnBuiltinBlock): string {
  return refusal.reason === "needs-value"
    ? `/${refusal.commandId} needs a value`
    : `/${refusal.commandId}: unknown value "${refusal.value}"`;
}

export function resolveSpawnBuiltinLaunch({
  match,
  remoteLaunch,
  scopedModelCatalog,
  modelCatalogScopeMismatch,
  effortModel,
  reasoningEffort,
}: SpawnBuiltinLaunchInput): SpawnBuiltinLaunch {
  // Bare /goal fail-closes pre-start like bare /reasoning-effort: there is
  // no goal to clear before the session exists, so starting one just to
  // no-op its clearing is waste, and sending the literal "/goal" as the
  // first turn is noise.
  if (match.command.id === "goal" && match.argsText.trim() === "") return block("goal", "needs-value", "");
  if (match.command.id !== "model" && match.command.id !== "reasoning-effort") return { kind: "none" };
  // Pre-start validation for enum-arg builtins: there is no cheaper
  // moment to refuse than before the session exists. Empty /model means
  // "(default)": fail-open, no override at all.
  const value = match.argsText.trim();
  if (match.command.id === "model" && value === "") return { kind: "none" };
  if (remoteLaunch) {
    // Remote target: the selected host's own hub validates the forwarded
    // value. This controller's catalog describes ITS providers and models
    // - not the host's - so matching against it here would refuse a model
    // or effort level that only the selected host supports, contradicting
    // the host-authority model the launch already follows for cwd,
    // providers and plugins. The value is forwarded verbatim: a model as
    // "provider/model" (the split every other /model path uses), an effort
    // level as typed. Bare /reasoning-effort still fails closed, since an
    // empty enum value is not a controller-catalog judgment.
    if (value === "") return block("reasoning-effort", "needs-value", "");
    if (match.command.id === "model") {
      const { provider, model: modelId } = splitModelId(value);
      // Unlike every other splitModelId caller, this input is RAW user text,
      // not a provider/model catalog id that always contains a slash: "foo"
      // splits to provider "foo" with an empty model, and the selected host
      // ignores a provider with no model (its own model field stays empty),
      // so the requested model would be silently dropped rather than
      // refused. The host's own parser requires provider/model too, so
      // refuse here with the same unknown-value outcome the local path
      // uses - a clear pre-launch rejection instead of a silent no-op.
      if (provider === "" || modelId === "") return block(match.command.id, "unknown-value", value);
      return { kind: "scalars", scalars: { modelProvider: provider, model: modelId } };
    }
    return { kind: "scalars", scalars: { reasoningEffort: value } };
  }
  // Effort validation reads the scope-stamped catalog, not the merged
  // display ladder: same staleness hole as /model (a value valid only
  // in the previous scope must not validate). Only PROVEN staleness
  // fail-closes (a stamp for another scope/loader): an unknown ladder
  // with no stamp — never loaded, failed refresh, or an entry without
  // ladder metadata — falls back to the fallback ladder, the pre-load
  // status quo. Conflating "don't know" with "known empty" would
  // refuse valid values whenever the catalog lists the model without
  // ladder details.
  const scopedEffortEntry =
    effortModel === ""
      ? undefined
      : scopedModelCatalog?.models.find((entry) => `${entry.provider}/${entry.model}` === effortModel);
  const scopedKnownEffortLevels = catalogEffortLevels(scopedEffortEntry);
  // Proven staleness fail-closes to no levels — and the chip value must
  // not re-authorize itself through `current`: effortOptionLevels
  // appends a missing current, so resolving against the chip would
  // validate a value the new scope never offered. Passing "" keeps the
  // stale chip out of the candidate set (bare effort fails closed on
  // the empty query regardless).
  const scopedEffortLevels = modelCatalogScopeMismatch ? [] : (scopedKnownEffortLevels ?? FALLBACK_EFFORT_LEVELS);
  const scopedEffortCurrent = modelCatalogScopeMismatch ? "" : reasoningEffort;
  // A model value cannot be validated against a catalog that never
  // committed for the current scope: during the ~250ms CATALOG_SETTLE_MS
  // window after mount - or after a failed refresh - resolveSpawnModelItems
  // resolves zero items for EVERY value, known or not, so fail-closing
  // here would refuse a model the scope does offer. Treat it as "don't
  // know" and forward the typed value as the launch scalar, letting the
  // start call's own check decide. Only PROVEN staleness (a stamp for
  // another scope/loader) and a scope-matched catalog that omits the value
  // still fail closed.
  const modelCatalogUnknown = match.command.id === "model" && scopedModelCatalog === null && !modelCatalogScopeMismatch;
  const items =
    match.command.id === "model"
      ? resolveSpawnModelItems(scopedModelCatalog)
      : resolveSpawnEffortItems(scopedEffortLevels, scopedEffortCurrent);
  // Bare /reasoning-effort fails CLOSED pre-start: the "" head of
  // resolveSpawnEffortItems (the "(default)" entry) must not count as
  // known here, so an empty effort value is refused without thread/start
  // (palette parity - in-session bare /reasoning-effort errors with no
  // side effects). Bare /model stays fail-open above.
  const matched =
    match.command.id === "reasoning-effort" && value === "" ? undefined : findBuiltinArgument(items, match.argsText);
  if (!matched && !modelCatalogUnknown) {
    return block(match.command.id, value ? "unknown-value" : "needs-value", value);
  }
  if (match.command.id === "model") {
    // A forwarded-but-unvalidated value (modelCatalogUnknown) is RAW user
    // text, not a provider/model catalog id: "foo" splits to provider
    // "foo" with an empty model, and the launch would carry no model at
    // all. Refuse the same way the remote path above does rather than
    // silently drop the request - this is a shape check, not a catalog
    // judgment, so it holds even while the catalog is unknown.
    const { provider, model: modelId } = splitModelId(matched ? matched.id : value);
    if (provider === "" || modelId === "") return block(match.command.id, "unknown-value", value);
    return { kind: "scalars", scalars: { modelProvider: provider, model: modelId } };
  }
  if (match.command.id === "reasoning-effort" && matched) {
    return { kind: "scalars", scalars: { reasoningEffort: matched.id } };
  }
  return { kind: "none" };
}
