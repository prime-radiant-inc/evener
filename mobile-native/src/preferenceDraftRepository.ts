import type { TranscriptDisplayConfigV1 } from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import { normalizeConfig } from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";

export interface TranscriptDraftCheckpoint {
	id: string;
	baseRevision: number;
	config: TranscriptDisplayConfigV1;
	writeUncertain: boolean;
}

export interface TranscriptDraftStorage {
	createId(): string;
	load(): TranscriptDraftCheckpoint | null;
	save(checkpoint: TranscriptDraftCheckpoint): void;
	remove(): void;
	removeIf(checkpoint: TranscriptDraftCheckpoint): void;
}

export class TranscriptDraftRepository {
	constructor(private readonly storage: TranscriptDraftStorage) {}
	createId(): string {
		return this.storage.createId();
	}
	load(): TranscriptDraftCheckpoint | null {
		const value = this.storage.load();
		return value === null ? null : validateCheckpoint(value);
	}
	save(checkpoint: TranscriptDraftCheckpoint): void {
		this.storage.save(validateCheckpoint(checkpoint));
	}
	remove(): void {
		this.storage.remove();
	}
	removeIf(checkpoint: TranscriptDraftCheckpoint): void {
		this.storage.removeIf(validateCheckpoint(checkpoint));
	}
}

function validateCheckpoint(
	value: TranscriptDraftCheckpoint,
): TranscriptDraftCheckpoint {
	if (
		!value ||
		typeof value.id !== "string" ||
		value.id.length === 0 ||
		!Number.isSafeInteger(value.baseRevision) ||
		value.baseRevision < 0 ||
		typeof value.writeUncertain !== "boolean"
	)
		throw new Error("Invalid transcript preference draft.");
	return {
		id: value.id,
		baseRevision: value.baseRevision,
		config: normalizeConfig(value.config),
		writeUncertain: value.writeUncertain,
	};
}
