// instanceModelEdit.ts: pure draft, diff, validation, and option helpers for
// InstanceSheet's per-model settings subview (InstanceModelEditor).

import type {
  InstanceModelAuthored,
  InstanceModelDetailResponse,
  InstanceModelEditParams,
  InstanceModelEntry,
} from "@evener/appwire-client";
import { EFFORT_TIERS, effortLabel, formatTokenCount, parseTokenCount, sameJsonValue } from "@evener/appwire-client";
import type { SegmentedControlOption, SelectOption } from "../../../../widgets";
import { PROTOCOL_OPTIONS, SURFACE_OPTIONS } from "./instanceEdit";

export const MIN_COMPACTION_THRESHOLD = 4096;
const DEFAULT_COMPACT_AT = 0.9;

export const ALL_EFFORT_LEVELS: readonly string[] = ["none", ...EFFORT_TIERS];

const DEFAULT_THINKING_FORMATS: readonly string[] = [
  "chat-template",
  "deepseek",
  "openai",
  "openrouter",
  "qwen",
  "qwen-chat-template",
  "string-thinking",
  "together",
  "zai",
];

const DEFAULT_THINKING_SHAPES: readonly string[] = ["adaptive", "budget", "budget+effort", "level"];

export interface ModelDraft {
  model: string;
  compactionThreshold: string;
  contextWindow: string;
  maxOutputTokens: string;
  reasoning: boolean | null;
  defaultEffort: string;
  effortValues: string[] | null;
  inputModalities: string[] | null;
  tools: boolean | null;
  protocol: string;
  surface: string;
  thinkingFormat: string;
  thinkingShape: string;
  wireId: string;
  aliasOf: string;
}

export interface ModelValidationErrors {
  model?: string;
  compactionThreshold?: string;
  contextWindow?: string;
  maxOutputTokens?: string;
  effortValues?: string;
  inputModalities?: string;
}

export function seedModelDraft(detail?: InstanceModelDetailResponse | null): ModelDraft {
  const authored = detail?.authored;
  return {
    model: detail?.model ?? "",
    compactionThreshold: authored?.compactionThreshold !== undefined ? String(authored.compactionThreshold) : "",
    contextWindow: authored?.contextWindow !== undefined ? String(authored.contextWindow) : "",
    maxOutputTokens: authored?.maxOutputTokens !== undefined ? String(authored.maxOutputTokens) : "",
    reasoning: authored?.reasoning ?? null,
    defaultEffort: authored?.defaultEffort ?? "",
    effortValues: authored?.effortValues !== undefined ? [...authored.effortValues] : null,
    inputModalities: authored?.inputModalities !== undefined ? [...authored.inputModalities] : null,
    tools: authored?.tools ?? null,
    protocol: authored?.protocol ?? "",
    surface: authored?.surface ?? "",
    thinkingFormat: authored?.thinkingFormat ?? "",
    thinkingShape: authored?.thinkingShape ?? "",
    wireId: authored?.wireId ?? "",
    aliasOf: authored?.aliasOf ?? "",
  };
}

export function hasAuthoredOverrides(authored: InstanceModelAuthored | undefined | null): boolean {
  if (!authored) return false;
  return (
    authored.compactionThreshold !== undefined ||
    authored.contextWindow !== undefined ||
    authored.maxOutputTokens !== undefined ||
    authored.reasoning !== undefined ||
    Boolean(authored.defaultEffort) ||
    authored.effortValues !== undefined ||
    authored.inputModalities !== undefined ||
    authored.tools !== undefined ||
    Boolean(authored.protocol) ||
    Boolean(authored.surface) ||
    Boolean(authored.thinkingFormat) ||
    Boolean(authored.thinkingShape) ||
    Boolean(authored.wireId) ||
    Boolean(authored.aliasOf)
  );
}

export function hasRoutingOverrides(authored: InstanceModelAuthored | undefined | null): boolean {
  if (!authored) return false;
  return (
    Boolean(authored.protocol) ||
    Boolean(authored.surface) ||
    Boolean(authored.thinkingFormat) ||
    Boolean(authored.thinkingShape) ||
    Boolean(authored.wireId) ||
    Boolean(authored.aliasOf)
  );
}

