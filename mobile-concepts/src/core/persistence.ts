import type { Appearance, ConceptId, ScenarioId, TextScale } from "./model";
import type { PrototypeState } from "./state";

export const preferenceStorageKey = "evener-concepts.preferences.v1";

export interface PersistedPreferencesV1 {
  version: 1;
  concept: ConceptId | null;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  scenario: ScenarioId;
}

export const defaultPreferences: PersistedPreferencesV1 = {
  version: 1,
  concept: null,
  appearance: "system",
  textScale: "standard",
  reducedMotion: false,
  scenario: "baseline",
};

const concepts: readonly ConceptId[] = [
  "stillwater",
  "constellation",
  "field-notes",
];
const appearances: readonly Appearance[] = ["system", "light", "dark"];
const textScales: readonly TextScale[] = ["standard", "large", "accessibility"];
const scenarios: readonly ScenarioId[] = [
  "baseline",
  "loading",
  "empty",
  "offline",
  "error",
  "needs-attention",
  "multi-agent",
  "question",
  "completed",
  "voice",
  "long-content",
];
const preferenceKeys = [
  "version",
  "concept",
  "appearance",
  "textScale",
  "reducedMotion",
  "scenario",
] as const;

function defaults(): PersistedPreferencesV1 {
  return { ...defaultPreferences };
}

function isExactRecord(
  value: unknown,
): value is Record<(typeof preferenceKeys)[number], unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const keys = Object.keys(value);
  return (
    keys.length === preferenceKeys.length &&
    preferenceKeys.every((key) => Object.hasOwn(value, key))
  );
}

export function decodePreferences(raw: string | null): PersistedPreferencesV1 {
  if (raw === null) return defaults();
  try {
    const value: unknown = JSON.parse(raw);
    if (!isExactRecord(value)) return defaults();
    if (
      value.version !== 1 ||
      (value.concept !== null &&
        !concepts.includes(value.concept as ConceptId)) ||
      !appearances.includes(value.appearance as Appearance) ||
      !textScales.includes(value.textScale as TextScale) ||
      typeof value.reducedMotion !== "boolean" ||
      !scenarios.includes(value.scenario as ScenarioId)
    ) {
      return defaults();
    }
    return {
      version: 1,
      concept: value.concept as ConceptId | null,
      appearance: value.appearance as Appearance,
      textScale: value.textScale as TextScale,
      reducedMotion: value.reducedMotion,
      scenario: value.scenario as ScenarioId,
    };
  } catch {
    return defaults();
  }
}

export function encodePreferences(state: PrototypeState): string {
  const preferences: PersistedPreferencesV1 = {
    version: 1,
    concept: state.concept,
    appearance: state.appearance,
    textScale: state.textScale,
    reducedMotion: state.reducedMotion,
    scenario: state.scenario,
  };
  return JSON.stringify(preferences);
}
