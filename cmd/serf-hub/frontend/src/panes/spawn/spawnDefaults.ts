// Sticky-defaults + prefill layering (floor §1.9) and stale-model detection
// and cleanup (floor §1.10) for the spawn pane. All state lives in localStorage
// under the `serf-hub.spawn-defaults.` namespace, distinct from prefs.ts's
// `serf.prefs.` flat keys - these are per-project JSON blobs plus a few global
// scalars, matching the legacy spawn.js key layout.
//
// This is a per-device convenience layer (like the legacy), NOT the source of
// truth for launch config - the daemon's own launch.toml layering is. A blob
// that the daemon later stops honoring is harmless; the stale-model sweep only
// prunes model values the hub can PROVE are gone (see modelValidityAgainstList).
import type { ModelDescriptor } from "../../protocol/types.gen";

export const SPAWN_DEFAULTS_PREFIX = "serf-hub.spawn-defaults.";
// Global scalar keys (distinct from the `.global` blob defaultsKeyFor("")
// produces): a last-resort working dir consulted on prefill and rewritten on
// every submit (spawn.js:84-88,100), a cross-project model default that layers
// under any per-project one (spawn.js:81-83,98), and the dir-picker's own
// "last accepted directory" seed (spawn.js:1910-1913,1919-1922).
export const GLOBAL_WORKING_DIR_KEY = `${SPAWN_DEFAULTS_PREFIX}global.working_dir`;
export const GLOBAL_MODEL_KEY = `${SPAWN_DEFAULTS_PREFIX}global.model`;
export const GLOBAL_LAST_WORKING_DIR_KEY = `${SPAWN_DEFAULTS_PREFIX}global.last-working-dir`;

// The scalar keys are plain strings, never JSON blobs - the sweep must skip
// them rather than try to parse them as `{model}` objects.
const SCALAR_KEYS = new Set([GLOBAL_WORKING_DIR_KEY, GLOBAL_LAST_WORKING_DIR_KEY, GLOBAL_MODEL_KEY]);

export interface SpawnDefaultsBlob {
  harness?: string;
  model?: string;
  branch?: string;
  access_mode?: string;
  reasoning_effort?: string;
}

export interface ResolvedDefaults {
  harness?: string;
  model?: string;
  workingDir?: string;
  branch?: string;
  accessMode?: string;
  reasoningEffort?: string;
}

function readRaw(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeRaw(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Best-effort (quota/denied): the session still works, just without
    // persistence, matching prefs.ts's own storage discipline.
  }
}

function removeRaw(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // Best-effort, same rationale as writeRaw.
  }
}

// The per-project blob key: keyed by the working dir, or the shared "global"
// blob when there is no working dir yet (floor §1.9, spawn.js:51-53).
export function defaultsKeyFor(cwd: string): string {
  const trimmed = cwd.trim();
  return `${SPAWN_DEFAULTS_PREFIX}${trimmed === "" ? "global" : trimmed}`;
}

export function loadDefaultsBlob(cwd: string): SpawnDefaultsBlob {
  const raw = readRaw(defaultsKeyFor(cwd));
  if (raw === null) return {};
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? (parsed as SpawnDefaultsBlob) : {};
  } catch {
    return {};
  }
}

export function getGlobalLastWorkingDir(): string {
  return readRaw(GLOBAL_LAST_WORKING_DIR_KEY) ?? "";
}

export function setGlobalLastWorkingDir(dir: string): void {
  if (dir.trim() !== "") writeRaw(GLOBAL_LAST_WORKING_DIR_KEY, dir);
}

// Computes the spawn form's initial field values with the floor §1.9 layering:
// the working dir comes from the server's ?dir= prefill first, else the global
// last-resort; the remaining fields come from the resolved project blob, with
// the global model layering UNDER any per-project model default. The legacy's
// harness/branch/access-BEFORE-working_dir application order (spawn.js:1132-1138)
// exists only so branch HEAD-resolution sees an explicit branch default - a
// React consumer gets that for free by having the branch-resolution effect
// respect an already-set branch value, so it is not modeled as ordering here.
export function resolveInitialDefaults(opts: { serverPrefillDir?: string }): ResolvedDefaults {
  const serverDir = opts.serverPrefillDir?.trim();
  const workingDir = serverDir && serverDir !== "" ? serverDir : (readRaw(GLOBAL_WORKING_DIR_KEY) ?? "").trim();
  const blob = loadDefaultsBlob(workingDir);
  const model = blob.model ?? readRaw(GLOBAL_MODEL_KEY) ?? undefined;
  return {
    harness: blob.harness,
    model: model ?? undefined,
    workingDir: workingDir === "" ? undefined : workingDir,
    branch: blob.branch,
    accessMode: blob.access_mode,
    reasoningEffort: blob.reasoning_effort,
  };
}

