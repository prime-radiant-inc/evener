import { sameJsonValue } from "@evener/appwire-client";
import { hasExactKeys, isNonBlankText } from "./checkpointRecord";
import type { NavigationActionBackend } from "./navigationActionRepository";

export interface ForkTarget {
	instanceId: string;
	entryIndex: number;
	preview: string;
}
export interface ForkChild {
	ref: string;
	title: string;
	input: string;
}
export interface ForkCheckpoint {
	id: string;
	target: ForkTarget;
	child?: ForkChild;
	draftPrepared?: true;
}
function valid(value: unknown): asserts value {
	if (!value) throw Error("Invalid fork recovery record.");
}
function record(value: unknown, required: string[], optional: string[] = []) {
	valid(hasExactKeys(value, required, optional));
	return value as Record<string, unknown>;
}
const text = isNonBlankText;
export function decodeForkTarget(value: unknown): ForkTarget {
	const r = record(value, ["instanceId", "entryIndex", "preview"]);
	valid(
		text(r.instanceId) &&
			typeof r.entryIndex === "number" &&
			Number.isSafeInteger(r.entryIndex) &&
			r.entryIndex > 0 &&
			typeof r.preview === "string",
	);
	return {
		instanceId: r.instanceId,
		entryIndex: r.entryIndex,
		preview: r.preview,
	};
}
function decodeChild(value: unknown, parentRef: string): ForkChild {
	const r = record(value, ["ref", "title", "input"]);
	valid(text(r.ref) && r.ref !== parentRef && typeof r.title === "string" && typeof r.input === "string");
	return { ref: r.ref, title: r.title, input: r.input };
}
function decode(value: unknown, parentRef: string): ForkCheckpoint {
	const r = record(value, ["id", "target"], ["child", "draftPrepared"]);
	valid(text(r.id));
	const result: ForkCheckpoint = {
		id: r.id,
		target: decodeForkTarget(r.target),
	};
	if (Object.hasOwn(r, "child")) result.child = decodeChild(r.child, parentRef);
	if (Object.hasOwn(r, "draftPrepared")) {
		valid(r.draftPrepared === true && result.child);
		result.draftPrepared = true;
	}
	return result;
}

/** Acknowledgement and composer preparation survive screen and connection loss. */
export function forkCheckpoints(hubId: string, parentRef: string, backend: NavigationActionBackend) {
	valid(text(hubId) && text(parentRef));
	const key = `evener.native.fork-checkpoint.${JSON.stringify([hubId, parentRef])}`;
	const load = () => {
		const raw = backend.get(key);
		return raw === null || raw === undefined ? null : decode(raw, parentRef);
	};
	const requireCurrent = (expected: ForkCheckpoint) => {
		const checked = decode(expected, parentRef),
			current = load();
		if (!current || !sameJsonValue(current, checked)) throw Error("The saved fork request changed.");
		return current;
	};
	const save = (value: ForkCheckpoint) => {
		backend.set(key, decode(value, parentRef));
		return decode(value, parentRef);
	};
	return {
		key,
		load,
		begin(target: ForkTarget) {
			const checked = decodeForkTarget(target);
			if (load()) throw Error("A previous fork request still needs review.");
			return save({ id: backend.createId(), target: checked });
		},
		acknowledge(expected: ForkCheckpoint, child: ForkChild) {
			const current = requireCurrent(expected);
			valid(!current.child);
			return save({ ...current, child: decodeChild(child, parentRef) });
		},
		prepareDraft(expected: ForkCheckpoint) {
			const current = requireCurrent(expected);
			valid(current.child);
			return save({ ...current, draftPrepared: true });
		},
		removeIf(expected: ForkCheckpoint) {
			const checked = decode(expected, parentRef),
				current = load();
			return !!current && sameJsonValue(current, checked) && backend.deleteIf(key, current);
		},
	};
}
export type ForkCheckpointRepository = ReturnType<typeof forkCheckpoints>;
