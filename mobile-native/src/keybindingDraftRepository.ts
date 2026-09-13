import type { KeybindingsRule } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";

export interface KeybindingDraftCheckpoint {
	id: string;
	baseRevision: number;
	rules: KeybindingsRule[];
	writeUncertain: boolean;
}

export interface KeybindingDraftStorage {
	createId(): string;
	load(): unknown;
	save(checkpoint: KeybindingDraftCheckpoint): void;
	remove(): void;
	removeIf(checkpoint: KeybindingDraftCheckpoint): void;
}

function invalid(): never {
	throw new Error("Invalid keybinding draft.");
}

export function keybindingRules(value: unknown): KeybindingsRule[] {
	if (!Array.isArray(value)) invalid();
	return value.map((item): KeybindingsRule => {
		if (item === null || typeof item !== "object" || Array.isArray(item))
			invalid();
		const rule = item as Record<string, unknown>;
		if (
			typeof rule.action !== "string" ||
			!rule.action.length ||
			(rule.chord !== null &&
				(typeof rule.chord !== "string" || !rule.chord.length))
		)
			invalid();
		return { action: rule.action, chord: rule.chord };
	});
}

function checkpoint(value: unknown): KeybindingDraftCheckpoint {
	if (value === null || typeof value !== "object" || Array.isArray(value))
		invalid();
	const item = value as Record<string, unknown>;
	if (
		typeof item.id !== "string" ||
		!item.id.length ||
		typeof item.baseRevision !== "number" ||
		!Number.isSafeInteger(item.baseRevision) ||
		item.baseRevision < 0 ||
		typeof item.writeUncertain !== "boolean"
	)
		invalid();
	return {
		id: item.id,
		baseRevision: item.baseRevision,
		rules: keybindingRules(item.rules),
		writeUncertain: item.writeUncertain,
	};
}

export class KeybindingDraftRepository {
	constructor(private readonly storage: KeybindingDraftStorage) {}
	createId(): string {
		return this.storage.createId();
	}
	load(): KeybindingDraftCheckpoint | null {
		const value = this.storage.load();
		return value === null ? null : checkpoint(value);
	}
	save(value: KeybindingDraftCheckpoint): void {
		this.storage.save(checkpoint(value));
	}
	remove(): void {
		this.storage.remove();
	}
	removeIf(value: KeybindingDraftCheckpoint): void {
		this.storage.removeIf(checkpoint(value));
	}
}
