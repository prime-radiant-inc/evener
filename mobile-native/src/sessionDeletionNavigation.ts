import { isThreadNotFound } from "../../cmd/evener-hub/frontend/src/panes/session/chrome/sessionErrors";
import type {
	NavigationInvalidationTarget,
	NavigationReadParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { decodeNavigationResponse } from "../../cmd/evener-hub/frontend/src/stores/navigation/codec";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { resourceKeyFor } from "./navigationPages";
import { localSessionId } from "./sessionDeletionResult";

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
		// This target invalidates whatever project pages a client has loaded; it
		// carries no individual resource version. The exact thread is read below.
		case "all_loaded_projects":
			return null;
		default:
			throw Error("Unknown navigation change.");
	}
}

export async function readSessionDeletion(
	client: ConversationClientLike,
	ref: string,
	checkpoint: NavigationActionCheckpoint | undefined,
	current: () => boolean,
	confirmReceipt = false,
) {
	const check = () => {
		if (!current())
			throw Error(
				"This session check belongs to a different screen or connection.",
			);
	};
	check();
	const id = localSessionId(ref);
	if (!id) throw Error("Only saved sessions on this hub can be deleted.");
	if (
		checkpoint &&
		(checkpoint.operation.kind !== "deleteSession" ||
			checkpoint.operation.params.ref !== ref)
	)
		throw Error(
			"Return to the previous organization screen to check that change first.",
		);
	const read = async (params: NavigationReadParams) => {
		check();
		const wire = await client.request("evener/navigation/read", params);
		check();
		const value = decodeNavigationResponse(
			resourceKeyFor(params),
			undefined,
			wire,
		);
		if (value.status !== "snapshot" && value.status !== "gone")
			throw Error("Navigation could not be refreshed.");
		return value;
	};
	const params = { representationVersion: 2, resource: "manifest" };
	const manifest = await read(params);
	if (manifest.status !== "snapshot")
		throw Error("Hub navigation is unavailable.");
	const generationId = manifest.version.generationId;
	const receipt = checkpoint?.receipt;
	if (confirmReceipt && receipt && receipt.generation_id !== generationId)
		throw Error(
			"The hub restarted before deletion could be confirmed. Refresh to check the current state.",
		);
	if (receipt?.generation_id === generationId) {
		for (const target of receipt.targets) {
			const resource = resourceFor(target);
			if (!resource) continue;
			const response = await read(resource);
			if (
				response.version.generationId !== generationId ||
				response.version.revision < (target.revision ?? 0)
			)
				throw Error(
					"Navigation is older than the acknowledged deletion. Refresh to check again.",
				);
		}
	}
	let missing = false,
		eligible = false,
		title = "",
		instanceId = "";
	try {
		// Metadata reads neither resume a stopped session nor replace the reader's
		// subscription. Exact missing-thread errors also survive hub restart, when
		// an old navigation location tombstone is no longer retained.
		const { thread } = await client.request("thread/read", {
			ref,
			includeTurns: false,
			subscribe: false,
		});
		check();
		if (
			thread?.id !== id ||
			thread.evener?.ref !== ref ||
			typeof thread.status?.type !== "string"
		)
			throw Error("The hub returned a different or incomplete session.");
		eligible = thread.status.type === "notLoaded";
		title = typeof thread.name === "string" ? thread.name : "";
		instanceId = thread.evener.instanceId ?? thread.id;
		if (typeof instanceId !== "string" || !instanceId)
			throw Error("The session identity could not be checked.");
	} catch (error) {
		if (
			!isThreadNotFound(error) ||
			!(error instanceof Error) ||
			error.message !== `thread not found: ${id}`
		)
			throw error;
		missing = true;
	}
	check();
	const after = await read(params);
	if (
		after.status !== "snapshot" ||
		after.version.generationId !== generationId
	)
		throw Error(
			"The hub restarted while checking this session. Refresh to try again.",
		);
	if (
		!missing &&
		(checkpoint?.deletion?.kind === "deleted" ||
			checkpoint?.deletion?.kind === "missing")
	)
		throw Error(
			"The session is still present after the deletion acknowledgement. Refresh to check again.",
		);
	return {
		generationId,
		missing,
		eligible,
		title,
		instanceId,
		settled: !checkpoint || missing || checkpoint.deletion?.kind === "skipped",
		skippedReason:
			checkpoint?.deletion?.kind === "skipped"
				? checkpoint.deletion.reason
				: null,
	};
}
