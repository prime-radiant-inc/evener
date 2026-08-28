import type {
  TranscriptDisplayAdvanced as WireAdvanced,
  TranscriptDisplayConfig as WireConfig,
  TranscriptDisplayContent as WireContent,
  TranscriptDisplayDefault as WireDefault,
  TranscriptDisplayDefaults as WireDefaults,
} from "../protocol/types.gen";

export type ContentLevel = "chat" | "intent" | "tools" | "activity" | "full";
export type TranscriptLevel = ContentLevel;
export type HookExitDetail = "none" | "successful" | "all";
export type TranscriptHookExitDetail = HookExitDetail;
export type TranscriptViewportClass = "desktop" | "mobile";
export type ViewportClass = TranscriptViewportClass;

export interface ContentVector {
  readonly toolIntent: boolean;
  readonly toolCalls: boolean;
  readonly reasoning: boolean;
  readonly expandByDefault: boolean;
}

export type ContentSelection =
  | { readonly kind: "preset"; readonly level: ContentLevel }
  | ({ readonly kind: "custom" } & ContentVector);

export interface TranscriptDisplayAdvanced {
  readonly roundTimings: boolean;
  readonly tokenCounts: boolean;
  readonly estimatedCost: boolean;
  readonly systemEvents: boolean;
  readonly promptEvents: boolean;
  readonly hookExits: HookExitDetail;
}

export interface TranscriptDisplayConfigV1 {
  readonly version: 1;
  readonly content: ContentSelection;
  readonly advanced: Readonly<TranscriptDisplayAdvanced>;
}

export type TranscriptDisplayConfig = TranscriptDisplayConfigV1;

export interface HubTranscriptDisplayDefault {
  readonly revision: number;
  readonly config: TranscriptDisplayConfigV1;
}

export const CONTENT_LEVELS = ["chat", "intent", "tools", "activity", "full"] as const;
export const HOOK_EXIT_DETAILS = ["none", "successful", "all"] as const;

const CONTENT_VECTORS: Readonly<Record<ContentLevel, ContentVector>> = {
  chat: { toolIntent: true, toolCalls: false, reasoning: false, expandByDefault: false },
  intent: { toolIntent: true, toolCalls: false, reasoning: false, expandByDefault: false },
  tools: { toolIntent: true, toolCalls: true, reasoning: false, expandByDefault: false },
  activity: { toolIntent: true, toolCalls: true, reasoning: true, expandByDefault: false },
  full: { toolIntent: true, toolCalls: true, reasoning: true, expandByDefault: true },
};

const ADVANCED_DEFAULTS: TranscriptDisplayAdvanced = {
  roundTimings: false,
  tokenCounts: false,
  estimatedCost: false,
  systemEvents: false,
  promptEvents: false,
  hookExits: "none",
};

function cloneVector(vector: ContentVector): ContentVector {
  return {
    toolIntent: vector.toolIntent,
    toolCalls: vector.toolCalls,
    reasoning: vector.reasoning,
    expandByDefault: vector.expandByDefault,
  };
}

function cloneAdvanced(advanced: TranscriptDisplayAdvanced): TranscriptDisplayAdvanced {
  return {
    roundTimings: advanced.roundTimings,
    tokenCounts: advanced.tokenCounts,
    estimatedCost: advanced.estimatedCost,
    systemEvents: advanced.systemEvents,
    promptEvents: advanced.promptEvents,
    hookExits: advanced.hookExits,
  };
}

