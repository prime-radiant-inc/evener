// instanceModelEdit.ts: pure draft, diff, validation, and option helpers for
// InstanceSheet's per-model settings subview (InstanceModelEditor).

import type {
  InstanceModelAuthored,
  InstanceModelDetailResponse,
  InstanceModelEditParams,
  InstanceModelEntry,
} from "@evener/appwire-client";
import { EFFORT_TIERS, effortLabel, formatTokenCount, parseTokenCount } from "@evener/appwire-client";
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
  const limits: number[] = [];
  const rawCw = draft.contextWindow.trim();
  const draftedCw = rawCw === "" ? null : parseTokenCount(rawCw);
  const initialAliasOf = (initial?.aliasOf ?? detail?.authored?.aliasOf ?? "").trim();
  const aliasChanging = mode === "edit" && draft.aliasOf.trim() !== initialAliasOf;
  if (aliasChanging) {
    if (draftedCw !== null && draftedCw > 0) {
      limits.push(draftedCw);
    }
    return limits.length > 0 ? Math.min(...limits) : 0;
  }
  const inheritedCw = detail?.provenance.ContextWindow === "config" ? null : (detail?.effective.contextWindow ?? null);
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
  if (a === b) return true;
  if (a === null || b === null) return false;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return false;
  }
  return true;
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

  if (!sameTokenField(initial.compactionThreshold, draft.compactionThreshold)) {
    const raw = draft.compactionThreshold.trim();
    if (raw === "") {
      if (mode === "edit") {
        params.clearCompactionThreshold = true;
        changed = true;
      }
    } else {
      const parsed = parseTokenCount(raw);
      if (parsed !== null) {
        params.compactionThreshold = parsed;
        changed = true;
      }
    }
  }

  if (!sameTokenField(initial.contextWindow, draft.contextWindow)) {
    const raw = draft.contextWindow.trim();
    if (raw === "") {
      if (mode === "edit") {
        params.clearContextWindow = true;
        changed = true;
      }
    } else {
      const parsed = parseTokenCount(raw);
      if (parsed !== null) {
        params.contextWindow = parsed;
        changed = true;
      }
    }
  }

  if (!sameTokenField(initial.maxOutputTokens, draft.maxOutputTokens)) {
    const raw = draft.maxOutputTokens.trim();
    if (raw === "") {
      if (mode === "edit") {
        params.clearMaxOutputTokens = true;
        changed = true;
      }
    } else {
      const parsed = parseTokenCount(raw);
      if (parsed !== null) {
        params.maxOutputTokens = parsed;
        changed = true;
      }
    }
  }

  if (draft.reasoning !== initial.reasoning) {
    if (draft.reasoning === null) {
      if (mode === "edit") {
        params.clearReasoning = true;
        changed = true;
      }
    } else {
      params.reasoning = draft.reasoning;
      changed = true;
    }
  }

  const draftDefaultEffort = draft.defaultEffort.trim();
  if (draftDefaultEffort !== initial.defaultEffort.trim()) {
    if (draftDefaultEffort === "") {
      if (mode === "edit") {
        params.clearDefaultEffort = true;
        changed = true;
      }
    } else {
      params.defaultEffort = draftDefaultEffort;
      changed = true;
    }
  }

  if (!sameStringArrayOrNull(initial.effortValues, draft.effortValues)) {
    if (draft.effortValues === null) {
      if (mode === "edit") {
        params.clearEffortValues = true;
        changed = true;
      }
    } else if (draft.effortValues.length > 0) {
      params.effortValues = [...draft.effortValues];
      changed = true;
    }
  }

  if (!sameStringArrayOrNull(initial.inputModalities, draft.inputModalities)) {
    if (draft.inputModalities === null) {
      if (mode === "edit") {
        params.clearInputModalities = true;
        changed = true;
      }
    } else if (draft.inputModalities.length > 0) {
      params.inputModalities = [...draft.inputModalities];
      changed = true;
    }
  }

  if (draft.tools !== initial.tools) {
    if (draft.tools === null) {
      if (mode === "edit") {
        params.clearTools = true;
        changed = true;
      }
    } else {
      params.tools = draft.tools;
      changed = true;
    }
  }

  const draftProtocol = draft.protocol.trim();
  if (draftProtocol !== initial.protocol.trim()) {
    if (draftProtocol === "") {
      if (mode === "edit") {
        params.clearProtocol = true;
        changed = true;
      }
    } else {
      params.protocol = draftProtocol;
      changed = true;
    }
  }

  const draftSurface = draft.surface.trim();
  if (draftSurface !== initial.surface.trim()) {
    if (draftSurface === "") {
      if (mode === "edit") {
        params.clearSurface = true;
        changed = true;
      }
    } else {
      params.surface = draftSurface;
      changed = true;
    }
  }

  const draftThinkingFormat = draft.thinkingFormat.trim();
  if (draftThinkingFormat !== initial.thinkingFormat.trim()) {
    if (draftThinkingFormat === "") {
      if (mode === "edit") {
        params.clearThinkingFormat = true;
        changed = true;
      }
    } else {
      params.thinkingFormat = draftThinkingFormat;
      changed = true;
    }
  }

  const draftThinkingShape = draft.thinkingShape.trim();
  if (draftThinkingShape !== initial.thinkingShape.trim()) {
    if (draftThinkingShape === "") {
      if (mode === "edit") {
        params.clearThinkingShape = true;
        changed = true;
      }
    } else {
      params.thinkingShape = draftThinkingShape;
      changed = true;
    }
  }

  const draftWireId = draft.wireId.trim();
  if (draftWireId !== initial.wireId.trim()) {
    if (draftWireId === "") {
      if (mode === "edit") {
        params.clearWireId = true;
        changed = true;
      }
    } else {
      params.wireId = draftWireId;
      changed = true;
    }
  }

  const draftAliasOf = draft.aliasOf.trim();
  if (draftAliasOf !== initial.aliasOf.trim()) {
    if (draftAliasOf === "") {
      if (mode === "edit") {
        params.clearAliasOf = true;
        changed = true;
      }
    } else {
      params.aliasOf = draftAliasOf;
      changed = true;
    }
  }

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

