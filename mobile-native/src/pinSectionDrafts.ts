import type { NavigationActionBackend } from "./navigationActionRepository";

export interface PinSectionDraft {
	id: string;
	name: string;
}
function valid(condition: unknown): asserts condition {
	if (!condition) throw Error("Invalid pin section draft.");
}
function draft(value: unknown): PinSectionDraft {
	valid(value !== null && typeof value === "object" && !Array.isArray(value));
	const record = value as Record<string, unknown>;
	valid(
		Object.keys(record).length === 2 &&
			typeof record.id === "string" &&
			record.id.trim().length > 0 &&
			typeof record.name === "string",
	);
	return { id: record.id, name: record.name };
}
export function pinSectionDrafts(
	hubId: string,
	sectionId: string,
	backend: NavigationActionBackend,
) {
	valid(hubId.trim().length > 0 && sectionId.trim().length > 0);
	const key = `evener.native.pin-section-name.${JSON.stringify([hubId, sectionId])}`;
	const load = (): PinSectionDraft | null => {
		const raw = backend.get(key);
		return raw === null || raw === undefined ? null : draft(raw);
	};
	return {
		key,
		load,
		save(name: string): PinSectionDraft {
			// Corrupt storage must be recovered before replacing an existing proposal.
			load();
			const next = draft({ id: backend.createId(), name });
			backend.set(key, { ...next });
			return next;
		},
		removeIf(expected: PinSectionDraft): boolean {
			const checked = draft(expected),
				current = load();
			if (
				!current ||
				current.id !== checked.id ||
				current.name !== checked.name
			)
				return false;
			return backend.deleteIf(key, current);
		},
	};
}