function cloneConfig(config: TranscriptDisplayConfigV1): TranscriptDisplayConfigV1 {
  return {
    version: 1,
    content:
      config.content.kind === "preset"
        ? { kind: "preset", level: config.content.level }
        : { kind: "custom", ...cloneVector(config.content) },
    advanced: cloneAdvanced(config.advanced),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasExactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const actual = Object.keys(value);
  return actual.length === keys.length && keys.every((key) => Object.hasOwn(value, key));
}

function isContentLevel(value: unknown): value is ContentLevel {
  return typeof value === "string" && (CONTENT_LEVELS as readonly string[]).includes(value);
}

function isHookExitDetail(value: unknown): value is HookExitDetail {
  return typeof value === "string" && (HOOK_EXIT_DETAILS as readonly string[]).includes(value);
}

function isBoolean(value: unknown): value is boolean {
  return typeof value === "boolean";
}

function isContentVector(value: unknown): value is ContentVector {
  return (
    isRecord(value) &&
    hasExactKeys(value, ["toolIntent", "toolCalls", "reasoning", "expandByDefault"]) &&
    isBoolean(value.toolIntent) &&
    isBoolean(value.toolCalls) &&
    isBoolean(value.reasoning) &&
    isBoolean(value.expandByDefault)
  );
}

function isCustomSelection(value: unknown): value is { readonly kind: "custom" } & ContentVector {
  return (
    isRecord(value) &&
    hasExactKeys(value, ["kind", "toolIntent", "toolCalls", "reasoning", "expandByDefault"]) &&
    value.kind === "custom" &&
    isBoolean(value.toolIntent) &&
    isBoolean(value.toolCalls) &&
    isBoolean(value.reasoning) &&
    isBoolean(value.expandByDefault)
  );
}

export function presetContent(level: ContentLevel): ContentVector {
  return cloneVector(CONTENT_VECTORS[level]);
}

export function normalizeContent(content: ContentSelection): ContentSelection {
  if (content.kind === "preset") {
    // Rebuild named presets instead of retaining a caller-owned object. This
    // also keeps a runtime caller that supplied a widened string from leaking
    // an unsupported level into the canonical representation.
    if (!isContentLevel(content.level)) throw new Error("unsupported transcript content level");
    return { kind: "preset", level: content.level };
  }

  if (!isCustomSelection(content)) throw new Error("invalid custom transcript content");
  return { kind: "custom", ...cloneVector(content) };
}

export function normalizeConfig(config: TranscriptDisplayConfigV1): TranscriptDisplayConfigV1 {
  if (config.version !== 1) throw new Error("unsupported transcript display config version");
  const content = normalizeContent(config.content);
  if (!isRecord(config.advanced)) throw new Error("invalid transcript display advanced settings");
  if (
    !isBoolean(config.advanced.roundTimings) ||
    !isBoolean(config.advanced.tokenCounts) ||
    !isBoolean(config.advanced.estimatedCost) ||
    !isBoolean(config.advanced.systemEvents) ||
    !isBoolean(config.advanced.promptEvents) ||
    !isHookExitDetail(config.advanced.hookExits)
  ) {
    throw new Error("invalid transcript display advanced settings");
  }
  return {
    version: 1,
    content,
    advanced: cloneAdvanced(config.advanced),
  };
}

export function makeTranscriptDisplayConfig(
  content: ContentSelection = { kind: "preset", level: "chat" },
  advanced: Partial<TranscriptDisplayAdvanced> = {},
): TranscriptDisplayConfigV1 {
  return normalizeConfig({
    version: 1,
    content,
    advanced: { ...ADVANCED_DEFAULTS, ...advanced },
  });
}

export const shippedDesktopConfig: TranscriptDisplayConfigV1 = makeTranscriptDisplayConfig({
  kind: "preset",
  level: "tools",
});
export const shippedMobileConfig: TranscriptDisplayConfigV1 = makeTranscriptDisplayConfig({
  kind: "preset",
  level: "intent",
});
export const SHIPPED_DESKTOP_CONFIG = shippedDesktopConfig;
export const SHIPPED_MOBILE_CONFIG = shippedMobileConfig;

export const shippedDefaults: Readonly<Record<TranscriptViewportClass, HubTranscriptDisplayDefault>> = {
  desktop: { revision: 0, config: shippedDesktopConfig },
  mobile: { revision: 0, config: shippedMobileConfig },
};
export const SHIPPED_DEFAULTS = shippedDefaults;

export function shippedConfig(layout: TranscriptViewportClass = "desktop"): TranscriptDisplayConfigV1 {
  return cloneConfig(shippedDefaults[layout].config);
}

export function shippedDefault(layout: TranscriptViewportClass = "desktop"): HubTranscriptDisplayDefault {
  return { revision: 0, config: shippedConfig(layout) };
}

export interface EffectiveConfigSources {
  readonly local?: TranscriptDisplayConfigV1 | null;
  readonly hub?: HubTranscriptDisplayDefault | TranscriptDisplayConfigV1 | null;
  readonly shipped?: TranscriptDisplayConfigV1 | null;
  readonly layout?: TranscriptViewportClass;
}

type ConfigCandidate = TranscriptDisplayConfigV1 | null | undefined;
type HubCandidate = HubTranscriptDisplayDefault | TranscriptDisplayConfigV1 | null | undefined;
type ShippedCandidate = TranscriptDisplayConfigV1 | TranscriptViewportClass | null | undefined;

function candidateConfig(value: ConfigCandidate | HubCandidate): TranscriptDisplayConfigV1 | undefined {
  if (value === null || value === undefined) return undefined;
  if (isRecord(value) && "config" in value && isRecord(value.config))
    return value.config as unknown as TranscriptDisplayConfigV1;
  return value as TranscriptDisplayConfigV1;
}

export function resolveEffectiveConfig(sources: EffectiveConfigSources): TranscriptDisplayConfigV1;
export function resolveEffectiveConfig(
  local: ConfigCandidate,
  hub?: HubCandidate,
  shipped?: ShippedCandidate,
): TranscriptDisplayConfigV1;
export function resolveEffectiveConfig(
  localOrSources: ConfigCandidate | EffectiveConfigSources,
  hub?: HubCandidate,
  shipped?: ShippedCandidate,
): TranscriptDisplayConfigV1 {
  let local: ConfigCandidate;
  let hubValue: HubCandidate;
  let shippedValue: ShippedCandidate;
  if (
    isRecord(localOrSources) &&
    (Object.hasOwn(localOrSources, "local") ||
      Object.hasOwn(localOrSources, "hub") ||
      Object.hasOwn(localOrSources, "shipped") ||
      Object.hasOwn(localOrSources, "layout"))
  ) {
    const sources = localOrSources as unknown as EffectiveConfigSources;
    local = sources.local;
    hubValue = sources.hub;
    shippedValue = sources.shipped ?? sources.layout;
  } else {
    local = localOrSources as ConfigCandidate;
    hubValue = hub;
    shippedValue = shipped;
  }

  const selected = candidateConfig(local) ?? candidateConfig(hubValue);
  if (selected !== undefined) return normalizeConfig(selected);

  let fallback: TranscriptDisplayConfigV1;
  if (shippedValue === "desktop" || shippedValue === "mobile") {
    fallback = shippedConfig(shippedValue);
  } else if (shippedValue === null || shippedValue === undefined) {
    fallback = shippedConfig("desktop");
  } else {
    fallback = shippedValue;
  }
  return normalizeConfig(fallback);
}

function wireContent(content: ContentSelection): WireContent {
  if (content.kind === "preset") return { kind: "preset", level: content.level };
  return { kind: "custom", custom: cloneVector(content) };
}

function wireAdvanced(advanced: TranscriptDisplayAdvanced): WireAdvanced {
  return {
    roundTimings: advanced.roundTimings,
    tokenCounts: advanced.tokenCounts,
    estimatedCost: advanced.estimatedCost,
    systemEvents: advanced.systemEvents,
    promptEvents: advanced.promptEvents,
    hookExits: advanced.hookExits,
  };
}

export function toWireConfig(config: TranscriptDisplayConfigV1): WireConfig {
  const normalized = normalizeConfig(config);
  return {
    version: 1,
    content: wireContent(normalized.content),
    advanced: wireAdvanced(normalized.advanced),
  };
}

function readWireContent(value: unknown): ContentSelection | undefined {
  if (!isRecord(value) || typeof value.kind !== "string") return undefined;
  if (value.kind === "preset") {
    if (!hasExactKeys(value, ["kind", "level"]) || !isContentLevel(value.level)) return undefined;
    return { kind: "preset", level: value.level };
  }
  if (value.kind === "custom") {
    if (!hasExactKeys(value, ["kind", "custom"]) || !isContentVector(value.custom)) return undefined;
    return { kind: "custom", ...value.custom };
  }
  return undefined;
}

function readWireAdvanced(value: unknown): TranscriptDisplayAdvanced | undefined {
  if (
    !isRecord(value) ||
    !hasExactKeys(value, [
      "roundTimings",
      "tokenCounts",
      "estimatedCost",
      "systemEvents",
      "promptEvents",
      "hookExits",
    ]) ||
    !isBoolean(value.roundTimings) ||
    !isBoolean(value.tokenCounts) ||
    !isBoolean(value.estimatedCost) ||
    !isBoolean(value.systemEvents) ||
    !isBoolean(value.promptEvents) ||
    !isHookExitDetail(value.hookExits)
  ) {
    return undefined;
  }
  return {
    roundTimings: value.roundTimings,
    tokenCounts: value.tokenCounts,
    estimatedCost: value.estimatedCost,
    systemEvents: value.systemEvents,
    promptEvents: value.promptEvents,
    hookExits: value.hookExits,
  };
}

export function fromWireConfig(value: unknown): TranscriptDisplayConfigV1 | undefined {
  if (!isRecord(value) || !hasExactKeys(value, ["version", "content", "advanced"]) || value.version !== 1)
    return undefined;
  const content = readWireContent(value.content);
  const advanced = readWireAdvanced(value.advanced);
  if (content === undefined || advanced === undefined) return undefined;
  return normalizeConfig({ version: 1, content, advanced });
}

export const wireToConfig = fromWireConfig;
export const configToWire = toWireConfig;
export const fromWireTranscriptDisplayConfig = fromWireConfig;
export const toWireTranscriptDisplayConfig = toWireConfig;

export function toWireDefault(value: HubTranscriptDisplayDefault): WireDefault {
  if (!Number.isSafeInteger(value.revision) || value.revision < 0)
    throw new Error("invalid transcript display revision");
  return { revision: value.revision, config: toWireConfig(value.config) };
}

export function fromWireDefault(value: unknown): HubTranscriptDisplayDefault | undefined {
  if (
    !isRecord(value) ||
    !hasExactKeys(value, ["revision", "config"]) ||
    !Number.isSafeInteger(value.revision) ||
    (value.revision as number) < 0
  ) {
    return undefined;
  }
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { revision: value.revision as number, config };
}

export function toWireDefaults(
  value: Readonly<Record<TranscriptViewportClass, HubTranscriptDisplayDefault>>,
): WireDefaults {
  return { desktop: toWireDefault(value.desktop), mobile: toWireDefault(value.mobile) };
}

export function fromWireDefaults(
  value: unknown,
): Readonly<Record<TranscriptViewportClass, HubTranscriptDisplayDefault>> | undefined {
  if (!isRecord(value) || !hasExactKeys(value, ["desktop", "mobile"])) return undefined;
  const desktop = fromWireDefault(value.desktop);
  const mobile = fromWireDefault(value.mobile);
  return desktop === undefined || mobile === undefined ? undefined : { desktop, mobile };
}

export const wireToDefault = fromWireDefault;
export const defaultToWire = toWireDefault;
export const wireToDefaults = fromWireDefaults;
export const defaultsToWire = toWireDefaults;

export function encodeLocalConfig(config: TranscriptDisplayConfigV1): string {
  return JSON.stringify(toWireConfig(config));
}

export function decodeLocalConfig(raw: unknown): TranscriptDisplayConfigV1 | undefined {
  if (typeof raw !== "string") return undefined;
  try {
    return fromWireConfig(JSON.parse(raw));
  } catch {
    return undefined;
  }
}

export const encodeConfig = encodeLocalConfig;
export const decodeConfig = decodeLocalConfig;
export const parseLocalConfig = decodeLocalConfig;
export const encodeLocal = encodeLocalConfig;
export const decodeLocal = decodeLocalConfig;

export function configFingerprint(config: TranscriptDisplayConfigV1): string {
  return encodeLocalConfig(config);
}

export const fingerprintConfig = configFingerprint;

const CONTENT_LABELS: Record<ContentLevel, string> = {
  chat: "Chat",
  intent: "Intent",
  tools: "Tools",
  activity: "Activity",
  full: "Full",
};

export function contentSummary(content: ContentSelection): string {
  const normalized = normalizeContent(content);
  return normalized.kind === "preset" ? CONTENT_LABELS[normalized.level] : "Custom";
}

export function advancedEnabledCount(config: TranscriptDisplayConfigV1): number {
  const advanced = normalizeConfig(config).advanced;
  return [
    advanced.roundTimings,
    advanced.tokenCounts,
    advanced.estimatedCost,
    advanced.systemEvents,
    advanced.promptEvents,
    advanced.hookExits !== "none",
  ].filter(Boolean).length;
}

export function configSummary(config: TranscriptDisplayConfigV1): string {
  const count = advancedEnabledCount(config);
  return `${contentSummary(config.content)} · ${count} advanced`;
}

/** Concise status text for assistive announcements, not the narrow visual track. */
export function accessibleConfigSummary(config: TranscriptDisplayConfigV1): string {
  const normalized = normalizeConfig(config);
  let content: string;
  if (normalized.content.kind === "preset" && normalized.content.level === "full") {
    content = "Full detail";
  } else if (normalized.content.kind === "custom") {
    content = "Custom content";
  } else {
    content = contentSummary(normalized.content);
  }
  const count = advancedEnabledCount(normalized);
  return count === 0 ? content : `${content} · ${count} advanced`;
}

export type TranscriptDisplayCategory =
  | "userMessages"
  | "agentMessages"
  | "criticalRows"
  | "toolIntent"
  | "toolCalls"
  | "reasoning"
  | "expandedDetails"
  | "roundTimings"
  | "tokenCounts"
  | "estimatedCost"
  | "systemEvents"
  | "promptEvents"
  | "hookExits";

export interface VisibleCategoryInventory {
  readonly visible: readonly TranscriptDisplayCategory[];
  readonly hidden: readonly TranscriptDisplayCategory[];
}

export function visibleCategoryInventory(config: TranscriptDisplayConfigV1): VisibleCategoryInventory {
  const normalized = normalizeConfig(config);
  const visible: TranscriptDisplayCategory[] = ["userMessages", "agentMessages", "criticalRows"];
  const hidden: TranscriptDisplayCategory[] = [];
  const content = normalized.content.kind === "preset" ? presetContent(normalized.content.level) : normalized.content;
  const contentCategories: readonly [keyof ContentVector, TranscriptDisplayCategory][] = [
    ["toolIntent", "toolIntent"],
    ["toolCalls", "toolCalls"],
    ["reasoning", "reasoning"],
    ["expandByDefault", "expandedDetails"],
  ];
  for (const [field, category] of contentCategories) (content[field] ? visible : hidden).push(category);

  const advanced = normalized.advanced;
  const advancedCategories: readonly [boolean, TranscriptDisplayCategory][] = [
    [advanced.roundTimings, "roundTimings"],
    [advanced.tokenCounts, "tokenCounts"],
    [advanced.estimatedCost, "estimatedCost"],
    [advanced.systemEvents, "systemEvents"],
    [advanced.promptEvents, "promptEvents"],
    [advanced.hookExits !== "none", "hookExits"],
  ];
  for (const [enabled, category] of advancedCategories) (enabled ? visible : hidden).push(category);
  return { visible, hidden };
}

export const categoryInventory = visibleCategoryInventory;

export const LEGACY_PREF_KEYS = [
  "transcriptRoundTimings",
  "transcriptTokenCounts",
  "transcriptHookExitsAll",
  "transcriptHookExitsNormal",
  "transcriptPromptLoaded",
  "showCost",
] as const;
export type LegacyPreferenceKey = (typeof LEGACY_PREF_KEYS)[number];

export interface LegacyPreferenceValues {
  readonly transcriptRoundTimings?: string | null;
  readonly transcriptTokenCounts?: string | null;
  readonly transcriptHookExitsAll?: string | null;
  readonly transcriptHookExitsNormal?: string | null;
  readonly transcriptPromptLoaded?: string | null;
  readonly showCost?: string | null;
}

function hasLegacyValue(values: LegacyPreferenceValues, key: LegacyPreferenceKey): boolean {
  return values[key] !== undefined && values[key] !== null;
}

function legacyBool(values: LegacyPreferenceValues, key: LegacyPreferenceKey, fallback: boolean): boolean {
  const value = values[key];
  if (value === "1") return true;
  if (value === "0") return false;
  return fallback;
}

export function legacyConfigFromValues(values: LegacyPreferenceValues): TranscriptDisplayConfigV1 | undefined {
  if (!LEGACY_PREF_KEYS.some((key) => hasLegacyValue(values, key))) return undefined;
  let hookExits: HookExitDetail = "none";
  if (legacyBool(values, "transcriptHookExitsAll", false)) {
    hookExits = "all";
  } else if (legacyBool(values, "transcriptHookExitsNormal", false)) {
    hookExits = "successful";
  }
  return makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    {
      roundTimings: legacyBool(values, "transcriptRoundTimings", true),
      tokenCounts: legacyBool(values, "transcriptTokenCounts", false),
      estimatedCost: legacyBool(values, "showCost", false),
      systemEvents: true,
      promptEvents: legacyBool(values, "transcriptPromptLoaded", true),
      hookExits,
    },
  );
}