export function toggleEffortValue(
  current: readonly string[] | null,
  effective: readonly string[] | undefined,
  level: string,
): string[] {
  const base = current ?? effective ?? [];
  const present = new Set(base);
  if (present.has(level)) {
    present.delete(level);
  } else {
    present.add(level);
  }
  const ordered: string[] = [];
  for (const candidate of ALL_EFFORT_LEVELS) {
    if (present.has(candidate)) {
      ordered.push(candidate);
      present.delete(candidate);
    }
  }
  for (const extra of base) {
    if (present.has(extra)) {
      ordered.push(extra);
      present.delete(extra);
    }
  }
  return ordered;
}

export function toggleInputModality(
  current: readonly string[] | null,
  effective: readonly string[] | undefined,
  modality: "image" | "pdf",
  enabled: boolean,
): string[] {
  const base = current ?? effective ?? ["text"];
  const next: string[] = ["text"];
  for (const item of base) {
    if (item !== "text" && item !== "image" && item !== "pdf" && !next.includes(item)) {
      next.push(item);
    }
  }
  const hasImage = modality === "image" ? enabled : base.includes("image");
  const hasPdf = modality === "pdf" ? enabled : base.includes("pdf");
  if (hasImage) next.push("image");
  if (hasPdf) next.push("pdf");
  return next;
}

function effectiveHardCeiling(
  draft: ModelDraft,
  detail: InstanceModelDetailResponse | null | undefined,
  mode: "edit" | "create",
  initial?: ModelDraft,
): number {
  const rawCw = draft.contextWindow.trim();
  const draftedCw = rawCw === "" ? null : parseTokenCount(rawCw);
  const initialAliasOf = (initial?.aliasOf ?? detail?.authored?.aliasOf ?? "").trim();
  const aliasChanging = mode === "edit" && draft.aliasOf.trim() !== initialAliasOf;
  if (aliasChanging) {
    return draftedCw !== null && draftedCw > 0 ? draftedCw : 0;
  }
  const limits: number[] = [];
  const inheritedCw = inheritedField(detail?.effective.contextWindow, detail?.provenance.ContextWindow) ?? null;
  const effectiveCw = rawCw !== "" ? draftedCw : inheritedCw;
  if (effectiveCw !== null && effectiveCw > 0) {
    limits.push(effectiveCw);
  }
  const maxIn = detail?.effective.maxInputTokens;
  if (maxIn !== undefined && maxIn > 0) {
    limits.push(maxIn);
  }
  return limits.length > 0 ? Math.min(...limits) : 0;
}

export function validateModelDraft(
  draft: ModelDraft,
  detail: InstanceModelDetailResponse | null | undefined,
  existingModels: readonly InstanceModelEntry[],
  mode: "edit" | "create",
  initial?: ModelDraft,
): ModelValidationErrors {
  const errors: ModelValidationErrors = {};

  if (mode === "create") {
    const trimmedModel = draft.model.trim();
    if (trimmedModel === "") {
      errors.model = "Model ID is required.";
    } else if (trimmedModel.includes("*")) {
      errors.model = "Model ID cannot contain wildcards (*).";
    } else if (existingModels.some((entry) => entry.id === trimmedModel)) {
      errors.model = `Model "${trimmedModel}" already exists on this instance.`;
    }
  }

  const rawCw = draft.contextWindow.trim();
  if (rawCw !== "" && parseTokenCount(rawCw) === null) {
    errors.contextWindow = "Enter a positive token count (for example, 128K or 1M).";
  }

  const rawMaxOut = draft.maxOutputTokens.trim();
  if (rawMaxOut !== "" && parseTokenCount(rawMaxOut) === null) {
    errors.maxOutputTokens = "Enter a positive token count (for example, 32K or 64K).";
  }

  const rawCt = draft.compactionThreshold.trim();
  if (rawCt !== "") {
    const parsedCt = parseTokenCount(rawCt);
    if (parsedCt === null || parsedCt < MIN_COMPACTION_THRESHOLD) {
      errors.compactionThreshold = `Enter a token count of at least ${MIN_COMPACTION_THRESHOLD} (for example, 250K).`;
    } else {
      const ceiling = effectiveHardCeiling(draft, detail, mode, initial);
      if (ceiling > 0 && parsedCt >= ceiling) {
        errors.compactionThreshold = `Auto-compact threshold must be below the ${formatTokenCount(ceiling)} input limit.`;
      }
    }
  }

  if (draft.effortValues !== null && draft.effortValues.length === 0) {
    errors.effortValues = "Select at least one effort level, or reset to inherited.";
  }

  if (
    draft.inputModalities !== null &&
    (draft.inputModalities.length === 0 || !draft.inputModalities.includes("text"))
  ) {
    errors.inputModalities = "Input modalities must include text.";
  }

  return errors;
}

