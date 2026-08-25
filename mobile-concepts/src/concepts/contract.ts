import type { ComponentType } from "react";
import type { ConceptId, Platform } from "../core/model";
import type { PlatformPrimitives } from "../core/platform";
import type { PrototypeAction, PrototypeState } from "../core/state";

export interface ConceptRendererProps {
  state: PrototypeState;
  dispatch: (action: PrototypeAction) => void;
  platform: Platform;
  primitives: PlatformPrimitives;
  onOpenConceptSwitcher: () => void;
  onOpenLabControls: () => void;
}

export interface ConceptModule {
  id: ConceptId;
  Renderer: ComponentType<ConceptRendererProps>;
}
