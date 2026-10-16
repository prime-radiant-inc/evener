// The composer's trailing slot (spec 8.5): Stop only when there is nothing to
// send, so an image-only draft still reaches Send (RoboRev finding on #4720).
import { describe, expect, it } from "vitest";
import { composerShowsStop } from "./composerSlot";

const idle = { connected: true, canStop: true, text: "", images: 0 };

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

	it("keeps Send when the turn cannot be stopped, or the hub is away", () => {
		expect(composerShowsStop({ ...idle, canStop: false })).toBe(false);
		expect(composerShowsStop({ ...idle, connected: false })).toBe(false);
	});
});