export function hasModelValidationErrors(errors: ModelValidationErrors): boolean {
  return Object.keys(errors).length > 0;
}

function sameStringArrayOrNull(a: readonly string[] | null, b: readonly string[] | null): boolean {
  return sameJsonValue(a, b);
}

function sameTokenField(aRaw: string, bRaw: string): boolean {
  const aTrim = aRaw.trim();
  const bTrim = bRaw.trim();
  if (aTrim === bTrim) return true;
  if (aTrim === "" || bTrim === "") return false;
  const aVal = parseTokenCount(aTrim);
  const bVal = parseTokenCount(bTrim);
  return aVal !== null && bVal !== null && aVal === bVal;
}

export function isModelDraftDirty(initial: ModelDraft, draft: ModelDraft, mode: "edit" | "create"): boolean {
  if (mode === "create") return draft.model.trim() !== "";
  return (
    !sameTokenField(initial.compactionThreshold, draft.compactionThreshold) ||
    !sameTokenField(initial.contextWindow, draft.contextWindow) ||
    !sameTokenField(initial.maxOutputTokens, draft.maxOutputTokens) ||
    initial.reasoning !== draft.reasoning ||
    initial.defaultEffort.trim() !== draft.defaultEffort.trim() ||
    !sameStringArrayOrNull(initial.effortValues, draft.effortValues) ||
    !sameStringArrayOrNull(initial.inputModalities, draft.inputModalities) ||
    initial.tools !== draft.tools ||
    initial.protocol.trim() !== draft.protocol.trim() ||
    initial.surface.trim() !== draft.surface.trim() ||
    initial.thinkingFormat.trim() !== draft.thinkingFormat.trim() ||
    initial.thinkingShape.trim() !== draft.thinkingShape.trim() ||
    initial.wireId.trim() !== draft.wireId.trim() ||
    initial.aliasOf.trim() !== draft.aliasOf.trim()
  );
}

