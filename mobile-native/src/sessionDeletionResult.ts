export type SessionDeletionResult =
	| { kind: "deleted" | "missing" }
	| { kind: "skipped"; reason: string };

export function localSessionId(ref: string): string | null {
	return /^local:([A-Za-z0-9]{22})$/.exec(ref)?.[1] ?? null;
}
function record(value: unknown): Record<string, unknown> {
	if (!value || typeof value !== "object" || Array.isArray(value))
		throw Error("Invalid session deletion result.");
	return value as Record<string, unknown>;
}
export function decodeSessionDeletionResult(
	value: unknown,
): SessionDeletionResult {
	const result = record(value);
	if (
		(result.kind === "deleted" || result.kind === "missing") &&
		Object.keys(result).length === 1
	)
		return { kind: result.kind };
	if (
		result.kind === "skipped" &&
		typeof result.reason === "string" &&
		result.reason.trim() &&
		Object.keys(result).length === 2
	)
		return { kind: "skipped", reason: result.reason };
	throw Error("Invalid session deletion result.");
}
export function sessionDeletionResult(
	ref: string,
	value: unknown,
): SessionDeletionResult {
	const id = localSessionId(ref),
		response = record(value);
	if (
		!id ||
		!Array.isArray(response.deleted) ||
		!Array.isArray(response.skipped) ||
		response.deleted.length + response.skipped.length > 1
	)
		throw Error("The hub returned an invalid session deletion result.");
	if (response.deleted.length) {
		if (response.deleted[0] !== id)
			throw Error("The hub returned a different deleted session.");
		return { kind: "deleted" };
	}
	if (response.skipped.length) {
		const skip = record(response.skipped[0]);
		if (skip.id !== id)
			throw Error("The hub returned a different skipped session.");
		return decodeSessionDeletionResult({
			kind: "skipped",
			reason: skip.reason,
		});
	}
	return { kind: "missing" };
}
