import { formatTokenCount, type ModelDescriptor, type ModelListResponse } from "@evener/appwire-client";

// A picker entry is one selectable model: its name, the line beneath it
// (context size and price), and the registry notes that belong under it. The
// row's text is built here so the list, its selection, and its warnings
// cannot drift apart, and so the mapping is testable without a renderer.
export interface ModelPickerEntry {
	model: ModelDescriptor;
	title: string;
	/** "200K context · $3 in · $15 out per M", or what of it the catalog knows. */
	detail: string;
	warnings: string[];
}

/** Dollars per million tokens: whole dollars bare, anything else in cents. */
function dollars(amount: number): string {
	return Number.isInteger(amount) ? `$${amount}` : `$${amount.toFixed(2)}`;
}

export function modelPickerEntry(model: ModelDescriptor): ModelPickerEntry {
	const price = [
		model.inputCostPerMillion !== undefined ? `${dollars(model.inputCostPerMillion)} in` : "",
		model.outputCostPerMillion !== undefined ? `${dollars(model.outputCostPerMillion)} out` : "",
	].filter(Boolean);
	const detail = [
		model.contextWindow ? `${formatTokenCount(model.contextWindow)} context` : "",
		price.length > 0 ? `${price.join(" · ")} per M` : "",
	].filter(Boolean);
	return {
		model,
		title: model.displayName || model.model,
		detail: detail.join(" · "),
		warnings: model.warnings ?? [],
	};
}

export interface ModelSection {
	title: string;
	data: ModelPickerEntry[];
}

const sameModel = (a: ModelDescriptor, b: ModelDescriptor) => a.provider === b.provider && a.model === b.model;

/** The model sheet's sections (spec 8.5): "Recent", then one per provider in
 * the catalog's order, each holding the models that match `query`. The vision
 * model lists only models that can see images. A recent model this session
 * can't run is left out, since the hub's Recent spans every session. */
export function modelSections(catalog: ModelListResponse, query: string, visionOnly: boolean): ModelSection[] {
	const search = query.trim().toLowerCase();
	const shown = catalog.data.filter(
		(model) =>
			(!visionOnly || model.supportsVision !== false) &&
			`${model.displayName ?? ""} ${model.provider} ${model.model}`.toLowerCase().includes(search),
	);
	const recent = (catalog.recent ?? []).flatMap((entry) => shown.filter((model) => sameModel(model, entry)));
	const providers = new Map<string, ModelDescriptor[]>();
	for (const model of shown) providers.set(model.provider, [...(providers.get(model.provider) ?? []), model]);
	return [
		...(recent.length > 0 ? [{ title: "Recent", data: recent.map(modelPickerEntry) }] : []),
		...[...providers].map(([provider, models]) => ({ title: provider, data: models.map(modelPickerEntry) })),
	];
}