export function modelEditParams(
  name: string,
  initial: ModelDraft,
  draft: ModelDraft,
  mode: "edit" | "create",
  expectedEndpointFingerprint?: string,
): InstanceModelEditParams | null {
  const modelId = mode === "create" ? draft.model.trim() : initial.model.trim();
  if (modelId === "") return null;

  const params: InstanceModelEditParams = {
    name,
    model: modelId,
  };
  let changed = false;

  if (mode === "create") {
    params.create = true;
    changed = true;
  }

  function diffToken(initialRaw: string, draftRaw: string, set: (val: number) => void, clear: () => void): void {
    if (sameTokenField(initialRaw, draftRaw)) return;
    const raw = draftRaw.trim();
    if (raw === "") {
      if (mode === "edit") {
        clear();
        changed = true;
      }
      return;
    }
    const parsed = parseTokenCount(raw);
    if (parsed !== null) {
      set(parsed);
      changed = true;
    }
  }

  function diffBool(
    initialVal: boolean | null,
    draftVal: boolean | null,
    set: (val: boolean) => void,
    clear: () => void,
  ): void {
    if (draftVal === initialVal) return;
    if (draftVal === null) {
      if (mode === "edit") {
        clear();
        changed = true;
      }
      return;
    }
    set(draftVal);
    changed = true;
  }

  function diffArray(
    initialVal: readonly string[] | null,
    draftVal: readonly string[] | null,
    set: (val: string[]) => void,
    clear: () => void,
  ): void {
    if (sameStringArrayOrNull(initialVal, draftVal)) return;
    if (draftVal === null) {
      if (mode === "edit") {
        clear();
        changed = true;
      }
    } else if (draftVal.length > 0) {
      set([...draftVal]);
      changed = true;
    }
  }

  function diffString(initialRaw: string, draftRaw: string, set: (val: string) => void, clear: () => void): void {
    const trimmed = draftRaw.trim();
    if (trimmed === initialRaw.trim()) return;
    if (trimmed === "") {
      if (mode === "edit") {
        clear();
        changed = true;
      }
      return;
    }
    set(trimmed);
    changed = true;
  }

  diffToken(
    initial.compactionThreshold,
    draft.compactionThreshold,
    (v) => {
      params.compactionThreshold = v;
    },
    () => {
      params.clearCompactionThreshold = true;
    },
  );
  diffToken(
    initial.contextWindow,
    draft.contextWindow,
    (v) => {
      params.contextWindow = v;
    },
    () => {
      params.clearContextWindow = true;
    },
  );
  diffToken(
    initial.maxOutputTokens,
    draft.maxOutputTokens,
    (v) => {
      params.maxOutputTokens = v;
    },
    () => {
      params.clearMaxOutputTokens = true;
    },
  );
  diffBool(
    initial.reasoning,
    draft.reasoning,
    (v) => {
      params.reasoning = v;
    },
    () => {
      params.clearReasoning = true;
    },
  );
  diffString(
    initial.defaultEffort,
    draft.defaultEffort,
    (v) => {
      params.defaultEffort = v;
    },
    () => {
      params.clearDefaultEffort = true;
    },
  );
  diffArray(
    initial.effortValues,
    draft.effortValues,
    (v) => {
      params.effortValues = v;
    },
    () => {
      params.clearEffortValues = true;
    },
  );
  diffArray(
    initial.inputModalities,
    draft.inputModalities,
    (v) => {
      params.inputModalities = v;
    },
    () => {
      params.clearInputModalities = true;
    },
  );
  diffBool(
    initial.tools,
    draft.tools,
    (v) => {
      params.tools = v;
    },
    () => {
      params.clearTools = true;
    },
  );
  diffString(
    initial.protocol,
    draft.protocol,
    (v) => {
      params.protocol = v;
    },
    () => {
      params.clearProtocol = true;
    },
  );
  diffString(
    initial.surface,
    draft.surface,
    (v) => {
      params.surface = v;
    },
    () => {
      params.clearSurface = true;
    },
  );
  diffString(
    initial.thinkingFormat,
    draft.thinkingFormat,
    (v) => {
      params.thinkingFormat = v;
    },
    () => {
      params.clearThinkingFormat = true;
    },
  );
  diffString(
    initial.thinkingShape,
    draft.thinkingShape,
    (v) => {
      params.thinkingShape = v;
    },
    () => {
      params.clearThinkingShape = true;
    },
  );
  diffString(
    initial.wireId,
    draft.wireId,
    (v) => {
      params.wireId = v;
    },
    () => {
      params.clearWireId = true;
    },
  );
  diffString(
    initial.aliasOf,
    draft.aliasOf,
    (v) => {
      params.aliasOf = v;
    },
    () => {
      params.clearAliasOf = true;
    },
  );

  if (!changed) return null;
  if (expectedEndpointFingerprint) {
    params.expectedEndpointFingerprint = expectedEndpointFingerprint;
  }
  return params;
}

export function formatModelRowSummary(row: Pick<InstanceModelEntry, "contextWindow" | "compactionThreshold">): string {
  const parts: string[] = [];
  if (row.contextWindow !== undefined && row.contextWindow > 0) {
    parts.push(`${formatTokenCount(row.contextWindow)} ctx`);
  }
  if (row.compactionThreshold !== undefined && row.compactionThreshold > 0) {
    parts.push(`compact ${formatTokenCount(row.compactionThreshold)}`);
  }
  return parts.join(" · ");
}

function inheritedField<T>(value: T | undefined, prov: string | undefined): T | undefined {
  return prov === "config" ? undefined : value;
}

function provenanceSuffix(prov: string | undefined): string {
  return prov && prov !== "config" ? ` (${prov})` : "";
}

function inheritedOptionLabel(value: string | undefined, prov: string | undefined): string {
  const inherited = inheritedField(value, prov);
  return inherited ? `Inherited (${inherited})` : "Inherited";
}

function vocabOptions(eff: string | undefined, prov: string | undefined, values: readonly string[]): SelectOption[] {
  return [{ value: "", label: inheritedOptionLabel(eff, prov) }, ...values.map((v) => ({ value: v, label: v }))];
}

