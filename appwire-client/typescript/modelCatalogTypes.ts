// The model catalog's view-model shapes: the model/list envelope both apps'
// pickers render from, shared by the web widget
// (cmd/evener-hub/frontend/src/widgets/modelCatalog) and native's launch
// catalog (mobile-native/src/hubModels.ts). Types only, no runtime.
import type { ModelDescriptor, ModelListDiagnostic } from "./types.gen";

// The pickers require a display label, while the generated AppWire descriptor
// makes it optional because daemon/source callers may know only an identity.
// Keeping the generated type as the source of truth prevents this view model
// from drifting when the model/list contract changes.
export type ModelCatalogEntry = Omit<ModelDescriptor, "displayName"> & { displayName: string };
export type ModelCatalogDiagnostic = ModelListDiagnostic;

export interface ModelCatalog {
  models: ModelCatalogEntry[];
  recent: ModelCatalogEntry[];
  diagnostics?: ModelCatalogDiagnostic[];
}
