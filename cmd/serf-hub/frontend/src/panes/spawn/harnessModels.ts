// Harness/model helpers for the spawn form. "Serf-model harness" = the one
// whose models the model chip + sticky-default + stale-model logic apply to
// (kind "serf"); other harnesses (kind "codex") carry the model through
// unmanaged. Mirrors app_models.go's descriptor kinds ("serf" vs "codex").
import type { HarnessDescriptor } from "../../protocol/types.gen";

export function harnessUsesSerfModels(harnessId: string, harnesses: HarnessDescriptor[]): boolean {
  // The default (unset) harness is serf (web_spawn.go DefaultHarness "serf").
  if (harnessId === "" || harnessId === "serf") return true;
  const found = harnesses.find((h) => h.id === harnessId);
  return found ? found.kind === "serf" : false;
}

// Display label for a model, matching statusFormat.modelLabel: "provider/model"
// unless the model repeats or is missing the provider (then just the provider).
export function modelLabel(provider: string, model: string): string {
  return model && model !== provider ? `${provider}/${model}` : provider;
}
