// VisionModelSwitch is the per-session vision-model picker. It shares the
// status-row trigger and catalog picker with ModelSwitch, but only offers
// catalog entries explicitly marked as vision-capable.
import { useCallback } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { type ModelCatalog, type ModelCatalogEntry, useToasts } from "../../../widgets";
import { fetchModelCatalog } from "../../../widgets/modelCatalog/catalogClient";
import { mergeScopedCatalog } from "../../../widgets/modelCatalog/scopedCatalog";
import { ModelSwitchTrigger } from "./ModelSwitchTrigger";
import { modelLabel } from "./statusFormat";

const CURRENT_MODEL_ID = "";
const OFF_MODEL_ID = "off";

const PSEUDO_ENTRIES: ModelCatalogEntry[] = [
  { provider: "", model: CURRENT_MODEL_ID, displayName: "Current model" },
  { provider: "", model: OFF_MODEL_ID, displayName: "Off" },
];

export interface VisionModelSwitchProps {
  sessionRef: string;
  model: ThreadModel;
}

export function VisionModelSwitch({ sessionRef, model }: VisionModelSwitchProps) {
  const toasts = useToasts();
  const disabled = !model.capabilities.changeVisionModel;
  const currentLabel =
    model.visionModel === CURRENT_MODEL_ID
      ? modelLabel(model.modelProvider, model.model)
      : model.visionModel.toLowerCase() === OFF_MODEL_ID
        ? "Off"
        : model.visionModel;

  const loadCatalog = useCallback(async (): Promise<ModelCatalog> => {
    const [scoped, enrichment] = await Promise.all([
      threadsStore.getState().listModels(),
      fetchModelCatalog().catch(() => null),
    ]);
    const merged = mergeScopedCatalog(scoped.data, enrichment);
    const visionModels = merged.models.filter((entry) => entry.supportsVision === true);
    const visionRecent = merged.recent.filter((entry) => entry.supportsVision === true);
    return { ...merged, models: visionModels, recent: [...PSEUDO_ENTRIES, ...visionRecent] };
  }, []);

  async function handlePick(entry: ModelCatalogEntry): Promise<void> {
    const visionModel = entry.provider === "" ? entry.model : `${entry.provider}/${entry.model}`;
    try {
      await threadsStore.getState().setVisionModel(sessionRef, visionModel);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't change vision model", err));
    }
  }

  return (
    <ModelSwitchTrigger
      label={currentLabel}
      value={currentLabel}
      disabled={disabled}
      loadCatalog={loadCatalog}
      onPick={(entry) => void handlePick(entry)}
      actionLabel="change vision model"
      data-testid="vision-model-switch-trigger"
      valueTestId="vision-model-switch-value"
    />
  );
}
