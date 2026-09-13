import type {
	NavigationPinSectionDescriptor,
	NavigationSessionLocation,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	decodeNavigationResponse,
	materializeNavigationResource,
	normalizedGraphFromSnapshot,
} from "../../cmd/evener-hub/frontend/src/stores/navigation/codec";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import type { NavigationPages } from "./navigationPages";

export async function readPinLocation(
	client: ConversationClientLike,
	ref: string,
) {
	const key = { kind: "location", ref } as const;
	const wire = await client.request("evener/navigation/read", {
		representationVersion: 2,
		resource: "location",
		ref,
	});
	const decoded = decodeNavigationResponse(key, undefined, wire);
	let location: NavigationSessionLocation | null = null;
	if (decoded.status === "snapshot") {
		location = materializeNavigationResource({
			key,
			graph: normalizedGraphFromSnapshot(decoded.snapshot),
			version: decoded.version,
			presence: "present",
		}) as unknown as NavigationSessionLocation;
		if (location.ref !== ref || location.session?.ref !== ref)
			throw Error("The hub returned a different session location.");
	} else if (decoded.status !== "gone") {
		throw Error("The session location was not refreshed.");
	}
	return {
		generationId: decoded.version.generationId,
		revision: decoded.version.revision,
		location,
	};
}

export async function refreshPinNavigation(
	client: ConversationClientLike,
	pages: NavigationPages<NavigationPinSectionDescriptor>,
	{
		checkpoint,
		sessionRef,
		sectionId,
		current,
		confirmReceipt = false,
	}: {
		checkpoint?: NavigationActionCheckpoint;
		sessionRef?: string;
		sectionId?: string;
		current: () => boolean;
		confirmReceipt?: boolean;
	},
) {
	const checkCurrent = () => {
		if (!current())
			throw Error(
				"The pin navigation request no longer belongs to this screen.",
			);
	};
	checkCurrent();
	const operation = checkpoint?.operation;
	if (
		operation &&
		!["assignPin", "unpin", "renamePinSection", "deletePinSection"].includes(
			operation.kind,
		)
	)
		throw Error(
			"Return to the previous organization screen to confirm that change.",
		);
	const pendingRef =
		operation?.kind === "assignPin" || operation?.kind === "unpin"
			? operation.params.sessionRef
			: undefined;
	const refs = [
		...new Set([pendingRef, sessionRef].filter((ref): ref is string => !!ref)),
	];
	const locations = new Map<
		string,
		Awaited<ReturnType<typeof readPinLocation>>
	>();
	for (const ref of refs) {
		locations.set(ref, await readPinLocation(client, ref));
		checkCurrent();
	}
	if (confirmReceipt && checkpoint?.receipt)
		await pages.refreshAfter(checkpoint.receipt);
	else await pages.refresh();
	checkCurrent();
	const checkedPage = () => {
		const state = pages.getSnapshot();
		if (!state.loaded || state.loading || state.stale || state.error)
			throw Error(
				"The pinned sections could not be confirmed. Refresh to try again.",
			);
		return state;
	};
	let page = checkedPage();
	const assignedIds = [...locations.values()].flatMap((value) =>
		value.location?.pin_section_id ? [value.location.pin_section_id] : [],
	);
	const requestedIds = [...assignedIds, ...(sectionId ? [sectionId] : [])];
	const readAll =
		operation?.kind === "renamePinSection" ||
		operation?.kind === "deletePinSection";
	while (
		page.remaining > 0 &&
		(readAll ||
			requestedIds.some(
				(id) => !page.rows.some((section) => section.id === id),
			))
	) {
		const before = page;
		await pages.more();
		checkCurrent();
		page = checkedPage();
		if (
			page.rows.length <= before.rows.length &&
			page.remaining >= before.remaining
		)
			throw Error("The pinned section list did not advance.");
	}
	const version = pages.getResourceVersion();
	if (!version) throw Error("The pin catalog version could not be confirmed.");
	for (const value of locations.values())
		if (value.generationId !== version.generationId)
			throw Error(
				"The hub restarted while reading pin assignments. Refresh to try again.",
			);
	if (assignedIds.some((id) => !page.rows.some((section) => section.id === id)))
		throw Error(
			"A pin assignment changed while reading the sections. Refresh to try again.",
		);
	const receipt = checkpoint?.receipt;
	if (receipt) {
		if (confirmReceipt && version.generationId !== receipt.generation_id)
			throw Error(
				"The hub restarted before the pin change could be confirmed. Refresh to read its current state.",
			);
		if (version.generationId === receipt.generation_id) {
			const floor = Math.max(
				0,
				...receipt.targets
					.filter((target) => target.kind === "pin_catalog")
					.map((target) => target.revision ?? 0),
			);
			if (version.revision < floor)
				throw Error("The pin catalog is older than the acknowledged change.");
		}
	}
	checkCurrent();
	checkedPage();
	const location =
		locations.get(sessionRef ?? pendingRef ?? "")?.location ?? null;
	return {
		generationId: version.generationId,
		location,
		section:
			page.rows.find(
				(section) => section.id === (sectionId ?? location?.pin_section_id),
			) ?? null,
	};
}
