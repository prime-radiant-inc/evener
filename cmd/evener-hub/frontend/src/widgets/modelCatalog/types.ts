import type { ModelDescriptor, ModelListDiagnostic } from "../../protocol/types.gen";

// The widget requires a display label, while the generated AppWire descriptor
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