export interface SaveDefaultsInput {
  cwd: string;
  harness?: string;
  model?: string;
  branch?: string;
  accessMode?: string;
  reasoningEffort?: string;
  // Whether the chosen harness uses serf models (kind === "serf"). Gates
  // whether the model field is persisted at all (floor §1.9, spawn.js:92-98).
  harnessUsesSerfModels: boolean;
}

function nonEmpty(value: string | undefined): string | undefined {
  return value && value.trim() !== "" ? value : undefined;
}

// Persists the submitted form as sticky defaults (floor §1.9): a per-project
// blob (dropping model entirely for a non-serf-model harness), the global model
// key (only when the harness uses serf models AND a model was chosen), and the
// global working_dir (on every submit that has one).
export function saveDefaults(input: SaveDefaultsInput): void {
  const blob: SpawnDefaultsBlob = {};
  const harness = nonEmpty(input.harness);
  const branch = nonEmpty(input.branch);
  const accessMode = nonEmpty(input.accessMode);
  const reasoningEffort = nonEmpty(input.reasoningEffort);
  const model = nonEmpty(input.model);
  if (harness) blob.harness = harness;
  if (branch) blob.branch = branch;
  if (accessMode) blob.access_mode = accessMode;
  if (reasoningEffort) blob.reasoning_effort = reasoningEffort;
  if (model && input.harnessUsesSerfModels) blob.model = model;

  const key = defaultsKeyFor(input.cwd);
  if (Object.keys(blob).length > 0) writeRaw(key, JSON.stringify(blob));
  else removeRaw(key);

  if (model && input.harnessUsesSerfModels) writeRaw(GLOBAL_MODEL_KEY, model);
  if (input.cwd.trim() !== "") writeRaw(GLOBAL_WORKING_DIR_KEY, input.cwd);
}

export type ModelValidity = "malformed" | "stale" | "unknown" | "valid";

// Classifies a stored model value against the live model list (floor §1.10,
// spawn.js:154-175): no "/" is malformed; an exact provider/model match is
// valid; a value whose provider IS enumerated but whose model is gone is stale;
// a provider not enumerated at all (e.g. OAuth-only anthropic, openrouter) is
// unknown - deliberately left untouched, the hub can't prove it is wrong.
export function modelValidityAgainstList(value: string, models: ModelDescriptor[]): ModelValidity {
  const slash = value.indexOf("/");
  if (slash === -1) return "malformed";
  const provider = value.slice(0, slash);
  let providerSeen = false;
  for (const m of models) {
    if (`${m.provider}/${m.model}` === value) return "valid";
    if (m.provider === provider) providerSeen = true;
  }
  return providerSeen ? "stale" : "unknown";
}

// Sweeps EVERY spawn-defaults blob plus the standalone global-model key,
// stripping model values classified malformed or stale (floor §1.10,
// spawn.js:177-246). An unknown or valid model is left in place. A blob left
// with no other fields is deleted outright. Returns the discarded model values
// so the caller can surface the inline "no longer offered by this hub" notice.
export function sweepStaleModels(models: ModelDescriptor[]): { discarded: string[] } {
  const discarded: string[] = [];

  const globalModel = readRaw(GLOBAL_MODEL_KEY);
  if (globalModel !== null && globalModel !== "") {
    const verdict = modelValidityAgainstList(globalModel, models);
    if (verdict === "malformed" || verdict === "stale") {
      removeRaw(GLOBAL_MODEL_KEY);
      discarded.push(globalModel);
    }
  }

  // Snapshot keys first: removeItem during iteration reindexes localStorage.
  const keys: string[] = [];
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (key?.startsWith(SPAWN_DEFAULTS_PREFIX) && !SCALAR_KEYS.has(key)) keys.push(key);
    }
  } catch {
    return { discarded };
  }

  for (const key of keys) {
    const raw = readRaw(key);
    if (raw === null) continue;
    let blob: SpawnDefaultsBlob;
    try {
      const parsed = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object") continue;
      blob = parsed as SpawnDefaultsBlob;
    } catch {
      continue;
    }
    if (typeof blob.model !== "string" || blob.model === "") continue;
    const verdict = modelValidityAgainstList(blob.model, models);
    if (verdict !== "malformed" && verdict !== "stale") continue;
    discarded.push(blob.model);
    delete blob.model;
    if (Object.keys(blob).length === 0) removeRaw(key);
    else writeRaw(key, JSON.stringify(blob));
  }

  return { discarded };
}
