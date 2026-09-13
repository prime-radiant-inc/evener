import { describe, expect, it } from "vitest";
import { makeTranscriptDisplayConfig } from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import { nativeTranscriptDrafts } from "./nativePreferenceDrafts";
import { TranscriptDraftRepository } from "./preferenceDraftRepository";

const config = makeTranscriptDisplayConfig();
const checkpoint = { id: "old", baseRevision: 4, config, writeUncertain: true };
function backend() {
	const values = new Map<string, unknown>();
	let id = 0;
	return {
		createId: () => String(++id),
		get: (key: string) => values.get(key),
		set: (key: string, value: unknown) => {
			values.set(key, structuredClone(value));
		},
		delete: (key: string) => {
			values.delete(key);
		},
		deleteIf: (key: string, value: unknown) => {
			if (JSON.stringify(values.get(key)) === JSON.stringify(value))
				values.delete(key);
		},
	};
}
describe("transcript draft persistence", () => {
	it("restores normalized checkpoints and keeps hubs separate", () => {
		const disk = backend();
		const a = new TranscriptDraftRepository(nativeTranscriptDrafts("a", disk));
		const b = new TranscriptDraftRepository(nativeTranscriptDrafts("b", disk));
		a.save(checkpoint);
		b.save({ ...checkpoint, id: "b", baseRevision: 9 });
		expect(a.load()).toEqual(checkpoint);
		a.remove();
		expect(a.load()).toBeNull();
		expect(b.load()?.baseRevision).toBe(9);
	});
	it("rejects invalid checkpoints rather than treating corrupt data as an empty draft", () => {
		const storage = nativeTranscriptDrafts("hub", backend());
		const repository = new TranscriptDraftRepository(storage);
		storage.save({ ...checkpoint, id: "" });
		expect(() => repository.load()).toThrow(
			"Invalid transcript preference draft",
		);
		expect(() =>
			repository.save({ ...checkpoint, baseRevision: -1 }),
		).toThrow();
		expect(() =>
			repository.save({
				...checkpoint,
				config: { ...config, version: 2 } as never,
			}),
		).toThrow();
	});
	it("conditionally removes only the acknowledged operation even for identical proposals", () => {
		const repository = new TranscriptDraftRepository(
			nativeTranscriptDrafts("hub", backend()),
		);
		repository.save(checkpoint);
		repository.save({ ...checkpoint, id: "new" });
		repository.removeIf(checkpoint);
		expect(repository.load()?.id).toBe("new");
		repository.removeIf({ ...checkpoint, id: "new" });
		expect(repository.load()).toBeNull();
	});
});
