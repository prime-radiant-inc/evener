import type { NotesHumanSetResponse, SessionURL, ThreadCapabilities } from "@evener/appwire-client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { SyncStringStorage } from "../syncStringStorage";
import {
	NOTE_LIMIT,
	NotesController,
	notesBarPreview,
	noteStatusLine,
	SAVE_AFTER_BLUR_MS,
} from "./sessionNotes";

const sharedNotes = { sharedNotes: true } as ThreadCapabilities;
const url = (id: string, label?: string): SessionURL => ({ id, url: `https://example.com/${id}`, label });

describe("the notes bar (spec 8.8)", () => {
	const preview = (humanNote: string, agentNote: string, sessionUrls: SessionURL[], capabilities = sharedNotes) =>
		notesBarPreview({ humanNote, agentNote, sessionUrls, capabilities });

	it("previews your note first, and says in words that links exist", () => {
		expect(preview("Don't skip\n  or quarantine tests.", "Agent says", [url("a"), url("b"), url("c")])).toEqual({
			glyph: "person",
			text: "Your note: Don't skip or quarantine tests.",
			links: "3 links",
		});
	});

	it("falls back to the agent's note, then the links", () => {
		expect(preview("", "Race is in the drain.", [url("a")])).toEqual({
			glyph: "sparkles",
			text: "Agent's note: Race is in the drain.",
			links: "1 link",
		});
		expect(preview("", "", [url("a", "PR #2331")])).toEqual({ glyph: "link", text: "PR #2331" });
		expect(preview("", "", [url("a")])).toEqual({ glyph: "link", text: "https://example.com/a" });
		expect(preview("", "", [url("a"), url("b")])).toEqual({ glyph: "link", text: "2 links" });
	});

	it("is absent with nothing in it, or when the session has no shared notes", () => {
		expect(preview("", "  ", [])).toBeNull();
		expect(preview("A note", "", [], { sharedNotes: false } as ThreadCapabilities)).toBeNull();
	});
});

describe("the editor's status line", () => {
	it.each([
		["clean", false, "Your note stays on this session. Saving it will wake the agent."],
		["editing", true, "Your note stays on this session. The agent is told when it changes."],
		["scheduled", false, "Saves in 10 seconds, or when you close this."],
		["saving", false, "Saving…"],
		["saved", true, "Saved"],
		["failed", false, "Couldn't save your note yet. It's kept on this phone."],
	] as const)("%s, working %s", (phase, working, text) => {
		expect(noteStatusLine(phase, working)).toBe(text);
	});
});

function memoryStorage(values = new Map<string, string>()): SyncStringStorage & { values: Map<string, string> } {
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => void values.set(key, value),
		removeItemSync: (key) => void values.delete(key),
	};
}

function harness(
	over: { fail?: Error; projectionState?: "pending" | "removed"; working?: boolean; instanceId?: string } = {},
) {
	let writable = true;
	const requests: { method: string; params: Record<string, unknown> }[] = [];
	let saved = "";
	let failure = over.fail ?? null;
	const client = {
		request: async (method: string, params: Record<string, unknown>) => {
			requests.push({ method, params });
			if (failure) throw failure;
			if (method === "urls/remove") return {};
			return {
				note: String(params.note).replace(/\s+/g, " ").trim(),
				receipt: {
					clientMutationId: String(params.clientMutationId),
					disposition: "applied",
					threadId: "thread-1",
					projectionState: over.projectionState ?? "pending",
				},
			} satisfies NotesHumanSetResponse;
		},
	} as unknown as Pick<ConversationClientLike, "request">;
	const storage = memoryStorage();
	let id = 0;
	const make = () =>
		new NotesController({
			client,
			hubId: "hub-1",
			ref: "local:s1",
			instanceId: () => ("instanceId" in over ? over.instanceId : "instance-1"),
			savedNote: () => saved,
			working: () => over.working ?? false,
			writable: () => writable,
			storage,
			uuid: () => `m-${++id}`,
		});
	return {
		requests,
		storage,
		make,
		setSaved: (value: string) => {
			saved = value;
		},
		recover: () => {
			failure = null;
		},
		setWritable: (value: boolean) => {
			writable = value;
		},
	};
}

