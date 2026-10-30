// The composer's trailing slot (spec 8.5): Stop only when there is nothing to
// send, so an image-only draft still reaches Send (RoboRev finding on #4720).
// The screen decides both this and whether the composer may rest, since only it
// sees the whole draft.
import { describe, expect, it } from "vitest";
import { composerRests, composerShowsStop } from "./composerSlot";

const idle = { connected: true, canStop: true, text: "", images: 0, hasSelections: false };

describe("the composer's Stop slot (spec 8.5)", () => {
	it("shows Stop only for an empty draft while the turn can be stopped", () => {
		expect(composerShowsStop(idle)).toBe(true);
	});

	it("keeps Send for text, however short", () => {
		expect(composerShowsStop({ ...idle, text: "x" })).toBe(false);
		// Whitespace is not a message, but it is also not nothing: the field
		// reads empty, so Stop still stands.
		expect(composerShowsStop({ ...idle, text: "   " })).toBe(true);
	});

	it("keeps Send for an image-only draft", () => {
		expect(composerShowsStop({ ...idle, images: 1 })).toBe(false);
		expect(composerShowsStop({ ...idle, images: 3 })).toBe(false);
	});

	it("keeps Send for a selection-only composition", () => {
		expect(composerShowsStop({ ...idle, hasSelections: true })).toBe(false);
	});

	it("keeps Send when the turn cannot be stopped, or the hub is away", () => {
		expect(composerShowsStop({ ...idle, canStop: false })).toBe(false);
		expect(composerShowsStop({ ...idle, connected: false })).toBe(false);
	});
});

describe("whether the composer may rest (spec 8.5)", () => {
	const idle = { text: "", images: 0, editable: true };

	it("rests only for a truly empty draft", () => {
		expect(composerRests(idle)).toBe(true);
	});

	it("stays open for text, and for whitespace the field visibly holds", () => {
		expect(composerRests({ ...idle, text: "x" })).toBe(false);
		// Stop treats whitespace as nothing; rest must not, or the field would
		// collapse while holding visible spaces. The two rules differ on purpose.
		expect(composerRests({ ...idle, text: "   " })).toBe(false);
	});

	it("stays open for an image-only draft", () => {
		expect(composerRests({ ...idle, images: 1 })).toBe(false);
	});

	it("stays open while the field cannot take focus", () => {
		expect(composerRests({ ...idle, editable: false })).toBe(false);
	});
});
