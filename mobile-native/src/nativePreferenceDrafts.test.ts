import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import {
	type NativePreferenceDraftBackend,
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
} from "./nativePreferenceDrafts";

const config = makeTranscriptDisplayConfig();
const transcriptCheckpoint = {
	id: "t1",
	layout: "mobile" as const,
	baseRevision: 4,
	config,
	writeUncertain: true,
};
const keybindingCheckpoint = {
	id: "k1",
	baseRevision: 7,
	rules: [{ action: "palette.open", chord: "Meta+P" }],
	writeUncertain: false,
};

/** The device store, with the keys it was written under observable. */
function backend() {
	const values = new Map<string, unknown>();
	let id = 0;
	const port: NativePreferenceDraftBackend = {
		createId: () => String(++id),
		get: (key) => values.get(key),
		set: (key, value) => {
			values.set(key, structuredClone(value));
		},
		delete: (key) => {
			values.delete(key);
		},
		deleteIf: (key, value) => {
			if (JSON.stringify(values.get(key)) === JSON.stringify(value))
				values.delete(key);
		},
	};
	return { port, keys: () => [...values.keys()].sort() };
}

describe("native preference drafts", () => {
	it("keeps one hub's drafts out of another's", () => {
		const disk = backend();
		const a = nativeTranscriptDrafts("a", disk.port);
		const b = nativeTranscriptDrafts("b", disk.port);

		a.save(transcriptCheckpoint);
		b.save({ ...transcriptCheckpoint, id: "t2", baseRevision: 9 });

		expect(a.load()).toEqual(transcriptCheckpoint);
		expect(b.load()).toMatchObject({ baseRevision: 9 });
		a.removeIf(transcriptCheckpoint);
		expect(a.load()).toBeNull();
		expect(b.load()).toMatchObject({ baseRevision: 9 });
	});

	it("keeps the two sections' drafts out of each other, on one hub", () => {
		const disk = backend();
		const transcript = nativeTranscriptDrafts("hub", disk.port);
		const keybindings = nativeKeybindingDrafts("hub", disk.port);

		transcript.save(transcriptCheckpoint);
		keybindings.save(keybindingCheckpoint);

		expect(disk.keys()).toEqual([
			"evener.native.keybinding-draft.hub",
			"evener.native.transcript-draft.hub",
		]);
		transcript.removeIf(transcriptCheckpoint);
		expect(transcript.load()).toBeNull();
		expect(keybindings.load()).toEqual(keybindingCheckpoint);
	});

	it("refuses a blank hub id rather than colliding every hub on one key", () => {
		const disk = backend();
		expect(() => nativeTranscriptDrafts(" ", disk.port)).toThrow(
			"A hub id is required",
		);
		expect(() => nativeKeybindingDrafts("", disk.port)).toThrow(
			"A hub id is required",
		);
	});
});