export const migrateLegacyConfig = legacyConfigFromValues;
export const configFromLegacyPrefs = legacyConfigFromValues;

export interface LegacyPreferenceWrites {
  readonly transcriptRoundTimings: boolean;
  readonly transcriptTokenCounts: boolean;
  readonly transcriptHookExitsAll: boolean;
  readonly transcriptHookExitsNormal: boolean;
  readonly transcriptPromptLoaded: boolean;
  readonly showCost: boolean;
}

export function legacyWritesFromConfig(config: TranscriptDisplayConfigV1): LegacyPreferenceWrites {
  const normalized = normalizeConfig(config);
  return {
    transcriptRoundTimings: normalized.advanced.roundTimings,
    transcriptTokenCounts: normalized.advanced.tokenCounts,
    transcriptHookExitsAll: normalized.advanced.hookExits === "all",
    transcriptHookExitsNormal: normalized.advanced.hookExits === "successful",
    transcriptPromptLoaded: normalized.advanced.promptEvents,
    showCost: normalized.advanced.estimatedCost,
  };
}

export function dualWriteLegacyPreferences(
  config: TranscriptDisplayConfigV1,
  write: (key: LegacyPreferenceKey, value: boolean) => void,
): void {
  const values = legacyWritesFromConfig(config);
  for (const key of LEGACY_PREF_KEYS) write(key, values[key]);
}

export const legacyPrefsFromConfig = legacyWritesFromConfig;
