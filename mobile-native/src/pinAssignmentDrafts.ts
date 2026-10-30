import { sameJsonValue } from "@evener/appwire-client";
import { cloneJson } from "./checkpointRecord";
import type { NavigationActionBackend } from "./navigationActionRepository";
export type PinAssignmentSelection = { kind: "existing"; sectionId: string } | { kind: "new"; name: string } | null;
export interface PinAssignmentDraft {
	id: string;
	selection: PinAssignmentSelection;
}
function valid(condition: unknown): asserts condition {
	if (!condition) throw new Error("Invalid pin assignment draft.");
}
function selection(value: unknown): PinAssignmentSelection {
	if (value === null) return null;
	valid(value !== null && typeof value === "object" && !Array.isArray(value));
	const record = value as Record<string, unknown>;
	if (record.kind === "existing") {
		valid(
			Object.keys(record).length === 2 && typeof record.sectionId === "string" && record.sectionId.trim().length > 0,
		);
		return { kind: "existing", sectionId: record.sectionId };
	}
	if (record.kind === "new") {
		valid(Object.keys(record).length === 2 && typeof record.name === "string");
		return { kind: "new", name: record.name };
	}
	throw new Error("Invalid pin assignment draft.");
}
function draft(value: unknown): PinAssignmentDraft {
	valid(value !== null && typeof value === "object" && !Array.isArray(value));
	const record = value as Record<string, unknown>;
	valid(Object.keys(record).length === 2 && typeof record.id === "string" && record.id.length > 0);
	return { id: record.id, selection: selection(record.selection) };
}
export function pinAssignmentDrafts(hubScope: string, sessionRef: string, backend: NavigationActionBackend) {
	valid(hubScope.trim().length > 0 && sessionRef.trim().length > 0);
	const key = `evener.native.pin-assignment.${JSON.stringify([hubScope, sessionRef])}`;
	const load = (): PinAssignmentDraft | null => {
		const raw = backend.get(key);
		return raw === null || raw === undefined ? null : cloneJson(draft(raw));
	};
	return {
		key,
		load,
		save(next: PinAssignmentSelection): PinAssignmentDraft {
			load();
			const id = backend.createId();
			valid(typeof id === "string" && id.length > 0);
			const result = { id, selection: cloneJson(selection(next)) };
			backend.set(key, cloneJson(result));
			return cloneJson(result);
		},
		removeIf(expected: PinAssignmentDraft): boolean {
			const current = load();
			if (!current || !sameJsonValue(current, expected)) return false;
			return backend.deleteIf(key, cloneJson(current));
		},
	};
}