export function compactionThresholdPlaceholder(
  detail?: InstanceModelDetailResponse | null,
  draftContextWindow = "",
): string {
  const thresholdProv = detail?.provenance.CompactionThreshold;
  const inheritedThreshold = inheritedField(detail?.effective.compactionThreshold, thresholdProv);
  if (inheritedThreshold !== undefined && inheritedThreshold > 0) {
    return `Inherited: ${formatTokenCount(inheritedThreshold)}${provenanceSuffix(thresholdProv)}`;
  }
  const rawDraftCw = draftContextWindow.trim();
  const draftedCw = rawDraftCw === "" ? null : parseTokenCount(rawDraftCw);
  const inheritedCw = inheritedField(detail?.effective.contextWindow, detail?.provenance.ContextWindow);
  const cw = draftedCw ?? inheritedCw;
  if (cw !== undefined && cw > 0) {
    const defaultTokens = Math.round(cw * DEFAULT_COMPACT_AT);
    return `Default: ${formatTokenCount(defaultTokens)} (90% of ${formatTokenCount(cw)} window)`;
  }
  return "Default: 90% of context window";
}

export function tokenLimitPlaceholder(value: number | undefined, prov: string | undefined): string {
  const inherited = inheritedField(value, prov);
  if (inherited !== undefined && inherited > 0) {
    return `Inherited: ${formatTokenCount(inherited)}${provenanceSuffix(prov)}`;
  }
  return "Inherited";
}

export function wireIdPlaceholder(detail?: InstanceModelDetailResponse | null): string {
  const prov = detail?.provenance.WireID;
  const wireId = inheritedField(detail?.effective.wireId, prov);
  if (wireId) {
    return `Inherited: ${wireId}${provenanceSuffix(prov)}`;
  }
  return "Inherited";
}

export function aliasOfPlaceholder(detail?: InstanceModelDetailResponse | null): string {
  const prov = detail?.provenance.AliasOf;
  const alias = inheritedField(detail?.effective.aliasOf, prov);
  if (alias) {
    return `Inherited: ${alias}${provenanceSuffix(prov)}`;
  }
  return "None";
}

export function reasoningOptions(detail?: InstanceModelDetailResponse | null): SegmentedControlOption[] {
  const stateLabel = detail?.effective.reasoning !== false ? "On" : "Off";
  const leadingLabel = inheritedOptionLabel(stateLabel, detail?.provenance.Reasoning);
  return [
    { value: "", label: leadingLabel },
    { value: "true", label: "On" },
    { value: "false", label: "Off" },
  ];
}

export function defaultEffortOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const eff = inheritedField(detail?.effective.defaultEffort, detail?.provenance.DefaultEffort);
  const ladder = detail?.effective.effortValues ?? [...ALL_EFFORT_LEVELS];
  const leadingLabel = eff ? `Inherited (${effortLabel(eff, ladder)})` : "Inherited";
  return [
    { value: "", label: leadingLabel },
    ...ALL_EFFORT_LEVELS.map((level) => ({ value: level, label: effortLabel(level, ladder) })),
  ];
}

export function toolCallingOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const stateLabel = detail?.effective.tools !== false ? "Supported" : "Unsupported";
  const leadingLabel = inheritedOptionLabel(stateLabel, detail?.provenance.Tools);
  return [
    { value: "", label: leadingLabel },
    { value: "true", label: "Supported" },
    { value: "false", label: "Unsupported" },
  ];
}

export function modelProtocolOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const leadingLabel = inheritedOptionLabel(detail?.effective.protocol, detail?.provenance.Protocol);
  return [{ value: "", label: leadingLabel }, ...PROTOCOL_OPTIONS.slice(1)];
}

export function modelSurfaceOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const leadingLabel = inheritedOptionLabel(detail?.effective.surface, detail?.provenance.Surface);
  return [{ value: "", label: leadingLabel }, ...SURFACE_OPTIONS.slice(1)];
}

export function thinkingFormatOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  return vocabOptions(
    detail?.effective.thinkingFormat,
    detail?.provenance.ThinkingFormat,
    detail?.thinkingFormats ?? DEFAULT_THINKING_FORMATS,
  );
}

export function thinkingShapeOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  return vocabOptions(
    detail?.effective.thinkingShape,
    detail?.provenance.ThinkingShape,
    detail?.thinkingShapes ?? DEFAULT_THINKING_SHAPES,
  );
}
