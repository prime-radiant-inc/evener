import {
  accessibleConfigSummary,
  configFingerprint,
  resolveEffectiveConfig,
  type TranscriptDisplayConfigV1,
  type ViewportClass,
} from "@evener/appwire-client";
import { transitionTranscriptViews } from "../../panes/session/transcript/flow/transcriptViewRegistry";

interface EffectiveLayers {
  viewport: ViewportClass;
  local: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  hub: Partial<Record<ViewportClass, { revision: number; config: TranscriptDisplayConfigV1 }>>;
}

function effectiveForLayers(layers: EffectiveLayers, layout: ViewportClass): TranscriptDisplayConfigV1 {
  return resolveEffectiveConfig({
    local: layers.local[layout],
    hub: layers.hub[layout],
    layout,
  });
}

export function publishEffectiveTransition(
  before: EffectiveLayers,
  after: EffectiveLayers,
  publish: () => void,
  targetLayout: ViewportClass,
  force = false,
): void {
  const beforeConfig = effectiveForLayers(before, before.viewport);
  const afterConfig = effectiveForLayers(after, after.viewport);
  const afterFingerprint = configFingerprint(afterConfig);
  const changed = configFingerprint(beforeConfig) !== afterFingerprint;
  if (!changed && !force) {
    publish();
    return;
  }
  transitionTranscriptViews(publish, accessibleConfigSummary(afterConfig), {
    fingerprint: afterFingerprint,
    targetLayout,
    force,
    prepareRemount: force,
    announce: changed,
  });
}
