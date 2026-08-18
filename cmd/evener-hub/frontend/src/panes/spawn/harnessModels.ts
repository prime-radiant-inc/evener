// Harness/model helpers for the spawn form. "Evener-model harness" = the one
// whose models the model chip + sticky-default + stale-model logic apply to
// (kind "evener"); other harnesses (kind "codex") carry the model through
// unmanaged. Mirrors app_models.go's descriptor kinds ("evener" vs "codex").
import type { HarnessDescriptor } from "../../protocol/types.gen";

export function harnessUsesEvenerModels(harnessId: string, harnesses: HarnessDescriptor[]): boolean {
  // The default (unset) harness is evener (web_spawn.go DefaultHarness "evener").
  if (harnessId === "" || harnessId === "evener") return true;
  const found = harnesses.find((h) => h.id === harnessId);
  return found ? found.kind === "evener" : false;
}
