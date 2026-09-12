import type { ModelDescriptor } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";

// A picker entry is one selectable model and the registry notes that belong
// under it. The label is built here so the list, its selection, and its
// warnings cannot drift apart, and so the mapping is testable without a
// renderer (mobile-native has no component test harness).
export interface ModelPickerEntry {
	model: ModelDescriptor;
	label: string;
	warnings: string[];
}

export function modelPickerEntries(
	models: ModelDescriptor[],
): ModelPickerEntry[] {
	return models.map((model) => ({
		model,
		label: `${model.displayName || model.model} · ${model.provider}`,
		warnings: model.warnings ?? [],
	}));
}
