import type {
	NavigationInvalidationTarget,
	NavigationMutation,
	NavigationReadParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	decodeNavigationResponse,
	materializeNavigationResource,
	normalizedGraphFromSnapshot,
} from "../../cmd/evener-hub/frontend/src/stores/navigation/codec";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { resourceKeyFor } from "./navigationPages";

function resourceFor(
	target: NavigationInvalidationTarget,
): NavigationReadParams | null {
	const base = { representationVersion: 2, resource: target.kind };
	switch (target.kind) {
		case "manifest":
			return base;
		case "section":
			return { ...base, section: target.section, limit: 50 };
		case "pin_catalog":
			return { ...base, limit: 50 };
		case "pin_section":
			return { ...base, sectionId: target.sectionId, limit: 50 };
		case "catalog":
			return { ...base, catalog: target.catalog, limit: 50 };
		case "project":
			return { ...base, projectKey: target.projectKey };
		// This invalidates loaded project pages without an individual version.
		case "all_loaded_projects":
			return null;
		default:
			throw Error("Unknown navigation change.");
	}
}

export async function navigationReadback(
	client: ConversationClientLike,
	receipt: NavigationMutation | null | undefined,
	current: () => boolean,
	confirmReceipt = false,
) {
	const check = () => {
		if (!current())
			throw Error("This check belongs to another screen or connection.");
	};
	let generationId: string | undefined;
	const read = async (
		input: Omit<NavigationReadParams, "representationVersion">,
	) => {
		const params = { ...input, representationVersion: 2 };
		check();
		const key = resourceKeyFor(params);
		const wire = await client.request("evener/navigation/read", {
			...params,
			representationVersion: 2,
		});
		check();
		const decoded = decodeNavigationResponse(key, undefined, wire);
		if (decoded.status !== "snapshot" && decoded.status !== "gone")
			throw Error("Navigation could not be refreshed.");
		if (generationId && decoded.version.generationId !== generationId)
			throw Error("The hub restarted during the check. Refresh to try again.");
		if (receipt?.generation_id === decoded.version.generationId) {
			const floor = Math.max(
				0,
				...receipt.targets
					.filter((target) => {
						const resource = resourceFor(target);
						return (
							resource &&
							resource.resource === params.resource &&
							resource.section === params.section &&
							resource.sectionId === params.sectionId &&
							resource.catalog === params.catalog &&
							resource.projectKey === params.projectKey
						);
					})
					.map((target) => target.revision ?? 0),
			);
			if (decoded.version.revision < floor)
				throw Error(
					"Navigation is older than the acknowledged change. Refresh to try again.",
				);
		}
		return {
			...decoded,
			data:
				decoded.status === "snapshot"
					? materializeNavigationResource({
							key,
							graph: normalizedGraphFromSnapshot(decoded.snapshot),
							version: decoded.version,
							presence: "present",
						})
					: null,
		};
	};
	const manifestParams = { resource: "manifest" };
	const manifest = await read(manifestParams);
	if (manifest.status !== "snapshot")
		throw Error("Hub navigation is unavailable.");
	generationId = manifest.version.generationId;
	if (confirmReceipt && receipt && receipt.generation_id !== generationId)
		throw Error(
			"The hub restarted before the change could be confirmed. Refresh to check its current state.",
		);
	if (receipt?.generation_id === generationId) {
		for (const target of receipt.targets) {
			const params = resourceFor(target);
			if (!params) continue;
			const result = await read(params);
			if (result.version.revision < (target.revision ?? 0))
				throw Error(
					"Navigation is older than the acknowledged change. Refresh to try again.",
				);
		}
	}
	return {
		generationId,
		read,
		finish: async () => {
			const after = await read(manifestParams);
			if (after.status !== "snapshot")
				throw Error("Hub navigation is unavailable.");
		},
	};
}
