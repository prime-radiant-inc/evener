// The wire loader for the rich model catalog: the REST /api/models call the
// two swap sites inject as loadCatalog. Requesting ?diagnostics=1 is deliberate
// - that is the ONLY response shape carrying the Recent group AND the
// provider diagnostics (the bare default response is a models-only array,
// web_spawn.go writeModelsResponse); it costs the server nothing extra (the
// diagnostics are computed regardless, the flag only toggles serialization).
// Snake_case wire fields are mapped to the camelCase catalog model here.
import type { ModelCatalog, ModelCatalogDiagnostic, ModelCatalogEntry } from "./index";

function asString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}
function asNumber(value: unknown): number | undefined {
  return typeof value === "number" ? value : undefined;
}
function asBoolean(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}
function asStringArray(value: unknown): string[] | undefined {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : undefined;
}

/** Maps one /api/models entry (snake_case, omitempty capability/cost fields)
 * to a ModelCatalogEntry, omitting any field the wire left out. */
export function mapApiEntry(raw: unknown): ModelCatalogEntry {
  const r = (raw ?? {}) as Record<string, unknown>;
  const entry: ModelCatalogEntry = {
    provider: asString(r.provider) ?? "",
    model: asString(r.model) ?? "",
    displayName: asString(r.display_name) ?? "",
  };
  const contextWindow = asNumber(r.context_window);
  if (contextWindow !== undefined) entry.contextWindow = contextWindow;
  const supportsTools = asBoolean(r.supports_tools);
  if (supportsTools !== undefined) entry.supportsTools = supportsTools;
  const supportsVision = asBoolean(r.supports_vision);
  if (supportsVision !== undefined) entry.supportsVision = supportsVision;
  const maxOutputTokens = asNumber(r.max_output_tokens);
  if (maxOutputTokens !== undefined) entry.maxOutputTokens = maxOutputTokens;
  const supportsWebSearch = asBoolean(r.supports_web_search);
  if (supportsWebSearch !== undefined) entry.supportsWebSearch = supportsWebSearch;
  const supportsReasoning = asBoolean(r.supports_reasoning);
  if (supportsReasoning !== undefined) entry.supportsReasoning = supportsReasoning;
  const inputCost = asNumber(r.input_cost_per_million);
  if (inputCost !== undefined) entry.inputCostPerMillion = inputCost;
  const outputCost = asNumber(r.output_cost_per_million);
  if (outputCost !== undefined) entry.outputCostPerMillion = outputCost;
  const effortLevels = asStringArray(r.reasoning_effort_levels);
  if (effortLevels !== undefined) entry.reasoningEffortLevels = effortLevels;
  return entry;
}

function mapApiDiagnostic(raw: unknown): ModelCatalogDiagnostic {
  const r = (raw ?? {}) as Record<string, unknown>;
  const diag: ModelCatalogDiagnostic = { message: asString(r.message) ?? "" };
  const provider = asString(r.provider);
  if (provider !== undefined) diag.provider = provider;
  const source = asString(r.source);
  if (source !== undefined) diag.source = source;
  const title = asString(r.title);
  if (title !== undefined) diag.title = title;
  const hint = asString(r.hint);
  if (hint !== undefined) diag.hint = hint;
  return diag;
}

export interface FetchCatalogOptions {
  /** Harness id to scope the launchable model set (spawn). Omitted = default. */
  harness?: string;
  /** Working directory whose project config scopes the set (spawn). */
  cwd?: string;
  signal?: AbortSignal;
}

/** Fetches and maps the /api/models?diagnostics=1 envelope. Tolerates the
 * bare-array default shape too (models only) so a wire drift can't crash the
 * picker. Escaping matches the codebase's own REST convention (search.ts):
 * encodeURIComponent, so a space serializes as %20. */
export async function fetchModelCatalog(opts?: FetchCatalogOptions): Promise<ModelCatalog> {
  let url = "/api/models?diagnostics=1";
  if (opts?.harness) url += `&harness=${encodeURIComponent(opts.harness)}`;
  if (opts?.cwd) url += `&cwd=${encodeURIComponent(opts.cwd)}`;

  const res = await fetch(url, { credentials: "same-origin", signal: opts?.signal });
  if (!res.ok) throw new Error(`models request failed: ${res.status}`);

  const body: unknown = await res.json();
  const envelope = Array.isArray(body)
    ? { models: body, recent: [], diagnostics: [] }
    : (body as Record<string, unknown>);
  const models = Array.isArray(envelope.models) ? envelope.models : [];
  const recent = Array.isArray(envelope.recent) ? envelope.recent : [];
  const diagnostics = Array.isArray(envelope.diagnostics) ? envelope.diagnostics : [];

  return {
    models: models.map(mapApiEntry),
    recent: recent.map(mapApiEntry),
    diagnostics: diagnostics.map(mapApiDiagnostic),
  };
}