function provenanceSuffix(prov: string | undefined): string {
  return prov && prov !== "config" ? ` (${prov})` : "";
}

export function compactionThresholdPlaceholder(
  detail?: InstanceModelDetailResponse | null,
  draftContextWindow = "",
): string {
  const inheritedThreshold = detail?.effective.compactionThreshold;
  const thresholdProv = detail?.provenance.CompactionThreshold;
  if (inheritedThreshold !== undefined && inheritedThreshold > 0 && thresholdProv !== "config") {
    return `Inherited: ${formatTokenCount(inheritedThreshold)}${provenanceSuffix(thresholdProv)}`;
  }
  const rawDraftCw = draftContextWindow.trim();
  const draftedCw = rawDraftCw === "" ? null : parseTokenCount(rawDraftCw);
  const cw =
    draftedCw !== null
      ? draftedCw
      : detail?.provenance.ContextWindow === "config"
        ? undefined
        : detail?.effective.contextWindow;
  if (cw !== undefined && cw > 0) {
    const defaultTokens = Math.round(cw * DEFAULT_COMPACT_AT);
    return `Default: ${formatTokenCount(defaultTokens)} (90% of ${formatTokenCount(cw)} window)`;
  }
  return "Default: 90% of context window";
}

export function tokenLimitPlaceholder(value: number | undefined, prov: string | undefined): string {
  if (value !== undefined && value > 0 && prov !== "config") {
    return `Inherited: ${formatTokenCount(value)}${provenanceSuffix(prov)}`;
  }
  return "Inherited";
}

export function wireIdPlaceholder(detail?: InstanceModelDetailResponse | null): string {
  const wireId = detail?.effective.wireId;
  const prov = detail?.provenance.WireID;
  if (wireId && prov !== "config") {
    return `Inherited: ${wireId}${provenanceSuffix(prov)}`;
  }
  return "Inherited";
}

export function aliasOfPlaceholder(detail?: InstanceModelDetailResponse | null): string {
  const alias = detail?.effective.aliasOf;
  const prov = detail?.provenance.AliasOf;
  if (alias && prov !== "config") {
    return `Inherited: ${alias}${provenanceSuffix(prov)}`;
  }
  return "None";
}

export function reasoningOptions(detail?: InstanceModelDetailResponse | null): SegmentedControlOption[] {
  const prov = detail?.provenance.Reasoning;
  const inheritedOn = detail?.effective.reasoning !== false;
  const leadingLabel = prov === "config" ? "Inherited" : inheritedOn ? "Inherited (On)" : "Inherited (Off)";
  return [
    { value: "", label: leadingLabel },
    { value: "true", label: "On" },
    { value: "false", label: "Off" },
  ];
}

export function defaultEffortOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const eff = detail?.effective.defaultEffort;
  const prov = detail?.provenance.DefaultEffort;
  const ladder = detail?.effective.effortValues ?? [...ALL_EFFORT_LEVELS];
  const leadingLabel = eff && prov !== "config" ? `Inherited (${effortLabel(eff, ladder)})` : "Inherited";
  return [
    { value: "", label: leadingLabel },
    ...ALL_EFFORT_LEVELS.map((level) => ({ value: level, label: effortLabel(level, ladder) })),
  ];
}

export function toolCallingOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const prov = detail?.provenance.Tools;
  const inheritedSupported = detail?.effective.tools !== false;
  const leadingLabel =
    prov === "config" ? "Inherited" : inheritedSupported ? "Inherited (Supported)" : "Inherited (Unsupported)";
  return [
    { value: "", label: leadingLabel },
    { value: "true", label: "Supported" },
    { value: "false", label: "Unsupported" },
  ];
}

export function modelProtocolOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const eff = detail?.effective.protocol;
  const prov = detail?.provenance.Protocol;
  const leadingLabel = eff && prov !== "config" ? `Inherited (${eff})` : "Inherited";
  return [{ value: "", label: leadingLabel }, ...PROTOCOL_OPTIONS.slice(1)];
}

export function modelSurfaceOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const eff = detail?.effective.surface;
  const prov = detail?.provenance.Surface;
  const leadingLabel = eff && prov !== "config" ? `Inherited (${eff})` : "Inherited";
  return [{ value: "", label: leadingLabel }, ...SURFACE_OPTIONS.slice(1)];
}

export function thinkingFormatOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const eff = detail?.effective.thinkingFormat;
  const prov = detail?.provenance.ThinkingFormat;
  const leadingLabel = eff && prov !== "config" ? `Inherited (${eff})` : "Inherited";
  const formats = detail?.thinkingFormats ?? DEFAULT_THINKING_FORMATS;
  return [{ value: "", label: leadingLabel }, ...formats.map((f) => ({ value: f, label: f }))];
}

export function thinkingShapeOptions(detail?: InstanceModelDetailResponse | null): SelectOption[] {
  const eff = detail?.effective.thinkingShape;
  const prov = detail?.provenance.ThinkingShape;
  const leadingLabel = eff && prov !== "config" ? `Inherited (${eff})` : "Inherited";
  const shapes = detail?.thinkingShapes ?? DEFAULT_THINKING_SHAPES;
  return [{ value: "", label: leadingLabel }, ...shapes.map((s) => ({ value: s, label: s }))];
}