describe("saving your note (spec 8.8; Review Focus 5)", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it("sends nothing once the session stops taking notes, and keeps the note on this phone", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("Fix causes");
		notes.blur();
		// The session ends within the ten seconds.
		hub.setWritable(false);
		await vi.advanceTimersByTimeAsync(SAVE_AFTER_BLUR_MS);
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(hub.requests).toEqual([]);
		expect([...hub.storage.values.keys()].some((key) => key.includes("note-draft"))).toBe(true);
		// No save is armed any more, so the status line mustn't promise one.
		expect(notes.getSnapshot().phase).toBe("editing");
	});

	it("sends no newer text typed during a save once the session stops taking notes", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("first");
		const saving = notes.flush();
		notes.edit("second");
		hub.setWritable(false);
		await saving;
		expect(hub.requests.map((request) => request.params.note)).toEqual(["first"]);
		expect(notes.getSnapshot()).toMatchObject({ text: "second", phase: "editing" });
	});

	it("stops promising a save when the hub's note catches up to the scheduled text", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("same");
		notes.blur();
		hub.setSaved("same");
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(notes.getSnapshot().phase).toBe("clean");
		expect([...hub.storage.values.keys()].some((key) => key.includes("note-draft"))).toBe(false);
	});

	it("goes back to clean without saving again when you edit back to the hub's note during a save", async () => {
		const hub = harness();
		hub.setSaved("A");
		const notes = hub.make();
		notes.sync();
		notes.edit("B");
		const saving = notes.flush();
		notes.edit("A");
		await saving;
		expect(hub.requests.map((request) => request.params.note)).toEqual(["B"]);
		expect(notes.getSnapshot().phase).toBe("clean");
	});

	it("saves ten seconds after you leave the field, and focusing again cancels that", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("Fix causes");
		notes.blur();
		expect(notes.getSnapshot().phase).toBe("scheduled");
		notes.focus();
		await vi.advanceTimersByTimeAsync(SAVE_AFTER_BLUR_MS);
		expect(hub.requests).toEqual([]);
		notes.blur();
		await vi.advanceTimersByTimeAsync(SAVE_AFTER_BLUR_MS);
		expect(hub.requests).toEqual([
			{
				method: "notes/human/set",
				params: { ref: "local:s1", clientMutationId: "m-1", expectedInstanceId: "instance-1", note: "Fix causes" },
			},
		]);
		expect(notes.getSnapshot()).toEqual({ text: "Fix causes", phase: "saved" });
	});

	it("doesn't resend a note it just saved before the hub's own echo catches savedNote() up (RoboRev #2769 round 3)", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("Wake up");
		expect(await notes.flush()).toEqual({ saved: true, woke: true });
		// The hub hasn't pushed evener/notes/updated back yet, so savedNote()
		// (hub.setSaved was never called) still lags what was just saved -
		// flush() must trust "saved" itself, not re-derive unsaved from that.
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(hub.requests).toHaveLength(1);
	});

	it("goes back to clean, not stuck on Saved, once it adopts a different note from the hub", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("Wake up");
		expect(await notes.flush()).toEqual({ saved: true, woke: true });
		expect(notes.getSnapshot().phase).toBe("saved");
		hub.setSaved("Someone else's later note");
		notes.sync();
		expect(notes.getSnapshot()).toEqual({ text: "Someone else's later note", phase: "clean" });
	});

	it("saves at once on flush and says whether it woke the agent", async () => {
		const idle = harness();
		const first = idle.make();
		first.edit("Wake up");
		expect(await first.flush()).toEqual({ saved: true, woke: true });
		const busy = harness({ working: true });
		const second = busy.make();
		second.edit("Mid-turn");
		expect(await second.flush()).toEqual({ saved: true, woke: false });
		const same = harness({ projectionState: "removed" });
		const third = same.make();
		third.edit("Unchanged");
		expect(await third.flush()).toEqual({ saved: true, woke: false });
	});

	it("does nothing on flush when nothing is unsaved", async () => {
		const hub = harness();
		expect(await hub.make().flush()).toEqual({ saved: false, woke: false });
		expect(hub.requests).toEqual([]);
	});

	it("keeps a note it couldn't save on the phone, and sends it from the next session open", async () => {
		const hub = harness({ fail: new Error("offline") });
		const notes = hub.make();
		notes.edit("Measure on magic-kingdom");
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(notes.getSnapshot().phase).toBe("failed");
		expect(hub.storage.values.get("evener.native.note-draft.hub-1")).toBe(
			JSON.stringify({ "local:s1": "Measure on magic-kingdom" }),
		);
		hub.recover();
		const reopened = hub.make();
		expect(reopened.getSnapshot()).toEqual({ text: "Measure on magic-kingdom", phase: "failed" });
		expect(await reopened.flush()).toEqual({ saved: true, woke: true });
		expect(hub.storage.values.has("evener.native.note-draft.hub-1")).toBe(false);
	});

	it("keeps a note typed with no connection", async () => {
		const hub = harness({ instanceId: undefined });
		const notes = hub.make();
		notes.edit("Offline thought");
		expect(await notes.flush()).toEqual({ saved: false, woke: false });
		expect(hub.requests).toEqual([]);
		expect(notes.getSnapshot().phase).toBe("failed");
	});

	it("stops at the hub's 1,000 characters rather than clipping silently later", () => {
		const notes = harness().make();
		notes.edit("x".repeat(NOTE_LIMIT + 50));
		expect(notes.getSnapshot().text).toHaveLength(NOTE_LIMIT);
	});

	it("clips by whole code points, not UTF-16 units, so it keeps the hub's full 1,000-rune allowance for astral characters (RoboRev #2769 round 2)", () => {
		const notes = harness().make();
		const emoji = "😀"; // one code point, two UTF-16 units - the daemon clamps by runes, not UTF-16 units.
		notes.edit(emoji.repeat(NOTE_LIMIT + 5));
		expect(notes.getSnapshot().text).toBe(emoji.repeat(NOTE_LIMIT));
	});

	it("follows the hub's note unless you have unsaved text", () => {
		const hub = harness();
		const notes = hub.make();
		hub.setSaved("From the web");
		notes.sync();
		expect(notes.getSnapshot().text).toBe("From the web");
		notes.edit("Mine");
		hub.setSaved("From the web, again");
		notes.sync();
		expect(notes.getSnapshot().text).toBe("Mine");
	});

	it("goes clean again when you type back to the hub's current text, so it keeps following it (RoboRev #2769)", () => {
		const hub = harness();
		const notes = hub.make();
		hub.setSaved("A");
		notes.sync();
		notes.edit("B");
		notes.edit("A");
		expect(notes.getSnapshot()).toEqual({ text: "A", phase: "clean" });
		// The hub moves on while you're back at its old text; since you're
		// clean again, sync() follows it rather than leaving a stale value
		// that a later blur would send and overwrite the hub's newer note.
		hub.setSaved("C");
		notes.sync();
		expect(notes.getSnapshot()).toEqual({ text: "C", phase: "clean" });
	});

	it("still keeps a draft when a save fails after you'd already typed back to the hub's text mid-flight (RoboRev #2769 round 2)", async () => {
		const hub = harness({ fail: new Error("offline") });
		hub.setSaved("A");
		const notes = hub.make();
		notes.edit("B");
		const flushing = notes.flush();
		// Reverting to the hub's current text while "B" is still in flight
		// goes clean and forgets the draft (the rule above) - but the failing
		// save for "B" hasn't settled yet, and "failed" must still mean a
		// draft is on disk, or a crash before the next flush would forget it.
		notes.edit("A");
		await flushing;
		expect(notes.getSnapshot()).toEqual({ text: "A", phase: "failed" });
		expect(hub.storage.values.get("evener.native.note-draft.hub-1")).toBe(JSON.stringify({ "local:s1": "A" }));
	});

	it("chains a second save for newer text typed while the first was in flight, rather than reporting it saved when it isn't (RoboRev #2769)", async () => {
		const hub = harness();
		const notes = hub.make();
		notes.edit("First");
		const saving = notes.flush();
		notes.edit("First and more");
		expect(await saving).toEqual({ saved: true, woke: true });
		// flush() didn't resolve until the newer text's own save landed too.
		expect(notes.getSnapshot()).toEqual({ text: "First and more", phase: "saved" });
		expect(hub.requests.map((request) => request.params.note)).toEqual(["First", "First and more"]);
		expect(hub.storage.values.has("evener.native.note-draft.hub-1")).toBe(false);
	});

	it("stops chaining once a save fails, rather than retrying in a tight loop", async () => {
		const hub = harness({ fail: new Error("offline") });
		const notes = hub.make();
		notes.edit("First");
		const saving = notes.flush();
		notes.edit("First and more");
		expect(await saving).toEqual({ saved: false, woke: false });
		expect(hub.requests).toHaveLength(1);
		expect(notes.getSnapshot().phase).toBe("failed");
	});

	it("keeps both sessions' drafts when two are open under the same hub (RoboRev #2769)", () => {
		const client = { request: async () => ({}) } as unknown as Pick<ConversationClientLike, "request">;
		const storage = memoryStorage();
		const make = (ref: string) =>
			new NotesController({
				client,
				hubId: "hub-1",
				ref,
				instanceId: () => "instance-1",
				savedNote: () => "",
				working: () => false,
				writable: () => true,
				storage,
				uuid: () => "m-1",
			});
		const first = make("local:s1");
		const second = make("local:s2");
		first.edit("From the first session");
		second.edit("From the second session");
		expect(JSON.parse(storage.values.get("evener.native.note-draft.hub-1") ?? "{}")).toEqual({
			"local:s1": "From the first session",
			"local:s2": "From the second session",
		});
	});
});

describe("removing a link", () => {
	it("sends urls/remove, and counts a link that is already gone as removed", async () => {
		const hub = harness();
		expect(await hub.make().removeLink("url-1")).toBe(true);
		expect(hub.requests[0]).toEqual({
			method: "urls/remove",
			params: { ref: "local:s1", clientMutationId: "m-1", expectedInstanceId: "instance-1", id: "url-1" },
		});
		const gone = harness({ fail: new Error('no URL entry with id "url-1"') });
		expect(await gone.make().removeLink("url-1")).toBe(true);
		const broken = harness({ fail: new Error("offline") });
		expect(await broken.make().removeLink("url-1")).toBe(false);
	});
});
