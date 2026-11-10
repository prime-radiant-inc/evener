import { isPlainObject } from "@evener/appwire-client";

// Shared predicates for the durable checkpoint repositories (navigation
// actions, fork checkpoints, pin drafts). Each repository keeps its own
// `valid` assertion because its failure message is its own.

export function isNonBlankText(value: unknown): value is string {
	return typeof value === "string" && value.trim().length > 0;
}

// A plain object whose keys are all named, with every required key present.
export function hasExactKeys(value: unknown, required: string[], optional: string[] = []): boolean {
	return (
		isPlainObject(value) &&
		required.every((key) => Object.hasOwn(value, key)) &&
		Object.keys(value).every((key) => required.includes(key) || optional.includes(key))
	);
}

export function cloneJson<T>(value: T): T {
	return JSON.parse(JSON.stringify(value)) as T;
}
