import { isThreadNotFound } from "../../cmd/evener-hub/frontend/src/panes/session/chrome/sessionErrors";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { navigationReadback } from "./navigationReadback";
import { localSessionId } from "./sessionDeletionResult";

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
	const navigation = await navigationReadback(
		client,
		checkpoint?.receipt,
		current,
		confirmReceipt,
	);
	const generationId = navigation.generationId;
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
	await navigation.finish();
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
